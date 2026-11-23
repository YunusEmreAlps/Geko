package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

const maxRunSnapshotBytes = 2 << 20

func loadRunSnapshot(ctx context.Context, source, server, token string) (investigation.RunSnapshot, error) {
	if investigation.ValidRunID(source) {
		if server != "" {
			base, err := remoteServerURL(server)
			if err != nil {
				return investigation.RunSnapshot{}, err
			}
			data, err := gekoGet(ctx, strings.TrimRight(base.String(), "/")+"/api/v1/runs/"+source, token)
			if err != nil {
				return investigation.RunSnapshot{}, err
			}
			var result investigation.RunSnapshot
			if len(data) > maxRunSnapshotBytes {
				return result, errors.New("run snapshot exceeds 2 MiB")
			}
			if err := json.Unmarshal(data, &result); err != nil {
				return result, errors.New("invalid run snapshot response")
			}
			if result.RunID != source {
				return result, errors.New("run snapshot ID mismatch")
			}
			return result, investigation.ValidateRunSnapshot(result)
		}
		store, err := openInvestigation(ctx)
		if err != nil {
			return investigation.RunSnapshot{}, err
		}
		defer store.Close()
		return store.RunSnapshot(ctx, source)
	}
	file, err := os.Open(source)
	if err != nil {
		return investigation.RunSnapshot{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRunSnapshotBytes+1))
	if err != nil {
		return investigation.RunSnapshot{}, err
	}
	if len(data) > maxRunSnapshotBytes {
		return investigation.RunSnapshot{}, errors.New("run snapshot exceeds 2 MiB")
	}
	var result investigation.RunSnapshot
	if err := json.Unmarshal(data, &result); err != nil {
		return result, errors.New("invalid run snapshot file")
	}
	return result, investigation.ValidateRunSnapshot(result)
}

func compareRunsCommand(ctx context.Context, args []string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	flags := flag.NewFlagSet("compare-runs", flag.ContinueOnError)
	flags.SetOutput(out)
	baseline := flags.String("baseline", "", "baseline run ID or run.json path")
	candidate := flags.String("candidate", "", "candidate run ID or run.json path")
	server := flags.String("server", "", "query a server; set GEKO_TOKEN for read access")
	asJSON := flags.Bool("json", false, "write structured comparison")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *baseline == "" || *candidate == "" {
		return errors.New("usage: geko compare-runs --baseline ID_OR_FILE --candidate ID_OR_FILE [--server URL] [--json]")
	}
	var result investigation.RunComparison
	if investigation.ValidRunID(*baseline) && investigation.ValidRunID(*candidate) && *server == "" {
		store, err := openInvestigation(ctx)
		if err != nil {
			return err
		}
		defer store.Close()
		result, err = store.CompareRuns(ctx, *baseline, *candidate)
		if err != nil {
			return err
		}
	} else if investigation.ValidRunID(*baseline) && investigation.ValidRunID(*candidate) && *server != "" {
		base, err := remoteServerURL(*server)
		if err != nil {
			return err
		}
		query := url.Values{"baseline": {*baseline}, "candidate": {*candidate}}
		data, err := gekoGet(ctx, strings.TrimRight(base.String(), "/")+"/api/v1/runs/compare?"+query.Encode(), os.Getenv("GEKO_TOKEN"))
		if err != nil {
			return err
		}
		var snapshots struct {
			Baseline  investigation.RunSnapshot `json:"baseline"`
			Candidate investigation.RunSnapshot `json:"candidate"`
		}
		if json.Unmarshal(data, &snapshots) != nil {
			return errors.New("invalid run comparison response")
		}
		result.Baseline, result.Candidate = snapshots.Baseline, snapshots.Candidate
		if result.Baseline.RunID != *baseline || result.Candidate.RunID != *candidate {
			return errors.New("run comparison ID mismatch")
		}
		result, err = investigation.CompareRunSnapshots(result.Baseline, result.Candidate)
		if err != nil {
			return err
		}
	} else {
		left, err := loadRunSnapshot(ctx, *baseline, *server, os.Getenv("GEKO_TOKEN"))
		if err != nil {
			return err
		}
		right, err := loadRunSnapshot(ctx, *candidate, *server, os.Getenv("GEKO_TOKEN"))
		if err != nil {
			return err
		}
		result, err = investigation.CompareRunSnapshots(left, right)
		if err != nil {
			return err
		}
	}
	if *asJSON {
		return json.NewEncoder(out).Encode(result)
	}
	_, err := io.WriteString(out, runComparisonMarkdown(result))
	return err
}

func runComparisonMarkdown(result investigation.RunComparison) string {
	var out strings.Builder
	fmt.Fprintf(&out, "### Geko run comparison: %s\n\n%s\n\n", result.Status, result.Reason)
	for _, side := range []struct {
		name     string
		snapshot investigation.RunSnapshot
	}{{"Baseline", result.Baseline}, {"Candidate", result.Candidate}} {
		if side.snapshot.CollectionStatus != "available" {
			fmt.Fprintf(&out, "%s snapshot unavailable; trace count unknown.\n\n", side.name)
			continue
		}
		fmt.Fprintf(&out, "%s `%s`: %d retained traces at capture.\n\n", side.name, side.snapshot.RunID, side.snapshot.Traces)
		for _, c := range side.snapshot.Contexts {
			fmt.Fprintf(&out, "%s scope: %s / %s, environment %s, version %s (%d traces).\n\n", side.name, code(c.Application), code(c.Service), code(c.Environment), code(c.Version), c.Traces)
		}
	}
	if len(result.Groups) > 0 {
		out.WriteString("| Observation | Evidence | Baseline | Candidate | Trace |\n| --- | --- | --- | --- | --- |\n")
		for _, change := range result.Groups {
			fmt.Fprintf(&out, "| %s | %s in %s (%s) | %d | %d | `%s` |\n", change.State, code(change.Group.Operation), code(change.Group.Service), code(change.Group.ExceptionType), change.BaselineCount, change.CandidateCount, change.Group.TraceID)
		}
	}
	return out.String()
}

func compareAfterRun(ctx context.Context, server, token, runID string, baseline *investigation.RunSnapshot, baselineErr error, dir string, out io.Writer) {
	result := investigation.RunComparison{Status: "unavailable", Reason: "Run comparison unavailable; check the diagnostic output. No fix is inferred.", Groups: []investigation.RunGroupChange{}}
	result.Candidate = investigation.UnavailableRun(runID)
	if baseline != nil {
		result.Baseline = *baseline
	}
	err := baselineErr
	if err == nil && baseline == nil {
		err = errors.New("baseline snapshot unavailable")
	}
	if err == nil {
		var right investigation.RunSnapshot
		if dir != "" {
			right, err = loadRunSnapshot(ctx, filepath.Join(dir, "run.json"), "", "")
		} else {
			right, err = loadRunSnapshot(ctx, runID, server, token)
		}
		if err == nil && right.RunID != runID {
			err = errors.New("candidate snapshot ID mismatch")
		}
		if err == nil {
			result, err = investigation.CompareRunSnapshots(*baseline, right)
		}
	}
	if err != nil {
		fmt.Fprintf(out, "geko: could not compare runs: %v\n", err)
	}
	markdown := runComparisonMarkdown(result)
	fmt.Fprintln(out, markdown)
	if dir == "" {
		return
	}
	data, writeErr := json.MarshalIndent(result, "", "  ")
	if writeErr == nil {
		writeErr = os.WriteFile(filepath.Join(dir, "comparison.json"), append(data, '\n'), 0o644)
	}
	if writeErr == nil {
		var file *os.File
		file, writeErr = os.OpenFile(filepath.Join(dir, "report.md"), os.O_APPEND|os.O_WRONLY, 0o644)
		if writeErr == nil {
			_, writeErr = io.WriteString(file, "\n"+markdown)
			writeErr = errors.Join(writeErr, file.Close())
		}
	}
	if writeErr != nil {
		fmt.Fprintf(out, "geko: could not save run comparison: %v\n", writeErr)
	}
}
