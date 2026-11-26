package web

import (
	"net/url"
	"testing"
)

func TestLocalReturnURL(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"/failures?application=group%3Ashop&range=1h", "/failures?application=group%3Ashop&range=1h"},
		{"/traces?service=api&return=%2Foverview", "/traces?service=api"},
		{"/overview", "/overview"},
		{"https://example.com/failures", "/failures"},
		{"//example.com/failures", "/failures"},
		{"/settings", "/failures"},
		{"/failures%2f..%2fsettings", "/failures"},
		{"/failures?x=%zz", "/failures"},
	} {
		if got := localReturnURL(tc.input); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestReaderModeAndReturnSurviveTraceNavigation(t *testing.T) {
	view := traceView{Parent: "/traces/ab?span=parent#span-details", First: "/traces/ab", Next: "/traces/ab?cursor=next", EvidenceFirst: "/traces/ab#evidence", EvidenceNext: "/traces/ab?evidence_after=next#evidence", Rows: []waterfallRow{{URL: "/traces/ab?span=child#span-details"}}, ServiceMap: serviceMapView{Nodes: []serviceNode{{URL: "/traces/ab?span=node#span-details"}}, EnvironmentToggle: "/traces/ab?environments=merged#service-map"}}
	back := "/failures?application=group%3Ashop&range=1h"
	view.preserveReturn(back)
	view.preserveReading()
	for _, href := range []string{view.Parent, view.First, view.Next, view.EvidenceFirst, view.EvidenceNext, view.Rows[0].URL, view.ServiceMap.Nodes[0].URL, view.ServiceMap.EnvironmentToggle} {
		u, err := url.Parse(href)
		if err != nil || u.Query().Get("reading") != "wide" || u.Query().Get("return") != back {
			t.Errorf("navigation lost context: %s (%v)", href, err)
		}
	}
}
