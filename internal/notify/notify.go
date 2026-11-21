// Package notify sends a webhook when an error group first appears or a
// resolved group recurs. It polls stored groups, so it adds no work to ingest.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/ownership"
)

type Format string

const (
	JSON    Format = "json"
	Slack   Format = "slack"
	Discord Format = "discord"
	Ntfy    Format = "ntfy"
)

const (
	// overlap rechecks groups received just before the previous poll, whose
	// export may have committed after that poll's query.
	overlap = 10 * time.Second
	// maxMessages bounds one poll; further events are summarized in one message.
	maxMessages = 5
	maxPages    = 10
)

// ParseFormat validates a format, inferring it from well-known webhook hosts.
func ParseFormat(format, webhook string) (Format, error) {
	if format == "" {
		parsed, _ := url.Parse(webhook)
		switch host := strings.ToLower(parsed.Hostname()); {
		case host == "hooks.slack.com":
			return Slack, nil
		case (host == "discord.com" || host == "discordapp.com") && strings.HasPrefix(parsed.Path, "/api/webhooks/"):
			return Discord, nil
		case host == "ntfy.sh":
			return Ntfy, nil
		}
		return JSON, nil
	}
	switch value := Format(format); value {
	case JSON, Slack, Discord, Ntfy:
		return value, nil
	}
	return "", errors.New("webhook format must be json, slack, discord or ntfy")
}

// Store is the subset of storage the notifier reads and updates.
type Store interface {
	FailureGroups(context.Context, investigation.FailureFilter, *investigation.GroupCursor) (investigation.FailureGroups, error)
	GroupNotified(context.Context, string) (time.Time, error)
	MarkGroupNotified(context.Context, string, time.Time) error
}

// Webhook is a destination for notifications.
type Webhook struct {
	URL    string
	Format Format
}

// Host names the webhook's host without revealing its secret path.
func (w Webhook) Host() string {
	parsed, err := url.Parse(w.URL)
	if err != nil {
		return ""
	}
	return parsed.Host
}

type Notifier struct {
	// webhook receives groups of applications without their own webhook. Its
	// URL is empty when only application webhooks are configured.
	webhook      Webhook
	applications map[string]Webhook
	baseURL      string
	store        Store
	client       *http.Client
	since        time.Time
	recent       map[string]time.Time
	owners       ownership.Owners
	digest       Digest
	// settled holds groups a full check has seen, so later checks can skip
	// grouping every stored failure while nothing new appears.
	settled map[string]time.Time
}

// New returns a notifier that reports groups first received after start.
// baseURL is the dashboard URL used in links. webhook may be empty when
// WithApplications adds application webhooks.
func New(webhook string, format Format, baseURL string, store Store, start time.Time) *Notifier {
	return &Notifier{
		webhook: Webhook{webhook, format}, baseURL: strings.TrimRight(baseURL, "/"), store: store,
		client: &http.Client{Timeout: 10 * time.Second}, since: start, recent: make(map[string]time.Time),
		settled: make(map[string]time.Time),
	}
}

// WithApplications sends groups of these applications to their own webhook
// instead of the shared one.
// WithOwners names each group's owning team in its notifications.
func (n *Notifier) WithOwners(owners ownership.Owners) *Notifier {
	n.owners = owners
	return n
}

func (n *Notifier) WithApplications(webhooks map[string]Webhook) *Notifier {
	n.applications = webhooks
	return n
}

func (n *Notifier) Format() Format { return n.webhook.Format }

// Host names the shared webhook's host without revealing its secret path.
func (n *Notifier) Host() string { return n.webhook.Host() }

// destination returns the webhook for an application's groups, and false
// when none is configured.
func (n *Notifier) destination(application string) (Webhook, bool) {
	if webhook, ok := n.applications[application]; ok {
		return webhook, true
	}
	return n.webhook, n.webhook.URL != ""
}

func (n *Notifier) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var digestAt time.Time
	if n.digest.Enabled {
		digestAt = n.digest.Next(time.Now())
	}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := n.Check(ctx, now); err != nil && ctx.Err() == nil {
				log.Printf("notifications: %v", err)
			}
			if n.digest.Enabled && !now.Before(digestAt) {
				if err := n.SendDigest(ctx, now); err != nil && ctx.Err() == nil {
					log.Printf("weekly digest: %v", err)
				}
				digestAt = n.digest.Next(now)
			}
		}
	}
}

type event struct {
	Kind  string // new_group, reopened_group, summary or test
	Group investigation.FailureGroup
	URL   string
	More  int
	// Application names whose webhook a test message is for.
	Application string
	// Owner is the team owning the group, from the ownership rules.
	Owner string
	// Digest and Summary describe a weekly digest.
	Digest  *digestSummary
	Summary string
}

// advance forgets notifications sent before window and starts the next check at now.
func (n *Notifier) advance(now, window time.Time) {
	for key, sent := range n.recent {
		if sent.Before(window) {
			delete(n.recent, key)
		}
	}
	n.since = now
}

// settledFor is how long a group stays settled after it was last seen.
const settledFor = time.Hour

// groups returns the groups observed since window, newest first. Without from
// they are grouped over every stored failure, so their first observation and
// count are exact; with it only failures since from are read.
func (n *Notifier) groups(ctx context.Context, from *time.Time, window time.Time) ([]investigation.FailureGroup, error) {
	var groups []investigation.FailureGroup
	var after *investigation.GroupCursor
	for range maxPages {
		page, err := n.store.FailureGroups(ctx, investigation.FailureFilter{Limit: investigation.MaxPageSize, GroupState: investigation.GroupAll, From: from}, after)
		if err != nil {
			return nil, err
		}
		for _, group := range page.Items {
			if group.LastObserved.Before(window) {
				return groups, nil
			}
			groups = append(groups, group)
		}
		if page.Next == nil {
			break
		}
		after = page.Next
	}
	return groups, nil
}

// Check sends notifications for groups first received, or reopened, since the
// previous check.
func (n *Notifier) Check(ctx context.Context, now time.Time) error {
	window := n.since.Add(-overlap)
	// Only failures since the window can raise an event. Group the whole store,
	// which a new group needs for its first observation, only when one of them
	// belongs to a group no full check has seen, or reopens one unannounced.
	groups, err := n.groups(ctx, &window, window)
	if err != nil {
		return err
	}
	full := false
	for _, group := range groups {
		if _, seen := n.settled[group.Key]; !seen {
			full = true
			break
		}
		if group.State == investigation.GroupReopened && group.StateChanged != nil {
			notified, err := n.store.GroupNotified(ctx, group.Key)
			if err != nil {
				return err
			}
			if notified.Before(*group.StateChanged) {
				full = true
				break
			}
		}
	}
	for _, group := range groups {
		n.settled[group.Key] = now
	}
	for key, seen := range n.settled {
		if now.Sub(seen) > settledFor {
			delete(n.settled, key)
		}
	}
	if !full {
		n.advance(now, window)
		return nil
	}
	if groups, err = n.groups(ctx, nil, window); err != nil {
		return err
	}
	var events []event
	for _, group := range groups {
		switch {
		case group.State == investigation.GroupOpen && !group.FirstObserved.Before(window):
			if _, sent := n.recent[group.Key]; !sent {
				events = append(events, event{Kind: "new_group", Group: group, Owner: n.owners.For(group.Application, group.Service)})
			}
		case group.State == investigation.GroupReopened && group.StateChanged != nil:
			notified, err := n.store.GroupNotified(ctx, group.Key)
			if err != nil {
				return err
			}
			if notified.Before(*group.StateChanged) {
				events = append(events, event{Kind: "reopened_group", Group: group, Owner: n.owners.For(group.Application, group.Service)})
			}
		}
	}

	// Each webhook gets at most maxMessages, then one summary of the rest.
	var sendErr error
	var order []string
	pending := make(map[string][]event)
	for _, e := range events {
		webhook, ok := n.destination(e.Group.Application)
		if !ok {
			continue
		}
		if _, seen := pending[webhook.URL]; !seen {
			order = append(order, webhook.URL)
		}
		pending[webhook.URL] = append(pending[webhook.URL], e)
	}
	for _, target := range order {
		queued := pending[target]
		webhook, _ := n.destination(queued[0].Group.Application)
		for i, e := range queued {
			if i == maxMessages {
				sendErr = errors.Join(sendErr, n.send(ctx, webhook, n.summary(len(queued)-maxMessages, n.ownApplication(e.Group.Application))))
				break
			}
			e.URL = n.groupURL(e.Group.Key)
			sendErr = errors.Join(sendErr, n.send(ctx, webhook, e))
		}
	}
	// Record every event, including summarized ones, so none repeats.
	for _, e := range events {
		if e.Kind == "new_group" {
			n.recent[e.Group.Key] = now
		} else if err := n.store.MarkGroupNotified(ctx, e.Group.Key, now); err != nil {
			sendErr = errors.Join(sendErr, err)
		}
	}
	n.advance(now, window)
	return sendErr
}

// Test sends a sample message to every webhook to confirm they work.
func (n *Notifier) Test(ctx context.Context) error {
	var err error
	if n.webhook.URL != "" {
		err = n.send(ctx, n.webhook, event{Kind: "test", URL: n.baseURL})
	}
	for _, application := range slices.Sorted(maps.Keys(n.applications)) {
		err = errors.Join(err, n.send(ctx, n.applications[application], event{Kind: "test", URL: n.baseURL, Application: application}))
	}
	return err
}

// ownApplication returns application if it has its own webhook.
func (n *Notifier) ownApplication(application string) *string {
	if _, ok := n.applications[application]; ok {
		return &application
	}
	return nil
}

// summary links to the open groups, of one application when it is set.
func (n *Notifier) summary(more int, application *string) event {
	e := event{Kind: "summary", More: more}
	if n.baseURL != "" {
		e.URL = n.baseURL + "/failures?view=groups"
		if application != nil {
			e.URL += "&application=" + url.QueryEscape("group:"+*application)
		}
	}
	return e
}

func (n *Notifier) groupURL(key string) string {
	if n.baseURL == "" {
		return ""
	}
	return n.baseURL + "/failures?group=" + url.QueryEscape(investigation.EncodeGroupKey(key))
}

func (e event) title() string {
	switch {
	case e.Kind == "new_group":
		return "New error group"
	case e.Kind == "reopened_group":
		return "Resolved error group recurred"
	case e.Kind == "test":
		return "Geko test notification"
	case e.Kind == "digest":
		return "Weekly error digest"
	default:
		return "More error group changes"
	}
}

// text describes the group without exception messages, which can contain
// secrets. Names come from instrumented services and are shortened.
func (e event) text() string {
	if e.Kind == "digest" {
		return e.Summary
	}
	if e.Kind == "test" {
		if e.Application != "" {
			return "Webhook notifications from Geko are working for " + clip(e.Application) + "."
		}
		return "Webhook notifications from Geko are working."
	}
	if e.Kind == "summary" {
		return fmt.Sprintf("%d more new or reopened error %s.", e.More, plural(int64(e.More), "group", "groups"))
	}
	g := e.Group
	failure := map[string]string{"exception": "Exception", "http_5xx": "HTTP 5xx", "span_error": "Span error"}[g.Classification]
	if kind := investigation.NamedExceptionType(g.ExceptionType); kind != "" {
		failure = kind
	}
	where := g.Service
	if g.Application != "" {
		where = g.Application + " / " + g.Service
	}
	if g.Environment != "" {
		where += " (" + g.Environment + ")"
	}
	lines := []string{clip(failure) + " in " + clip(g.Operation), clip(where)}
	detail := fmt.Sprintf("%d %s", g.Count, plural(g.Count, "trace", "traces"))
	if g.HTTPStatus != 0 {
		detail += fmt.Sprintf(" · evidence HTTP %d", g.HTTPStatus)
	}
	lines = append(lines, detail)
	if e.Owner != "" {
		lines = append(lines, "Owner: "+clip(e.Owner))
	}
	return strings.Join(lines, "\n")
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func clip(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < ' ' {
			return ' '
		}
		return r
	}, value)
	if utf8.RuneCountInString(value) <= 120 {
		return value
	}
	return string([]rune(value)[:119]) + "…"
}

var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func (n *Notifier) send(ctx context.Context, webhook Webhook, e event) error {
	text := e.text()
	var body []byte
	var err error
	contentType := "application/json"
	switch webhook.Format {
	case Slack:
		message := "*" + e.title() + "*\n" + slackEscaper.Replace(text)
		if e.URL != "" {
			message += "\n<" + slackEscaper.Replace(e.URL) + "|Open in Geko>"
		}
		body, err = marshal(map[string]string{"text": message})
	case Discord:
		message := "**" + e.title() + "**\n" + text
		if e.URL != "" {
			message += "\n" + e.URL
		}
		body, err = marshal(map[string]any{"content": message, "allowed_mentions": map[string]any{"parse": []string{}}})
	case Ntfy:
		body, contentType = []byte(text), "text/plain; charset=utf-8"
	default:
		payload := map[string]any{"event": e.Kind, "title": e.title(), "text": text}
		if e.URL != "" {
			payload["url"] = e.URL
		}
		switch e.Kind {
		case "new_group", "reopened_group":
			payload["group"] = e.Group
			if e.Owner != "" {
				payload["owner"] = e.Owner
			}
		case "summary":
			payload["count"] = e.More
		case "digest":
			payload["digest"] = e.Digest
		}
		body, err = marshal(payload)
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "Geko")
	if webhook.Format == Ntfy {
		req.Header.Set("Title", e.title())
		req.Header.Set("Tags", "warning")
		if e.URL != "" {
			req.Header.Set("Click", e.URL)
		}
	}
	response, err := n.client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // The URL can contain a secret.
		}
		return fmt.Errorf("send webhook to %s: %w", webhook.Host(), err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("send webhook to %s: %s", webhook.Host(), response.Status)
	}
	return nil
}

// marshal keeps <, > and & literal; Slack and Discord read them as markup.
func marshal(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(value)
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), err
}
