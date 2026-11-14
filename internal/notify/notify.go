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
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/YunusEmreAlps/geko/internal/investigation"
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

type Notifier struct {
	webhook string
	format  Format
	baseURL string
	store   Store
	client  *http.Client
	since   time.Time
	recent  map[string]time.Time
}

// New returns a notifier that reports groups first received after start.
// baseURL is the dashboard URL used in links.
func New(webhook string, format Format, baseURL string, store Store, start time.Time) *Notifier {
	return &Notifier{
		webhook: webhook, format: format, baseURL: strings.TrimRight(baseURL, "/"), store: store,
		client: &http.Client{Timeout: 10 * time.Second}, since: start, recent: make(map[string]time.Time),
	}
}

func (n *Notifier) Format() Format { return n.format }

// Host names the webhook's host without revealing its secret path.
func (n *Notifier) Host() string {
	parsed, err := url.Parse(n.webhook)
	if err != nil {
		return ""
	}
	return parsed.Host
}

func (n *Notifier) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := n.Check(ctx, now); err != nil && ctx.Err() == nil {
				log.Printf("notifications: %v", err)
			}
		}
	}
}

type event struct {
	Kind  string // new_group, reopened_group, summary or test
	Group investigation.FailureGroup
	URL   string
	More  int
}

// Check sends notifications for groups first received, or reopened, since the
// previous check.
func (n *Notifier) Check(ctx context.Context, now time.Time) error {
	window := n.since.Add(-overlap)
	var events []event
	var after *investigation.GroupCursor
	for range maxPages {
		page, err := n.store.FailureGroups(ctx, investigation.FailureFilter{Limit: investigation.MaxPageSize, GroupState: investigation.GroupAll}, after)
		if err != nil {
			return err
		}
		done := page.Next == nil
		for _, group := range page.Items {
			if group.LastObserved.Before(window) {
				done = true
				break
			}
			switch {
			case group.State == investigation.GroupOpen && !group.FirstObserved.Before(window):
				if _, sent := n.recent[group.Key]; !sent {
					events = append(events, event{Kind: "new_group", Group: group})
				}
			case group.State == investigation.GroupReopened && group.StateChanged != nil:
				notified, err := n.store.GroupNotified(ctx, group.Key)
				if err != nil {
					return err
				}
				if notified.Before(*group.StateChanged) {
					events = append(events, event{Kind: "reopened_group", Group: group})
				}
			}
		}
		if done {
			break
		}
		after = page.Next
	}

	var sendErr error
	for i, e := range events {
		if i == maxMessages {
			sendErr = errors.Join(sendErr, n.send(ctx, n.summary(len(events)-maxMessages)))
			break
		}
		e.URL = n.groupURL(e.Group.Key)
		sendErr = errors.Join(sendErr, n.send(ctx, e))
	}
	// Record every event, including summarized ones, so none repeats.
	for _, e := range events {
		if e.Kind == "new_group" {
			n.recent[e.Group.Key] = now
		} else if err := n.store.MarkGroupNotified(ctx, e.Group.Key, now); err != nil {
			sendErr = errors.Join(sendErr, err)
		}
	}
	for key, sent := range n.recent {
		if sent.Before(window) {
			delete(n.recent, key)
		}
	}
	n.since = now
	return sendErr
}

// Test sends a sample message to confirm the webhook works.
func (n *Notifier) Test(ctx context.Context) error {
	return n.send(ctx, event{Kind: "test", URL: n.baseURL})
}

func (n *Notifier) summary(more int) event {
	e := event{Kind: "summary", More: more}
	if n.baseURL != "" {
		e.URL = n.baseURL + "/failures?view=groups"
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
	default:
		return "More error group changes"
	}
}

// text describes the group without exception messages, which can contain
// secrets. Names come from instrumented services and are shortened.
func (e event) text() string {
	if e.Kind == "test" {
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
	return strings.Join(append(lines, detail), "\n")
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

func (n *Notifier) send(ctx context.Context, e event) error {
	text := e.text()
	var body []byte
	var err error
	contentType := "application/json"
	switch n.format {
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
		case "summary":
			payload["count"] = e.More
		}
		body, err = marshal(payload)
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "Geko")
	if n.format == Ntfy {
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
		return fmt.Errorf("send webhook to %s: %w", n.Host(), err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("send webhook to %s: %s", n.Host(), response.Status)
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
