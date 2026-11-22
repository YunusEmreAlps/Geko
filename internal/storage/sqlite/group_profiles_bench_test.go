package sqlite

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

// Exercise the cases hidden by the dense, single-hour end-to-end fixture.
func BenchmarkGroupingProfiles(b *testing.B) {
	for _, profile := range []string{"mixed", "rare", "unique_versions"} {
		b.Run(profile, func(b *testing.B) {
			base := time.Now().UTC().Truncate(time.Hour).Add(-8 * time.Hour)
			store := openBenchGroupProfile(b, profile, base)
			var members, partitions, identities int
			if err := store.read.QueryRowContext(b.Context(), `SELECT
 (SELECT count(*) FROM failure_group_members),(SELECT count(*) FROM failure_group_partitions),
 (SELECT count(*) FROM failure_group_keys)`).Scan(&members, &partitions, &identities); err != nil {
				b.Fatal(err)
			}
			for _, window := range []struct {
				name     string
				from, to time.Time
			}{{"whole_hours", base, base.Add(7 * time.Hour)}, {"15_minutes", base.Add(5*time.Hour + 17*time.Minute), base.Add(5*time.Hour + 32*time.Minute)},
				{"two_edges", base.Add(time.Hour + 15*time.Minute), base.Add(5*time.Hour + 45*time.Minute)}} {
				filter := investigation.FailureFilter{Limit: 25, From: &window.from, To: &window.to}
				matching, args := legacyGroupMatches(filter)
				legacy, oldArgs, err := assembleGroupsQuery(filter, nil, matching, args)
				if err != nil {
					b.Fatal(err)
				}
				optimized, newArgs, err := failureGroupsQuery(filter, nil)
				if err != nil {
					b.Fatal(err)
				}
				oldRows, err := groupRows(b.Context(), store.read, legacy, oldArgs)
				if err != nil {
					b.Fatal(err)
				}
				newRows, err := groupRows(b.Context(), store.read, optimized, newArgs)
				if err != nil || !reflect.DeepEqual(newRows, oldRows) {
					b.Fatalf("profile %s/%s differs: %v", profile, window.name, err)
				}
				for _, engine := range []struct {
					name, query string
					args        []any
				}{{"legacy", legacy, oldArgs}, {"optimized", optimized, newArgs}} {
					b.Run(window.name+"/"+engine.name, func(b *testing.B) {
						b.ReportAllocs()
						for b.Loop() {
							if _, err := groupRows(b.Context(), store.read, engine.query, engine.args); err != nil {
								b.Fatal(err)
							}
						}
						b.ReportMetric(float64(partitions)/float64(members), "partitions/member")
						b.ReportMetric(float64(identities), "identities")
					})
				}
			}
		})
	}
}

func openBenchGroupProfile(b *testing.B, profile string, base time.Time) *Store {
	b.Helper()
	store, err := Open(b.Context(), filepath.Join(b.TempDir(), "profiles.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	for batch := range 100 {
		spans := benchExport(batch*50, 50)
		for i := range spans {
			number := batch*50 + i/5
			version := fmt.Sprintf("v%d", number/200%5)
			if profile == "unique_versions" {
				version = fmt.Sprintf("build-%d", number)
			}
			spans[i].ResourceAttributes["service.version"] = version
			if i%5 == 0 {
				spans[i].ServiceName = fmt.Sprintf("gateway-%d", number%3)
			}
			if profile == "rare" {
				spans[i].StatusCode, spans[i].Events = telemetry.StatusUnset, nil
				spans[i].Attributes = telemetry.Attributes{"http.response.status_code": 200}
				if number%100 == 0 && i%5 == 4 {
					spans[i].StatusCode = telemetry.StatusError
					spans[i].Events = []telemetry.Event{{Name: "exception", Attributes: telemetry.Attributes{"exception.type": "RareError"}}}
				}
			}
		}
		if err := store.WriteSpans(b.Context(), base.Add(time.Duration(batch)*4*time.Minute), spans); err != nil {
			b.Fatal(err)
		}
	}
	return store
}
