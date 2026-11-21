// Package demo generates a synthetic, multi-service dataset with recognisable
// failures, so Geko can be explored before any application is instrumented.
// Every name, host and message in it is invented.
package demo

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

// Writer stores spans as one export observed at a time.
type Writer interface {
	WriteSpans(ctx context.Context, observed time.Time, spans []telemetry.Span) error
}

// Store is what Seed needs to also leave groups in each triage state.
type Store interface {
	Writer
	FailureGroups(context.Context, investigation.FailureFilter, *investigation.GroupCursor) (investigation.FailureGroups, error)
	SetGroupState(ctx context.Context, key string, state investigation.GroupState, version, by string, now time.Time) error
	SetGroupNote(ctx context.Context, key, note, issueURL, by string, now time.Time) error
	SetTracePin(ctx context.Context, id telemetry.TraceID, pinned bool, by string, now time.Time) error
}

const (
	// History is how far back the seeded traces reach.
	History = 6 * time.Hour
	// Deployed is when web-frontend 5.13.0 replaced 5.12.0, before now.
	Deployed = 3 * time.Hour
	// Baseline and Candidate are the web-frontend versions to compare.
	Baseline, Candidate = "5.12.0", "5.13.0"
	seededTraces        = 2400
	exportSize          = 40
)

// Dataset generates further traces after Seed, so the dashboard keeps moving.
type Dataset struct {
	rng      *rand.Rand
	deployed time.Time
	// Traces, Services and Applications describe what Seed stored.
	Traces, Services, Applications int
}

// Seed stores History of traces ending at now, then resolves, mutes, notes
// and pins a few of the resulting groups as a team would have.
func Seed(ctx context.Context, store Store, now time.Time) (*Dataset, error) {
	d := &Dataset{rng: rand.New(rand.NewPCG(20261121, 4318)), deployed: now.Add(-Deployed), Services: 7, Applications: 2}
	start := now.Add(-History)
	step := History / seededTraces
	var batch []telemetry.Span
	for i := range seededTraces {
		end := start.Add(time.Duration(i+1) * step)
		batch = append(batch, d.Trace(end)...)
		if (i+1)%exportSize == 0 || i == seededTraces-1 {
			if err := store.WriteSpans(ctx, end, batch); err != nil {
				return nil, fmt.Errorf("store demo traces: %w", err)
			}
			batch = nil
		}
	}
	d.Traces = seededTraces
	return d, d.triage(ctx, store, now)
}

// Run stores one new trace every interval until ctx ends.
func (d *Dataset) Run(ctx context.Context, w Writer, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := w.WriteSpans(ctx, now, d.Trace(now)); err != nil && ctx.Err() == nil {
				return
			}
		}
	}
}

func (d *Dataset) triage(ctx context.Context, store Store, now time.Time) error {
	groups, err := store.FailureGroups(ctx, investigation.FailureFilter{Limit: investigation.MaxPageSize, GroupState: investigation.GroupAll}, nil)
	if err != nil {
		return err
	}
	for _, group := range groups.Items {
		var err error
		switch {
		case group.Service == payments.name && group.Classification == "exception":
			err = store.SetGroupNote(ctx, group.Key, "Timeouts rose after web-frontend "+Candidate+" moved checkout to the 3-D Secure flow. "+
				"Compare "+Baseline+" with "+Candidate+" on Version regressions.", "https://issues.example.com/SHOP-142", "", now.Add(-time.Hour))
			if err == nil {
				var id telemetry.TraceID
				if decoded, decodeErr := hex.DecodeString(group.TraceID); decodeErr == nil && len(decoded) == len(id) {
					copy(id[:], decoded)
					err = store.SetTracePin(ctx, id, true, "", now.Add(-50*time.Minute))
				}
			}
		case group.Service == reporting.name:
			// Resolved four hours ago, and the next nightly report failed again.
			err = store.SetGroupState(ctx, group.Key, investigation.GroupResolved, "", "", now.Add(-4*time.Hour))
		case group.Service == notifications.name:
			err = store.SetGroupState(ctx, group.Key, investigation.GroupMuted, "", "", now.Add(-2*time.Hour))
		}
		if err != nil {
			return fmt.Errorf("triage demo group: %w", err)
		}
	}
	return nil
}

type service struct{ name, namespace, version, language string }

var (
	frontendBefore = service{"web-frontend", "storefront", Baseline, "nodejs"}
	frontendAfter  = service{"web-frontend", "storefront", Candidate, "nodejs"}
	checkout       = service{"checkout", "storefront", "2.4.1", "go"}
	inventory      = service{"inventory", "storefront", "1.9.4", "go"}
	payments       = service{"payments", "storefront", "3.2.1", "java"}
	catalog        = service{"catalog", "storefront", "2.0.7", "python"}
	reporting      = service{"reporting", "back-office", "1.8.2", "java"}
	notifications  = service{"notifications", "back-office", "0.14.0", "go"}
)

type trace struct {
	d     *Dataset
	id    telemetry.TraceID
	start time.Time
	spans []telemetry.Span
}

// span appends a span from offset to offset+duration after the trace start
// and returns its index. parent is -1 for the root.
func (t *trace) span(parent int, s service, kind telemetry.SpanKind, name string, offset, duration time.Duration, attributes telemetry.Attributes) int {
	var id telemetry.SpanID
	for id == (telemetry.SpanID{}) {
		value := t.d.rng.Uint64()
		for i := range id {
			id[i] = byte(value >> (8 * i))
		}
	}
	span := telemetry.Span{
		TraceID: t.id, ID: id, ServiceName: s.name, Name: name, Kind: kind,
		StartTime: t.start.Add(offset), EndTime: t.start.Add(offset + duration),
		ResourceAttributes: telemetry.Attributes{
			"service.name": s.name, "service.namespace": s.namespace, "service.version": s.version,
			"deployment.environment.name": "production", "telemetry.sdk.language": s.language,
		},
		Scope:      telemetry.Scope{Name: "io.opentelemetry." + s.language},
		Attributes: attributes,
	}
	if parent >= 0 {
		span.ParentID = t.spans[parent].ID
	}
	t.spans = append(t.spans, span)
	return len(t.spans) - 1
}

func (t *trace) fail(index int, status int64, description string) {
	span := &t.spans[index]
	span.StatusCode, span.StatusDescription = telemetry.StatusError, description
	if status != 0 {
		span.Attributes["http.response.status_code"] = status
	}
}

func (t *trace) raise(index int, kind, message, stack string) {
	span := &t.spans[index]
	t.fail(index, 0, message)
	span.Events = append(span.Events, telemetry.Event{Name: "exception", Time: span.EndTime, Attributes: telemetry.Attributes{
		"exception.type": kind, "exception.message": message, "exception.stacktrace": stack,
	}})
}

// jitter returns base scaled by a factor between 0.7 and 1.3.
func (d *Dataset) jitter(base time.Duration) time.Duration {
	return time.Duration(float64(base) * (0.7 + 0.6*d.rng.Float64()))
}

func server(method, route string, status int64) telemetry.Attributes {
	return telemetry.Attributes{"http.request.method": method, "http.route": route, "http.response.status_code": status}
}

func client(method, url string, status int64) telemetry.Attributes {
	return telemetry.Attributes{"http.request.method": method, "url.full": url, "http.response.status_code": status}
}

// Trace returns one request that ended at end.
func (d *Dataset) Trace(end time.Time) []telemetry.Span {
	// Spans are laid out from end, then moved back by the trace's duration.
	t := &trace{d: d, start: end}
	first, second := d.rng.Uint64(), d.rng.Uint64()
	for i := range 8 {
		t.id[i], t.id[8+i] = byte(first>>(8*i)), byte(second>>(8*i))
	}
	frontend := frontendBefore
	if !end.Before(d.deployed) {
		frontend = frontendAfter
	}
	var duration time.Duration
	switch pick := d.rng.Float64(); {
	case pick < 0.40:
		duration = t.products(frontend)
	case pick < 0.65:
		duration = t.checkout(frontend)
	case pick < 0.80:
		duration = t.order(frontend)
	case pick < 0.88:
		duration = t.report()
	default:
		duration = t.notification()
	}
	shift := -duration
	for i := range t.spans {
		t.spans[i].StartTime = t.spans[i].StartTime.Add(shift)
		t.spans[i].EndTime = t.spans[i].EndTime.Add(shift)
		for j := range t.spans[i].Events {
			t.spans[i].Events[j].Time = t.spans[i].Events[j].Time.Add(shift)
		}
	}
	return slices.Clip(t.spans)
}

func (t *trace) products(frontend service) time.Duration {
	d := t.d
	query := d.jitter(18 * time.Millisecond)
	if frontend == frontendAfter { // the new release also loads recommendations
		query = d.jitter(90 * time.Millisecond)
	}
	failed := d.rng.Float64() < 0.01
	if failed {
		query = time.Second
	}
	total := query + d.jitter(22*time.Millisecond)
	root := t.span(-1, frontend, telemetry.KindServer, "GET /products", 0, total, server("GET", "/products", 200))
	call := t.span(root, frontend, telemetry.KindClient, "GET", 3*time.Millisecond, total-6*time.Millisecond, client("GET", "http://catalog:8080/api/products?category=shoes", 200))
	handler := t.span(call, catalog, telemetry.KindServer, "GET /api/products", 4*time.Millisecond, total-8*time.Millisecond, server("GET", "/api/products", 200))
	db := t.span(handler, catalog, telemetry.KindClient, "SELECT shop.products", 6*time.Millisecond, query, telemetry.Attributes{
		"db.system.name": "postgresql", "db.namespace": "shop", "db.operation.name": "SELECT",
		"db.query.text": "SELECT id, name, price FROM products WHERE category = $1 ORDER BY rank LIMIT $2",
	})
	if failed {
		t.raise(db, "psycopg.errors.QueryCanceled", "canceling statement due to statement timeout",
			"Traceback (most recent call last):\n  File \"catalog/products.py\", line 41, in list_products\n    rows = await cursor.execute(QUERY, (category, limit))\npsycopg.errors.QueryCanceled: canceling statement due to statement timeout")
		t.fail(handler, 500, "")
		t.fail(call, 500, "")
		t.fail(root, 503, "catalog unavailable")
	}
	return total
}

func (t *trace) checkout(frontend service) time.Duration {
	d := t.d
	rate, path := 0.02, "/v1/charges"
	if frontend == frontendAfter {
		rate, path = 0.20, "/v1/3ds/authenticate"
	}
	timedOut := d.rng.Float64() < rate
	outOfStock := !timedOut && d.rng.Float64() < 0.04
	deadlock := !timedOut && !outOfStock && d.rng.Float64() < 0.012
	reserve, charge := d.jitter(24*time.Millisecond), d.jitter(140*time.Millisecond)
	if timedOut {
		charge = 2*time.Second + d.jitter(15*time.Millisecond)
	}
	if outOfStock || deadlock {
		charge = 0
	}
	total := reserve + charge + d.jitter(30*time.Millisecond)
	root := t.span(-1, frontend, telemetry.KindServer, "POST /checkout", 0, total, server("POST", "/checkout", 200))
	call := t.span(root, frontend, telemetry.KindClient, "POST", 2*time.Millisecond, total-4*time.Millisecond, client("POST", "http://checkout:8080/api/checkout", 200))
	handler := t.span(call, checkout, telemetry.KindServer, "POST /api/checkout", 3*time.Millisecond, total-6*time.Millisecond, server("POST", "/api/checkout", 200))
	reserveCall := t.span(handler, checkout, telemetry.KindClient, "POST", 5*time.Millisecond, reserve, client("POST", "http://inventory:8080/api/reservations", 201))
	reservation := t.span(reserveCall, inventory, telemetry.KindServer, "POST /api/reservations", 6*time.Millisecond, reserve-2*time.Millisecond, server("POST", "/api/reservations", 201))
	stock := t.span(reservation, inventory, telemetry.KindClient, "UPDATE shop.stock", 8*time.Millisecond, reserve-6*time.Millisecond, telemetry.Attributes{
		"db.system.name": "postgresql", "db.namespace": "shop", "db.operation.name": "UPDATE",
		"db.query.text": "UPDATE stock SET reserved = reserved + $1 WHERE sku = $2 AND available - reserved >= $1",
	})
	switch {
	case outOfStock:
		for _, span := range []int{reservation, reserveCall, handler, call, root} {
			t.spans[span].Attributes["http.response.status_code"] = int64(409)
		}
		return total
	case deadlock:
		t.raise(stock, "*pgconn.PgError", "ERROR: deadlock detected (SQLSTATE 40P01)",
			"goroutine 912 [running]:\ninventory/internal/stock.(*Store).Reserve(...)\n\t/src/internal/stock/store.go:118\ninventory/internal/api.(*Handler).reserve(...)\n\t/src/internal/api/reservations.go:57")
		t.fail(reservation, 500, "")
		t.fail(reserveCall, 500, "")
		t.fail(handler, 502, "inventory reservation failed")
		t.fail(call, 502, "")
		t.fail(root, 502, "checkout failed")
		return total
	}
	payCall := t.span(handler, checkout, telemetry.KindClient, "POST", reserve+8*time.Millisecond, charge, client("POST", "http://payments:8080/api/charges", 201))
	payment := t.span(payCall, payments, telemetry.KindServer, "POST /api/charges", reserve+9*time.Millisecond, charge-2*time.Millisecond, server("POST", "/api/charges", 201))
	gateway := t.span(payment, payments, telemetry.KindClient, "POST", reserve+12*time.Millisecond, charge-8*time.Millisecond,
		client("POST", "https://gateway.example.net"+path, 200))
	if timedOut {
		delete(t.spans[gateway].Attributes, "http.response.status_code")
		t.raise(gateway, "java.net.http.HttpTimeoutException", "request timed out after 2000 ms: POST https://gateway.example.net"+path,
			"java.net.http.HttpTimeoutException: request timed out\n\tat java.net.http/jdk.internal.net.http.HttpClientImpl.send(HttpClientImpl.java:574)\n\tat com.example.payments.gateway.GatewayClient.authorize(GatewayClient.java:88)\n\tat com.example.payments.ChargeService.charge(ChargeService.java:52)")
		t.fail(payment, 504, "")
		t.fail(payCall, 504, "")
		t.fail(handler, 502, "payment failed")
		t.fail(call, 502, "")
		t.fail(root, 502, "checkout failed")
	}
	return total
}

func (t *trace) order(frontend service) time.Duration {
	d := t.d
	total := d.jitter(26 * time.Millisecond)
	root := t.span(-1, frontend, telemetry.KindServer, "GET /orders/{id}", 0, total, server("GET", "/orders/{id}", 200))
	call := t.span(root, frontend, telemetry.KindClient, "GET", 2*time.Millisecond, total-4*time.Millisecond, client("GET", "http://checkout:8080/api/orders/8841", 200))
	handler := t.span(call, checkout, telemetry.KindServer, "GET /api/orders/{id}", 3*time.Millisecond, total-6*time.Millisecond, server("GET", "/api/orders/{id}", 200))
	t.span(handler, checkout, telemetry.KindClient, "SELECT shop.orders", 4*time.Millisecond, total-10*time.Millisecond, telemetry.Attributes{
		"db.system.name": "postgresql", "db.namespace": "shop", "db.operation.name": "SELECT",
		"db.query.text": "SELECT * FROM orders WHERE id = $1",
	})
	return total
}

func (t *trace) report() time.Duration {
	d := t.d
	failed := d.rng.Float64() < 0.08
	total := d.jitter(420 * time.Millisecond)
	root := t.span(-1, reporting, telemetry.KindServer, "POST /reports/daily", 0, total, server("POST", "/reports/daily", 200))
	t.span(root, reporting, telemetry.KindClient, "SELECT reporting.orders", 5*time.Millisecond, total/2, telemetry.Attributes{
		"db.system.name": "postgresql", "db.namespace": "reporting", "db.operation.name": "SELECT",
		"db.query.text": "SELECT o.id, o.total, c.region FROM orders o LEFT JOIN customers c ON c.id = o.customer_id WHERE o.day = $1",
	})
	render := t.span(root, reporting, telemetry.KindInternal, "render daily report", total/2+10*time.Millisecond, total/2-20*time.Millisecond, telemetry.Attributes{
		"code.function.name": "com.example.reports.DailyReport.render", "code.file.path": "src/main/java/com/example/reports/DailyReport.java", "code.line.number": int64(87),
	})
	if failed {
		t.raise(render, "java.lang.NullPointerException", "Cannot invoke \"Customer.getRegion()\" because \"order.customer\" is null",
			"java.lang.NullPointerException: Cannot invoke \"Customer.getRegion()\" because \"order.customer\" is null\n\tat com.example.reports.DailyReport.render(DailyReport.java:87)\n\tat com.example.reports.ReportController.daily(ReportController.java:31)")
		t.fail(root, 500, "")
	}
	return total
}

func (t *trace) notification() time.Duration {
	d := t.d
	failed := d.rng.Float64() < 0.05
	total := d.jitter(160 * time.Millisecond)
	root := t.span(-1, notifications, telemetry.KindConsumer, "orders.created process", 0, total, telemetry.Attributes{
		"messaging.system": "kafka", "messaging.destination.name": "orders.created", "messaging.operation.type": "process",
	})
	send := t.span(root, notifications, telemetry.KindClient, "POST", 6*time.Millisecond, total-12*time.Millisecond, client("POST", "https://mail.example.net/v3/send", 202))
	if failed {
		t.fail(send, 503, "mail provider unavailable")
		t.fail(root, 0, "notification not sent")
	}
	return total
}
