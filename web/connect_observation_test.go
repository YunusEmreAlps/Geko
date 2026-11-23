package web

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
)

func TestConnectionCheckScopeAndWindow(t *testing.T) {
	now := time.Unix(1800000000, 0)
	since := now.Add(-10 * time.Second)
	d := dashboard{settings: Settings{ReceiverConnections: func() otlp.ConnectionSnapshot {
		return otlp.ConnectionSnapshot{Services: []otlp.ServiceObservation{
			{Service: "api", Namespace: "store", At: now},
			{Service: "api", Namespace: "other", At: now},
			{Service: "worker", Namespace: "store", At: now},
			{Service: "api", Namespace: "store", At: since.Add(-time.Second)},
		}}
	}}}
	r := httptest.NewRequest("GET", "http://localhost/connect?service=api&application=store&since=1799999990000", nil)
	got, err := d.connectionCheck(r, connectView{Service: "api", Application: "store", Protocol: "http/protobuf"}, now)
	if err != nil || !got.Started || got.Expired || len(got.Samples) != 1 || got.Samples[0].Namespace != "store" {
		t.Fatalf("check: %+v, %v", got, err)
	}
	got, err = d.connectionCheck(r, connectView{Service: "api", Bound: "store"}, now.Add(time.Minute))
	if err != nil || !got.Expired {
		t.Fatalf("expiration: %+v, %v", got, err)
	}
	for _, raw := range []string{"bad", "1800000001000", "1799999000000"} {
		r := httptest.NewRequest("GET", "http://localhost/connect?since="+raw, nil)
		if _, err := d.connectionCheck(r, connectView{}, now); err == nil {
			t.Fatalf("accepted invalid start %q", raw)
		}
	}
	r = httptest.NewRequest("GET", "http://localhost/connect?observe=start", nil)
	got, err = d.connectionCheck(r, connectView{Service: "new"}, now)
	if err != nil || len(got.Samples) != 0 || !got.Started || got.Until != now.Add(time.Minute) {
		t.Fatalf("new check: %+v, %v", got, err)
	}
}
