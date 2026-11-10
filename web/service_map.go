package web

import (
	"fmt"
	"sort"

	"geko/internal/investigation"
)

const mapSpanLimit = 500

type serviceMapView struct {
	Nodes                                []serviceNode
	Edges                                []serviceEdge
	Width, Height, Spans, MissingParents int
	Truncated                            bool
}

type serviceNode struct {
	Name, Label, URL    string
	X, Y, Count, Errors int
	Selected            bool
}

type serviceEdge struct {
	Path, Label string
}

// serviceMap uses only observed parent-child pairs. Same-service spans are
// counted within their node, not misrepresented as remote calls.
func serviceMap(spans []investigation.Span, selected *investigation.Span, path string) serviceMapView {
	view := serviceMapView{Spans: len(spans)}
	byID := make(map[string]investigation.Span, len(spans))
	nodes := make(map[string]*serviceNode)
	targetHasError := make(map[string]bool)
	for _, span := range spans {
		byID[span.SpanID] = span
		node := nodes[span.Service]
		if node == nil {
			node = &serviceNode{Name: span.Service, Label: span.Service}
			if node.Label == "" {
				node.Label = "Unknown service"
			}
			if label := []rune(node.Label); len(label) > 24 {
				node.Label = string(label[:21]) + "…"
			}
			nodes[span.Service] = node
		}
		node.Count++
		hasError := signal(span) != ""
		if hasError {
			node.Errors++
		}
		if node.URL == "" || (hasError && !targetHasError[span.Service]) {
			node.URL = spanLink(path, span.SpanID, nil)
			targetHasError[span.Service] = hasError
		}
		node.Selected = selected != nil && selected.Service == span.Service
	}
	type pair struct{ from, to string }
	pairs := make(map[pair]int)
	incoming := make(map[string]int)
	children := make(map[string][]string)
	for _, span := range spans {
		if span.ParentID == "" {
			continue
		}
		parent, ok := byID[span.ParentID]
		if !ok {
			view.MissingParents++
			continue
		}
		if parent.Service == span.Service {
			continue
		}
		edge := pair{parent.Service, span.Service}
		if pairs[edge] == 0 {
			incoming[span.Service]++
			children[parent.Service] = append(children[parent.Service], span.Service)
		}
		pairs[edge]++
	}
	names := make([]string, 0, len(nodes))
	for name := range nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	for name := range children {
		sort.Strings(children[name])
	}
	// Breadth-first layout is bounded even for cyclic or disconnected telemetry.
	levels := make(map[string]int)
	queue := []string{}
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
		view.Nodes = append(view.Nodes, *node)
	}
	edges := make([]pair, 0, len(pairs))
	for edge := range pairs {
		edges = append(edges, edge)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].from != edges[j].from {
			return edges[i].from < edges[j].from
		}
		return edges[i].to < edges[j].to
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
		view.Edges = append(view.Edges, serviceEdge{Path: route, Label: fmt.Sprintf("%s → %s: %d observed parent-child links", edge.from, edge.to, pairs[edge])})
	}
	return view
}
