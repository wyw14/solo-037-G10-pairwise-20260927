package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/service"
)

type EnvironmentService interface {
	RecordEnvironmentSample(service.EnvironmentSampleInput) (model.EnvironmentSample, error)
	EnvironmentSamples(section string, at time.Time, includeSuperseded bool) []model.EnvironmentSample
	EnvironmentSampleHistory(model.EventID) ([]model.EnvironmentSample, error)
}

func (r *Router) recordEnvironmentSample(w http.ResponseWriter, request *http.Request) {
	var input service.EnvironmentSampleInput
	if err := decodeMeasurementBody(request, &input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	value, err := r.dependencies.RecordEnvironmentSample(input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, value)
}

func (r *Router) environmentSamples(w http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	var at time.Time
	if raw := query.Get("at"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			http.Error(w, "at must be RFC3339", http.StatusBadRequest)
			return
		}
		at = parsed
	}
	includeSuperseded := query.Get("all") == "true"
	writeJSON(w, r.dependencies.EnvironmentSamples(query.Get("section"), at, includeSuperseded))
}

func (r *Router) environmentSampleHistory(w http.ResponseWriter, request *http.Request) {
	value, err := r.dependencies.EnvironmentSampleHistory(model.EventID(chi.URLParam(request, "id")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, value)
}
