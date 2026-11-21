package notify

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

// Digest schedules a weekly summary on a weekday and hour in UTC.
type Digest struct {
	Weekday time.Weekday
	Hour    int
	Enabled bool
}

const (
	digestWindow = 7 * 24 * time.Hour
	// digestLines bounds each section of the message; the JSON payload
	// carries up to digestGroups of each.
	digestLines  = 3
	digestGroups = 10
)

// ParseDigest reads a weekday and an hour in UTC, such as "monday 09:00".
func ParseDigest(spec string) (Digest, error) {
	day, clock, ok := strings.Cut(strings.TrimSpace(spec), " ")
	when, err := time.Parse("15:04", strings.TrimSpace(clock))
	if !ok || err != nil || when.Minute() != 0 {
		return Digest{}, fmt.Errorf("digest %q must be a weekday and a whole hour in UTC, such as \"monday 09:00\"", spec)
	}
	for weekday := time.Sunday; weekday <= time.Saturday; weekday++ {
		if strings.EqualFold(day, weekday.String()) {
			return Digest{Weekday: weekday, Hour: when.Hour(), Enabled: true}, nil
		}
	}
	return Digest{}, fmt.Errorf("digest %q must start with a weekday such as monday", spec)
}

func (d Digest) String() string {
	return fmt.Sprintf("%s %02d:00 UTC", d.Weekday, d.Hour)
}

// Next returns the first scheduled time after t.
func (d Digest) Next(t time.Time) time.Time {
	t = t.UTC()
	next := time.Date(t.Year(), t.Month(), t.Day(), d.Hour, 0, 0, 0, time.UTC)
	next = next.AddDate(0, 0, (int(d.Weekday)-int(next.Weekday())+7)%7)
	if !next.After(t) {
		next = next.AddDate(0, 0, 7)
	}
	return next
}

// WithDigest sends a weekly digest to each webhook on the schedule.
func (n *Notifier) WithDigest(digest Digest) *Notifier {
	n.digest = digest
	return n
}

// digestSummary is one webhook's week: the groups that received traces, and
// the groups resolved during it.
type digestSummary struct {
	Application string                       `json:"application,omitempty"`
	From        time.Time                    `json:"from"`
	To          time.Time                    `json:"to"`
	Active      int                          `json:"active_groups"`
	New         []investigation.FailureGroup `json:"new_groups"`
	Reopened    []investigation.FailureGroup `json:"reopened_groups"`
	Resolved    []investigation.FailureGroup `json:"resolved_groups"`
	Largest     []investigation.FailureGroup `json:"largest_groups"`
}

// SendDigest sends every webhook the week before now: new, reopened and
// resolved groups and the largest active ones. It groups every stored
// failure, which is affordable once a week.
func (n *Notifier) SendDigest(ctx context.Context, now time.Time) error {
	from := now.Add(-digestWindow)
	active, err := n.groups(ctx, nil, from)
	if err != nil {
		return err
	}
	var resolved []investigation.FailureGroup
	var after *investigation.GroupCursor
	for range maxPages {
		page, err := n.store.FailureGroups(ctx, investigation.FailureFilter{Limit: investigation.MaxPageSize, GroupState: investigation.GroupResolved}, after)
		if err != nil {
			return err
		}
		for _, group := range page.Items {
			if group.StateChanged != nil && !group.StateChanged.Before(from) {
				resolved = append(resolved, group)
			}
		}
		if page.Next == nil {
			break
		}
		after = page.Next
	}
	type destination struct {
		webhook     Webhook
		application *string
	}
	var destinations []destination
	if n.webhook.URL != "" {
		destinations = append(destinations, destination{n.webhook, nil})
	}
	for _, application := range slices.Sorted(maps.Keys(n.applications)) {
		destinations = append(destinations, destination{n.applications[application], &application})
	}
	belongs := func(group investigation.FailureGroup, application *string) bool {
		if application != nil {
			return group.Application == *application
		}
		return n.ownApplication(group.Application) == nil
	}
	var sendErr error
	for _, target := range destinations {
		summary := &digestSummary{From: from.UTC(), To: now.UTC(), New: []investigation.FailureGroup{},
			Reopened: []investigation.FailureGroup{}, Resolved: []investigation.FailureGroup{}, Largest: []investigation.FailureGroup{}}
		if target.application != nil {
			summary.Application = *target.application
		}
		for _, group := range active {
			if !belongs(group, target.application) || group.State == investigation.GroupMuted {
				continue
			}
			summary.Active++
			switch {
			case group.State == investigation.GroupReopened:
				summary.Reopened = append(summary.Reopened, group)
			case group.State == investigation.GroupOpen && !group.FirstObserved.Before(from):
				summary.New = append(summary.New, group)
			}
			if group.State != investigation.GroupResolved {
				summary.Largest = append(summary.Largest, group)
			}
		}
		for _, group := range resolved {
			if belongs(group, target.application) {
				summary.Resolved = append(summary.Resolved, group)
			}
		}
		slices.SortStableFunc(summary.Largest, func(a, b investigation.FailureGroup) int { return cmp.Compare(b.Count, a.Count) })
		for _, list := range []*[]investigation.FailureGroup{&summary.New, &summary.Reopened, &summary.Resolved, &summary.Largest} {
			*list = (*list)[:min(len(*list), digestGroups)]
		}
		e := event{Kind: "digest", Digest: summary}
		if n.baseURL != "" {
			e.URL = n.baseURL + "/failures?view=groups&range=7d"
			if target.application != nil {
				e.URL += "&application=" + url.QueryEscape("group:"+*target.application)
			}
		}
		e.Summary = n.digestText(summary)
		sendErr = errors.Join(sendErr, n.send(ctx, target.webhook, e))
	}
	return sendErr
}

// digestText describes the week without exception messages.
func (n *Notifier) digestText(d *digestSummary) string {
	scope := "all applications"
	if d.Application != "" {
		scope = clip(d.Application)
	} else if len(n.applications) > 0 {
		scope = "applications without their own webhook"
	}
	lines := []string{fmt.Sprintf("%s to %s in %s: %d active %s, %d new, %d reopened, %d resolved.",
		d.From.Format("Jan 2"), d.To.Format("Jan 2"), scope, d.Active, plural(int64(d.Active), "group", "groups"),
		len(d.New), len(d.Reopened), len(d.Resolved))}
	shown := map[string]bool{}
	for _, section := range []struct {
		label  string
		groups []investigation.FailureGroup
	}{{"New", d.New}, {"Reopened", d.Reopened}, {"Largest", d.Largest}, {"Resolved", d.Resolved}} {
		listed := 0
		for _, group := range section.groups {
			// The largest groups are often new or reopened ones already listed.
			if listed == digestLines || section.label == "Largest" && shown[group.Key] {
				continue
			}
			shown[group.Key] = true
			listed++
			line := fmt.Sprintf("%s: %s in %s · %d %s", section.label, clip(groupFailure(group)), clip(group.Operation),
				group.Count, plural(group.Count, "trace", "traces"))
			if owner := n.owners.For(group.Application, group.Service); owner != "" {
				line += " · " + clip(owner)
			}
			lines = append(lines, line)
		}
	}
	if d.Active == 0 && len(d.Resolved) == 0 {
		lines = append(lines, "No error group received traces this week.")
	}
	return strings.Join(lines, "\n")
}

// groupFailure names a group's failure as notifications do.
func groupFailure(g investigation.FailureGroup) string {
	failure := map[string]string{"exception": "Exception", "http_5xx": "HTTP 5xx", "span_error": "Span error"}[g.Classification]
	if kind := investigation.NamedExceptionType(g.ExceptionType); kind != "" {
		failure = kind
	}
	return failure
}
