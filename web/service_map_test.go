package web

import (
	"reflect"
	"strings"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestServiceMapObservedRelationships(t *testing.T) {
	spans := []investigation.Span{
		{SpanSummary: investigation.SpanSummary{SpanID: "1", Service: "frontend"}},
		{SpanSummary: investigation.SpanSummary{SpanID: "2", Service: "frontend"}, ParentID: "1"},
		{SpanSummary: investigation.SpanSummary{SpanID: "3", Service: "payments"}, ParentID: "2", StatusCode: telemetry.StatusError},
		{SpanSummary: investigation.SpanSummary{SpanID: "4", Service: "payments"}, ParentID: "2"},
		{SpanSummary: investigation.SpanSummary{SpanID: "5", Service: "orphan"}, ParentID: "missing"},
	}
	view := serviceMap(spans, &spans[2], "/traces/demo", false)
	if len(view.Nodes) != 3 || len(view.Edges) != 1 || view.MissingParents != 1 {
		t.Fatalf("incorrect topology: %+v", view)
	}
	if !strings.Contains(view.Edges[0].Label, "2 observed parent-child links") {
		t.Fatal(view.Edges)
	}
	for _, node := range view.Nodes {
		if node.Name == "payments" && (node.Count != 2 || node.Errors != 1 || !node.Selected || !strings.Contains(node.URL, "span=3")) {
			t.Fatalf("incorrect evidence target: %+v", node)
		}
	}
}

func TestServiceMapCyclesAndLongNames(t *testing.T) {
	spans := []investigation.Span{
		{SpanSummary: investigation.SpanSummary{SpanID: "1", Service: strings.Repeat("長", 40)}, ParentID: "3"},
		{SpanSummary: investigation.SpanSummary{SpanID: "2", Service: "b"}, ParentID: "1"},
		{SpanSummary: investigation.SpanSummary{SpanID: "3", Service: "c"}, ParentID: "2"},
	}
	a := serviceMap(spans, nil, "/traces/demo", false)
	b := serviceMap([]investigation.Span{spans[2], spans[0], spans[1]}, nil, "/traces/demo", false)
	if len(a.Nodes) != 3 || len(a.Edges) != 3 || !reflect.DeepEqual(a, b) {
		t.Fatalf("layout must be deterministic and retain cycles: %+v / %+v", a, b)
	}
	for _, n := range a.Nodes {
		if len([]rune(n.Label)) > 24 {
			t.Fatal("unbounded label")
		}
	}
}

func TestServiceMapSeparatesEnvironmentsAndAggregatesInstances(t *testing.T) {
	spans := []investigation.Span{
		{SpanSummary: investigation.SpanSummary{SpanID: "1", Service: "api", Application: "Storefront"},
			ResourceAttributes: telemetry.Attributes{"deployment.environment.name": "prod", "service.instance.id": "pod-a"}},
		{SpanSummary: investigation.SpanSummary{SpanID: "2", Service: "api", Application: "Storefront"},
			ResourceAttributes: telemetry.Attributes{"deployment.environment": "prod", "service.instance.id": "pod-b"}},
		{SpanSummary: investigation.SpanSummary{SpanID: "3", Service: "api", Application: "Storefront"},
			ResourceAttributes: telemetry.Attributes{"deployment.environment.name": "staging", "service.instance.id": "pod-c"}},
	}
	separate := serviceMap(spans, &spans[0], "/traces/demo", false)
	if len(separate.Nodes) != 2 || !reflect.DeepEqual(separate.SelectedInstances, []string{"pod-a", "pod-b"}) {
		t.Fatalf("separate environments: %+v", separate)
	}
	merged := serviceMap(spans, &spans[0], "/traces/demo", true)
	if len(merged.Nodes) != 1 || !reflect.DeepEqual(merged.SelectedInstances, []string{"pod-a", "pod-b", "pod-c"}) ||
		!strings.Contains(merged.Nodes[0].URL, "environments=merged") {
		t.Fatalf("merged environments: %+v", merged)
	}
}
