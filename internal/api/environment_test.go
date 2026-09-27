package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/service"
)

func TestEnvironmentHTTPFlowKeepsHistory(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	base := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	end := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	calibrated := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano)
	body := `{"source_id":"ctd-1","sections":["section-a"],"valid_from":"` + base + `","valid_to":"` + end + `","temperature_c":18.5,"salinity_psu":32,"calibrated_at":"` + calibrated + `"}`
	created := httptest.NewRecorder()
	server.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/environment-readings", strings.NewReader(body)))
	if created.Code != http.StatusOK {
		t.Fatalf("record: %d %s", created.Code, created.Body)
	}
	var createdResponse struct {
		Data service.EnvironmentReading `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdResponse); err != nil {
		t.Fatal(err)
	}
	series := createdResponse.Data.Series
	if series == "" || createdResponse.Data.Version != 1 {
		t.Fatalf("unexpected reading: %+v", createdResponse.Data)
	}
	updated := httptest.NewRecorder()
	server.ServeHTTP(updated, httptest.NewRequest(http.MethodPost, "/api/environment-readings/"+string(series)+"/versions", strings.NewReader(strings.Replace(body, "18.5", "19.1", 1))))
	if updated.Code != http.StatusOK {
		t.Fatalf("update: %d %s", updated.Code, updated.Body)
	}
	history := httptest.NewRecorder()
	server.ServeHTTP(history, httptest.NewRequest(http.MethodGet, "/api/environment-readings/"+string(series)+"/history", nil))
	if history.Code != http.StatusOK {
		t.Fatalf("history: %d %s", history.Code, history.Body)
	}
	var historyResponse struct {
		Data []service.EnvironmentReading `json:"data"`
	}
	if err := json.Unmarshal(history.Body.Bytes(), &historyResponse); err != nil {
		t.Fatal(err)
	}
	if len(historyResponse.Data) != 2 || historyResponse.Data[0].TemperatureC != 18.5 || historyResponse.Data[1].TemperatureC != 19.1 {
		t.Fatalf("history must keep original and updated values: %+v", historyResponse.Data)
	}
	current := httptest.NewRecorder()
	server.ServeHTTP(current, httptest.NewRequest(http.MethodGet, "/api/environment-readings", nil))
	if current.Code != http.StatusOK {
		t.Fatalf("current: %d %s", current.Code, current.Body)
	}
	var currentResponse struct {
		Data []service.EnvironmentReading `json:"data"`
	}
	if err := json.Unmarshal(current.Body.Bytes(), &currentResponse); err != nil {
		t.Fatal(err)
	}
	if len(currentResponse.Data) != 1 || currentResponse.Data[0].TemperatureC != 19.1 || currentResponse.Data[0].Version != 2 {
		t.Fatalf("current view should hold only the latest version: %+v", currentResponse.Data)
	}
	missing := httptest.NewRecorder()
	server.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/environment-readings/00000000-0000-0000-0000-000000000000/history", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown series should be 404: %d", missing.Code)
	}
}

func TestEnvironmentAnalysisIncludesConditions(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	base := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	end := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	calibrated := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano)
	body := `{"source_id":"ctd-1","sections":["section-a"],"valid_from":"` + base + `","valid_to":"` + end + `","temperature_c":18.5,"salinity_psu":32,"calibrated_at":"` + calibrated + `"}`
	created := httptest.NewRecorder()
	server.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/environment-readings", strings.NewReader(body)))
	if created.Code != http.StatusOK {
		t.Fatalf("record: %d %s", created.Code, created.Body)
	}
	run := httptest.NewRecorder()
	server.ServeHTTP(run, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(`{"name":"tow A","model_name":"hull A","section":"section-a","carriage":"carriage-1"}`)))
	if run.Code != http.StatusOK {
		t.Fatalf("create run: %d %s", run.Code, run.Body)
	}
	var runResponse struct {
		Data service.MeasuredRun `json:"data"`
	}
	if err := json.Unmarshal(run.Body.Bytes(), &runResponse); err != nil {
		t.Fatal(err)
	}
	analysis := httptest.NewRecorder()
	server.ServeHTTP(analysis, httptest.NewRequest(http.MethodGet, "/api/measured-runs/"+string(runResponse.Data.Operation.ID)+"/analysis", nil))
	if analysis.Code != http.StatusOK {
		t.Fatalf("analysis: %d %s", analysis.Code, analysis.Body)
	}
	text := analysis.Body.String()
	if !strings.Contains(text, `"environment"`) || !strings.Contains(text, `"correction"`) || !strings.Contains(text, `"temperature_c":18.5`) {
		t.Fatalf("analysis should carry environment conditions and correction basis: %s", text)
	}
	if strings.Contains(text, "no_reading") {
		t.Fatalf("covered window should not report gaps: %s", text)
	}
}
