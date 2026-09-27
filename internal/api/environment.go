package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/service"
)

type EnvironmentService interface {
	RecordEnvironment(service.EnvironmentInput) (service.EnvironmentReading, error)
	UpdateEnvironment(model.EventID, service.EnvironmentInput) (service.EnvironmentReading, error)
	EnvironmentReadings() []service.EnvironmentReading
	EnvironmentHistory(model.EventID) ([]service.EnvironmentReading, bool)
}

func (r *Router) environmentReadings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, r.dependencies.EnvironmentReadings())
}

func (r *Router) recordEnvironment(w http.ResponseWriter, request *http.Request) {
	var input service.EnvironmentInput
	if err := decodeMeasurementBody(request, &input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	value, err := r.dependencies.RecordEnvironment(input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, value)
}

func (r *Router) updateEnvironment(w http.ResponseWriter, request *http.Request) {
	var input service.EnvironmentInput
	if err := decodeMeasurementBody(request, &input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	value, err := r.dependencies.UpdateEnvironment(model.EventID(chi.URLParam(request, "series")), input)
	if errors.Is(err, service.ErrEnvironmentSeriesNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, value)
}

func (r *Router) environmentHistory(w http.ResponseWriter, request *http.Request) {
	value, ok := r.dependencies.EnvironmentHistory(model.EventID(chi.URLParam(request, "series")))
	if !ok {
		http.Error(w, "environment series not found", http.StatusNotFound)
		return
	}
	writeJSON(w, value)
}
