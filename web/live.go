package web

import (
	"fmt"
	"net/http"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

type failureGroupView struct {
	investigation.FailureGroup
	TracesURL string
}

func (d *dashboard) failureEvents(w http.ResponseWriter, r *http.Request) {
	// Only the dashboard's own pages may hold one of the limited live streams.
	// Browsers send Sec-Fetch-Site; other clients such as curl omit it.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		http.Error(w, "Live updates are only available to Geko pages", http.StatusForbidden)
		return
	}
	select {
	case d.streams <- struct{}{}:
		defer func() { <-d.streams }()
	default:
		w.Header().Set("Retry-After", "5")
		http.Error(w, "Too many live views", http.StatusServiceUnavailable)
		return
	}
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, version uint64) bool {
		if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "retry: 3000\nevent: %s\ndata: %d\n\n", event, version); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	last := d.store.Revision()
	if !send("ready", last) {
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-d.settings.Shutdown:
			return
		case <-tick.C:
			version := d.store.Revision()
			if version != last {
				if !send("changed", version) {
					return
				}
				last = version
			}
		case <-heartbeat.C:
			if !send("heartbeat", last) {
				return
			}
		}
	}
}
