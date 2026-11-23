package api

import (
	"context"
	"net/http"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

type RunReader interface {
	RunSnapshot(context.Context, string) (investigation.RunSnapshot, error)
	CompareRuns(context.Context, string, string) (investigation.RunComparison, error)
}

func (api *queryAPI) registerRuns(mux *http.ServeMux) {
	reader, ok := api.store.(RunReader)
	if !ok {
		return
	}
	mux.HandleFunc("GET /api/v1/runs/compare", api.local(func(w http.ResponseWriter, r *http.Request) {
		baseline, candidate := r.URL.Query().Get("baseline"), r.URL.Query().Get("candidate")
		if !investigation.ValidRunID(baseline) || !investigation.ValidRunID(candidate) || baseline == candidate {
			fail(w, http.StatusBadRequest, "choose two different valid run IDs")
			return
		}
		result, err := reader.CompareRuns(r.Context(), baseline, candidate)
		if err != nil {
			queryError(w, r.Context(), err)
			return
		}
		respond(w, http.StatusOK, result)
	}))
	mux.HandleFunc("GET /api/v1/runs/{runID}", api.local(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("runID")
		if !investigation.ValidRunID(id) {
			fail(w, http.StatusBadRequest, "invalid run ID")
			return
		}
		result, err := reader.RunSnapshot(r.Context(), id)
		if err != nil {
			queryError(w, r.Context(), err)
			return
		}
		respond(w, http.StatusOK, result)
	}))
}
