package web

import (
	"context"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

type comparisonSuggestions struct {
	investigation.ComparisonChoices
	Unavailable                        bool
	TraceLimit, SpanLimit, ChoiceLimit int
}

func (d *dashboard) comparisonSuggestions(ctx context.Context, since time.Time, service string) comparisonSuggestions {
	view := comparisonSuggestions{TraceLimit: investigation.ChoiceTraceLimit, SpanLimit: investigation.ChoiceSpanLimit, ChoiceLimit: investigation.ChoiceLimit}
	reader, ok := d.store.(interface {
		ComparisonChoices(context.Context, time.Time, string) (investigation.ComparisonChoices, error)
	})
	if !ok {
		view.Unavailable = true
		return view
	}
	var err error
	view.ComparisonChoices, err = reader.ComparisonChoices(ctx, since, service)
	view.Unavailable = err != nil
	if err != nil {
		view.ComparisonChoices = investigation.ComparisonChoices{}
	}
	return view
}
