package web

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"geko/internal/investigation"
)

const mapSpanLimit = 500

type serviceMapView struct {
	Nodes                                []serviceNode
	Edges                                []serviceEdge
	Width, Height, Spans, MissingParents int
	Truncated                            bool
	MergeEnvironments                    bool
	EnvironmentToggle                    string
	SelectedInstances                    []string
}

type serviceNode struct {
	Name, Label, URL    string
	Environment         string
	Instances           []string
	X, Y, Count, Errors int
	Selected            bool
}

type serviceEdge struct {
	Path, Label string
}

type serviceKey struct {
	application, namespace, service, environment string
}

func mapService(span investigation.Span, mergeEnvironments bool) serviceKey {
	namespace, _ := span.ResourceAttributes["service.namespace"].(string)
	key := serviceKey{application: span.Application, namespace: strings.TrimSpace(namespace), service: span.Service}
	if !mergeEnvironments {
		environment, _ := span.ResourceAttributes["deployment.environment.name"].(string)
		if strings.TrimSpace(environment) == "" {
			environment, _ = span.ResourceAttributes["deployment.environment"].(string)
		}
		key.environment = strings.TrimSpace(environment)
	}
	return key
}

func compareServices(a, b serviceKey) int {
	if order := cmp.Compare(a.application, b.application); order != 0 {
		return order
	}
	if order := cmp.Compare(a.namespace, b.namespace); order != 0 {
		return order
	}
	if order := cmp.Compare(a.service, b.service); order != 0 {
		return order
	}
	return cmp.Compare(a.environment, b.environment)
}

// serviceMap uses only observed parent-child pairs. Same-service spans are
// counted within their node, not misrepresented as remote calls.
func serviceMap(spans []investigation.Span, selected *investigation.Span, path string, mergeEnvironments bool) serviceMapView {
	view := serviceMapView{Spans: len(spans), MergeEnvironments: mergeEnvironments}
	keys := make(map[string]serviceKey, len(spans))
	nodes := make(map[serviceKey]*serviceNode)
	targetHasError := make(map[serviceKey]bool)
	var selectedKey serviceKey
	if selected != nil {
		selectedKey = mapService(*selected, mergeEnvironments)
	}
	for _, span := range spans {
		key := mapService(span, mergeEnvironments)
		keys[span.SpanID] = key
		node := nodes[key]
		if node == nil {
			label := serviceLabel(span.Application, span.Service)
			name := label
			if key.environment != "" {
				name += " · " + key.environment
			}
			environment := []rune(key.environment)
			if len(environment) > 14 {
				environment = append(environment[:13], '…')
			}
			node = &serviceNode{Name: name, Label: label, Environment: string(environment)}
			if node.Label == "" {
				node.Label = "Unknown service"
			}
			if label := []rune(node.Label); len(label) > 24 {
				node.Label = string(label[:21]) + "…"
			}
			nodes[key] = node
		}
		node.Count++
		if instance, _ := span.ResourceAttributes["service.instance.id"].(string); instance != "" && instance != "[redacted]" && !slices.Contains(node.Instances, instance) {
			node.Instances = append(node.Instances, instance)
		}
		hasError := signal(span) != ""
		if hasError {
			node.Errors++
		}
		if node.URL == "" || (hasError && !targetHasError[key]) {
			node.URL = spanLink(path, span.SpanID, nil, mergeEnvironments)
			targetHasError[key] = hasError
		}
		node.Selected = selected != nil && selectedKey == key
	}
	type pair struct{ from, to serviceKey }
	pairs := make(map[pair]int)
	incoming := make(map[serviceKey]int)
	children := make(map[serviceKey][]serviceKey)
	for _, span := range spans {
		if span.ParentID == "" {
			continue
		}
		from, ok := keys[span.ParentID]
		if !ok {
			view.MissingParents++
			continue
		}
		to := keys[span.SpanID]
		if from == to {
			continue
		}
		edge := pair{from, to}
		if pairs[edge] == 0 {
			incoming[to]++
			children[from] = append(children[from], to)
		}
		pairs[edge]++
	}
	names := make([]serviceKey, 0, len(nodes))
	for name := range nodes {
		names = append(names, name)
	}
	slices.SortFunc(names, compareServices)
	for name := range children {
		slices.SortFunc(children[name], compareServices)
	}
	// Breadth-first layout is bounded even for cyclic or disconnected telemetry.
	levels := make(map[serviceKey]int)
	var queue []serviceKey
	for _, name := range names {
		if incoming[name] == 0 {
			levels[name] = 0
			queue = append(queue, name)
		}
	}
	walk := func() {
		for len(queue) > 0 {
			name := queue[0]
			queue = queue[1:]
			for _, child := range children[name] {
				if _, seen := levels[child]; !seen {
					levels[child] = levels[name] + 1
					queue = append(queue, child)
				}
			}
		}
	}
	walk()
	for _, name := range names {
		if _, seen := levels[name]; !seen {
			levels[name] = 0
			queue = append(queue, name)
			walk()
		}
	}
	rows := make(map[int]int)
	view.Width, view.Height = 640, 360
	for _, name := range names {
		node := nodes[name]
		level := levels[name]
		node.X, node.Y = 48+level*280, 64+rows[level]*140
		rows[level]++
		view.Width = max(view.Width, node.X+252)
		view.Height = max(view.Height, node.Y+140)
		slices.Sort(node.Instances)
		if node.Selected {
			view.SelectedInstances = node.Instances
		}
		view.Nodes = append(view.Nodes, *node)
	}
	edges := make([]pair, 0, len(pairs))
	for edge := range pairs {
		edges = append(edges, edge)
	}
	slices.SortFunc(edges, func(a, b pair) int {
		if order := compareServices(a.from, b.from); order != 0 {
			return order
		}
		return compareServices(a.to, b.to)
	})
	for _, edge := range edges {
		from, to := nodes[edge.from], nodes[edge.to]
		x1, y1, x2, y2 := from.X+204, from.Y+40, to.X, to.Y+40
		var route string
		if x2 > x1 {
			mid := (x1 + x2) / 2
			route = fmt.Sprintf("M%d %d H%d V%d H%d", x1, y1, mid, y2, x2)
		} else {
			// Route cycles above the nodes rather than through their labels.
			route = fmt.Sprintf("M%d %d H%d V%d H%d V%d H%d", x1, y1, x1+18, min(from.Y, to.Y)-24, x2-18, y2, x2)
		}
		view.Edges = append(view.Edges, serviceEdge{Path: route, Label: fmt.Sprintf("%s → %s: %d observed parent-child links", from.Name, to.Name, pairs[edge])})
	}
	return view
}
