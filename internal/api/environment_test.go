package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/service"
)

func postEnvironmentSample(t *testing.T, server http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/environment-samples", strings.NewReader(body)))
	return response
}

func environmentBody(temperature float64, from, to time.Time) string {
	return fmt.Sprintf(`{"sections":["section-a"],"source_id":"ctd-1","temperature_c":%v,"salinity_ppt":32,"calibrated_at":%q,"sampled_at":%q,"valid_from":%q,"valid_to":%q}`,
		temperature,
		from.Add(-time.Hour).Format(time.RFC3339Nano),
		from.Format(time.RFC3339Nano),
		from.Format(time.RFC3339Nano),
		to.Format(time.RFC3339Nano))
}

func TestEnvironmentSamplesHTTPRecordListAndHistory(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	from := time.Now().Add(-2 * time.Hour).UTC()
	to := time.Now().Add(time.Hour).UTC()

	created := postEnvironmentSample(t, server, environmentBody(20.5, from, to))
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var sample struct {
		Data model.EnvironmentSample `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &sample); err != nil {
		t.Fatal(err)
	}
	if sample.Data.ID == "" || !sample.Data.Durable {
		t.Fatalf("sample not recorded durably: %+v", sample.Data)
	}

	invalid := postEnvironmentSample(t, server, `{"sections":["section-a"],"source_id":"ctd-1","temperature_c":20,"salinity_ppt":-3,"calibrated_at":"2026-01-01T00:00:00Z","sampled_at":"2026-01-01T00:00:00Z","valid_from":"2026-01-01T00:00:00Z","valid_to":"2026-01-02T00:00:00Z"}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("negative salinity accepted: %d", invalid.Code)
	}

	correction := postEnvironmentSample(t, server, strings.Replace(environmentBody(21.1, from, to), `"valid_from"`, `"supersedes":"`+string(sample.Data.ID)+`","valid_from"`, 1))
	if correction.Code != http.StatusOK {
		t.Fatalf("correction: %d %s", correction.Code, correction.Body)
	}

	listed := httptest.NewRecorder()
	server.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/environment-samples?section=section-a", nil))
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"temperature_c":21.1`) || strings.Contains(listed.Body.String(), `"temperature_c":20.5`) {
		t.Fatalf("effective list wrong: %d %s", listed.Code, listed.Body)
	}
	all := httptest.NewRecorder()
	server.ServeHTTP(all, httptest.NewRequest(http.MethodGet, "/api/environment-samples?all=true", nil))
	if !strings.Contains(all.Body.String(), `"temperature_c":20.5`) {
		t.Fatalf("superseded original missing: %s", all.Body)
	}
	badAt := httptest.NewRecorder()
	server.ServeHTTP(badAt, httptest.NewRequest(http.MethodGet, "/api/environment-samples?at=not-a-time", nil))
	if badAt.Code != http.StatusBadRequest {
		t.Fatalf("bad at parameter accepted: %d", badAt.Code)
	}

	history := httptest.NewRecorder()
	server.ServeHTTP(history, httptest.NewRequest(http.MethodGet, "/api/environment-samples/"+string(sample.Data.ID)+"/history", nil))
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), `"temperature_c":20.5`) || !strings.Contains(history.Body.String(), `"temperature_c":21.1`) {
		t.Fatalf("history lost the original value: %d %s", history.Code, history.Body)
	}
	missing := httptest.NewRecorder()
	server.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/environment-samples/nope/history", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown sample history: %d", missing.Code)
	}
}

func TestMeasuredRunAnalysisHTTPCarriesEnvironment(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	created := httptest.NewRecorder()
	server.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(`{"name":"tow B","model_name":"hull B","section":"section-a","carriage":"carriage-1"}`)))
	var run struct {
		Data service.MeasuredRun `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-30 * time.Minute).UTC()
	stamp := at.Format(time.RFC3339Nano)
	recorded := httptest.NewRecorder()
	server.ServeHTTP(recorded, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+string(run.Data.Operation.ID)+"/evidence", strings.NewReader(`{"kind":"wave_time","source_id":"probe-a","value":0.24,"recorded_at":"`+stamp+`"}`)))
	if recorded.Code != http.StatusOK {
		t.Fatalf("record: %d %s", recorded.Code, recorded.Body)
	}
	if created := postEnvironmentSample(t, server, environmentBody(20.5, at.Add(-time.Minute), at.Add(time.Minute))); created.Code != http.StatusOK {
		t.Fatalf("sample: %d %s", created.Code, created.Body)
	}
	analysis := httptest.NewRecorder()
	server.ServeHTTP(analysis, httptest.NewRequest(http.MethodGet, "/api/measured-runs/"+string(run.Data.Operation.ID)+"/analysis", nil))
	body := analysis.Body.String()
	if analysis.Code != http.StatusOK || !strings.Contains(body, `"environment"`) || !strings.Contains(body, `"covered":true`) || !strings.Contains(body, `"correction_basis"`) {
		t.Fatalf("analysis lacks environment coverage: %d %s", analysis.Code, body)
	}
}
