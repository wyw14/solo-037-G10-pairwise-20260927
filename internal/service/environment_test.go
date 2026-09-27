package service

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/model"
)

func environmentTestInput(base time.Time) EnvironmentInput {
	return EnvironmentInput{
		SourceID:     "ctd-1",
		Sections:     []string{"section-a"},
		ValidFrom:    base,
		ValidTo:      base.Add(time.Hour),
		TemperatureC: 18.5,
		SalinityPSU:  32,
		CalibratedAt: base.Add(-24 * time.Hour),
		RecordedAt:   base,
	}
}

func TestEnvironmentUpdateKeepsHistoryAcrossReload(t *testing.T) {
	directory := t.TempDir()
	a, err := OpenEnvironmentArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-2 * time.Hour).UTC()
	first, err := a.Record(environmentTestInput(base))
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 || first.Series == "" || !first.Durable {
		t.Fatalf("unexpected first reading: %+v", first)
	}
	corrected := environmentTestInput(base)
	corrected.TemperatureC = 19.1
	second, err := a.Update(first.Series, corrected)
	if err != nil {
		t.Fatal(err)
	}
	if second.Series != first.Series || second.Version != 2 || second.Supersedes != first.ID {
		t.Fatalf("update did not extend the series: %+v", second)
	}
	reloaded, err := OpenEnvironmentArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	history, ok := reloaded.History(first.Series)
	if !ok || len(history) != 2 {
		t.Fatalf("history lost after reload: %+v", history)
	}
	if history[0].TemperatureC != 18.5 || history[1].TemperatureC != 19.1 {
		t.Fatalf("original values not preserved: %+v", history)
	}
	current := reloaded.Current()
	if len(current) != 1 || current[0].TemperatureC != 19.1 || current[0].Version != 2 {
		t.Fatalf("current view should hold only the latest version: %+v", current)
	}
}

func TestEnvironmentConditionsCoverageGapsAndCorrection(t *testing.T) {
	a, err := OpenEnvironmentArchive("")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-3 * time.Hour).UTC()
	if _, err := a.Record(environmentTestInput(base)); err != nil {
		t.Fatal(err)
	}
	conditions := a.Conditions("section-a", base.Add(30*time.Minute), base.Add(2*time.Hour))
	if len(conditions.Readings) != 1 {
		t.Fatalf("expected the covering reading: %+v", conditions.Readings)
	}
	if len(conditions.Gaps) != 1 || !conditions.Gaps[0].From.Equal(base.Add(time.Hour)) || !conditions.Gaps[0].To.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("uncovered tail not reported as gap: %+v", conditions.Gaps)
	}
	if conditions.Correction == nil || conditions.Correction.TemperatureC != 18.5 || conditions.Correction.SalinityPSU != 32 || !conditions.Correction.CalibratedAt.Equal(base.Add(-24*time.Hour)) {
		t.Fatalf("correction basis missing or wrong: %+v", conditions.Correction)
	}
	other := a.Conditions("section-b", base, base.Add(time.Hour))
	if len(other.Readings) != 0 || len(other.Gaps) != 1 || other.Correction != nil {
		t.Fatalf("section without readings must be fully gapped: %+v", other)
	}
	if !other.Gaps[0].From.Equal(base) || !other.Gaps[0].To.Equal(base.Add(time.Hour)) {
		t.Fatalf("gap should span the whole window: %+v", other.Gaps)
	}
}

func TestEnvironmentRejectsInvalidInput(t *testing.T) {
	a, err := OpenEnvironmentArchive("")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour).UTC()
	valid := environmentTestInput(base)
	cases := map[string]EnvironmentInput{
		"empty source":      func() EnvironmentInput { in := valid; in.SourceID = " "; return in }(),
		"no sections":       func() EnvironmentInput { in := valid; in.Sections = nil; return in }(),
		"blank section":     func() EnvironmentInput { in := valid; in.Sections = []string{" "}; return in }(),
		"inverted window":   func() EnvironmentInput { in := valid; in.ValidFrom, in.ValidTo = in.ValidTo, in.ValidFrom; return in }(),
		"NaN temperature":   func() EnvironmentInput { in := valid; in.TemperatureC = math.NaN(); return in }(),
		"negative salinity": func() EnvironmentInput { in := valid; in.SalinityPSU = -1; return in }(),
		"zero calibration":  func() EnvironmentInput { in := valid; in.CalibratedAt = time.Time{}; return in }(),
		"future calibration": func() EnvironmentInput {
			in := valid
			in.CalibratedAt = time.Now().Add(time.Hour)
			return in
		}(),
		"future record": func() EnvironmentInput {
			in := valid
			in.RecordedAt = time.Now().Add(time.Hour)
			return in
		}(),
	}
	for name, input := range cases {
		if _, err := a.Record(input); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if len(a.Current()) != 0 {
		t.Fatal("rejected inputs changed the archive")
	}
	if _, err := a.Update(model.NewEventID(), valid); !errors.Is(err, ErrEnvironmentSeriesNotFound) {
		t.Fatalf("unknown series update should fail with ErrEnvironmentSeriesNotFound, got %v", err)
	}
}

func TestAnalyzeMeasuredRunAttachesEnvironment(t *testing.T) {
	runtime, err := OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	covered := environmentTestInput(now.Add(-time.Hour))
	covered.ValidTo = now.Add(time.Hour)
	if _, err := runtime.RecordEnvironment(covered); err != nil {
		t.Fatal(err)
	}
	run, err := runtime.CreateMeasuredRun("tow A", "hull A", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordMeasurement(run.Operation.ID, MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 0.2, RecordedAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	analysis, err := runtime.AnalyzeMeasuredRun(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Environment == nil || len(analysis.Environment.Gaps) != 0 || analysis.Environment.Correction == nil {
		t.Fatalf("covered run should resolve a correction basis without gaps: %+v", analysis.Environment)
	}
	if analysis.Environment.Correction.TemperatureC != 18.5 {
		t.Fatalf("correction basis should carry the sampled temperature: %+v", analysis.Environment.Correction)
	}
	exposed, err := runtime.CreateMeasuredRun("tow B", "hull A", "section-b", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	analysis, err = runtime.AnalyzeMeasuredRun(exposed.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Environment == nil || len(analysis.Environment.Gaps) == 0 || analysis.Environment.Correction != nil {
		t.Fatalf("uncovered section should report gaps and no correction basis: %+v", analysis.Environment)
	}
}
