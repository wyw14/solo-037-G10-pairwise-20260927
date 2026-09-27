package service

import (
	"math"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/model"
)

func environmentInput(temperature float64, from, to time.Time) EnvironmentSampleInput {
	return EnvironmentSampleInput{
		Sections:     []string{"section-a", "section-b"},
		SourceID:     "ctd-1",
		TemperatureC: temperature,
		SalinityPPT:  32,
		CalibratedAt: from.Add(-time.Hour),
		SampledAt:    from,
		ValidFrom:    from,
		ValidTo:      to,
	}
}

func TestEnvironmentSamplesPersistAndKeepHistory(t *testing.T) {
	directory := t.TempDir()
	a, err := OpenEnvironmentArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Now().Add(-2 * time.Hour).UTC()
	to := time.Now().Add(time.Hour).UTC()
	original, err := a.Record(environmentInput(20.5, from, to))
	if err != nil {
		t.Fatal(err)
	}
	correction := environmentInput(21.1, from, to)
	correction.Supersedes = original.ID
	updated, err := a.Record(correction)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Supersedes != original.ID {
		t.Fatalf("correction not linked: %+v", updated)
	}
	current := a.List("section-a", time.Time{}, false)
	if len(current) != 1 || current[0].TemperatureC != 21.1 {
		t.Fatalf("effective list should hold only the correction: %+v", current)
	}
	loaded, err := OpenEnvironmentArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	history, err := loaded.History(updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].TemperatureC != 20.5 || history[1].TemperatureC != 21.1 {
		t.Fatalf("original value lost after reload: %+v", history)
	}
	if history[0].SupersededBy != updated.ID || !history[1].Effective() || !history[1].Durable {
		t.Fatalf("version chain broken: %+v", history)
	}
	if all := loaded.List("", time.Time{}, true); len(all) != 2 {
		t.Fatalf("superseded original not queryable: %+v", all)
	}
	if at := loaded.List("section-a", from.Add(time.Minute), false); len(at) != 1 {
		t.Fatalf("time filter failed: %+v", at)
	}
	if other := loaded.List("section-c", time.Time{}, false); len(other) != 0 {
		t.Fatalf("section filter failed: %+v", other)
	}
}

func TestEnvironmentSampleRejectsInvalidAndStaleSupersede(t *testing.T) {
	a, err := OpenEnvironmentArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	from := time.Now().Add(-time.Hour).UTC()
	to := time.Now().Add(time.Hour).UTC()
	base := environmentInput(20, from, to)

	bad := base
	bad.Sections = nil
	if _, err := a.Record(bad); err == nil {
		t.Fatal("missing sections accepted")
	}
	bad = base
	bad.TemperatureC = math.NaN()
	if _, err := a.Record(bad); err == nil {
		t.Fatal("NaN temperature accepted")
	}
	bad = base
	bad.SalinityPPT = -1
	if _, err := a.Record(bad); err == nil {
		t.Fatal("negative salinity accepted")
	}
	bad = base
	bad.ValidFrom, bad.ValidTo = to, from
	if _, err := a.Record(bad); err == nil {
		t.Fatal("inverted window accepted")
	}
	bad = base
	bad.Supersedes = "missing"
	if _, err := a.Record(bad); err == nil {
		t.Fatal("unknown supersede accepted")
	}

	original, err := a.Record(base)
	if err != nil {
		t.Fatal(err)
	}
	correction := base
	correction.Supersedes = original.ID
	if _, err := a.Record(correction); err != nil {
		t.Fatal(err)
	}
	stale := base
	stale.Supersedes = original.ID
	if _, err := a.Record(stale); err == nil {
		t.Fatal("superseding an already replaced sample accepted")
	}
	if got := len(a.List("", time.Time{}, true)); got != 2 {
		t.Fatalf("rejected records changed archive: %d", got)
	}
}

func TestEnvironmentCoverageFindsConditionsGapsAndBasis(t *testing.T) {
	a, err := OpenEnvironmentArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-3 * time.Hour).UTC()
	first := environmentInput(20, start, start.Add(time.Hour))
	if _, err := a.Record(first); err != nil {
		t.Fatal(err)
	}
	second := environmentInput(21, start.Add(90*time.Minute), start.Add(2*time.Hour))
	second.Sections = []string{"section-a"}
	if _, err := a.Record(second); err != nil {
		t.Fatal(err)
	}
	otherSection := environmentInput(19, start, start.Add(2*time.Hour))
	otherSection.Sections = []string{"section-c"}
	if _, err := a.Record(otherSection); err != nil {
		t.Fatal(err)
	}

	coverage := a.Coverage("section-a", start, start.Add(2*time.Hour))
	if len(coverage.Matched) != 2 || coverage.Covered {
		t.Fatalf("expected two matches and incomplete coverage: %+v", coverage)
	}
	if len(coverage.Gaps) != 1 || !coverage.Gaps[0].From.Equal(start.Add(time.Hour)) || !coverage.Gaps[0].To.Equal(start.Add(90*time.Minute)) {
		t.Fatalf("gap not detected: %+v", coverage.Gaps)
	}
	if len(coverage.CorrectionBasis) != 2 {
		t.Fatalf("correction basis missing: %+v", coverage.CorrectionBasis)
	}
	basis := coverage.CorrectionBasis[0]
	if basis.TemperatureC != 20 || !basis.CoversFrom.Equal(start) || !basis.CoversTo.Equal(start.Add(time.Hour)) || basis.CalibratedAt.IsZero() {
		t.Fatalf("basis does not identify the observation: %+v", basis)
	}

	full := a.Coverage("section-a", start, start.Add(time.Hour))
	if !full.Covered || len(full.Gaps) != 0 {
		t.Fatalf("fully covered window reported gaps: %+v", full)
	}
	empty := a.Coverage("section-a", time.Time{}, time.Time{})
	if empty.Covered || len(empty.Matched) != 0 || len(empty.Gaps) != 0 {
		t.Fatalf("empty window should match nothing: %+v", empty)
	}
}

func TestRuntimeAnalysisIncludesEnvironment(t *testing.T) {
	runtime, err := OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.CreateMeasuredRun("tow", "hull", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-30 * time.Minute).UTC()
	for _, value := range []float64{0.2, 0.3} {
		if _, err := runtime.RecordMeasurement(run.Operation.ID, MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: value, RecordedAt: at}); err != nil {
			t.Fatal(err)
		}
		at = at.Add(2 * time.Minute)
	}
	analysis, err := runtime.AnalyzeMeasuredRun(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Environment.Covered || len(analysis.Environment.Gaps) != 1 {
		t.Fatalf("uncovered run should show a gap: %+v", analysis.Environment)
	}
	if _, err := runtime.RecordEnvironmentSample(environmentInput(20, at.Add(-5*time.Minute), at.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	analysis, err = runtime.AnalyzeMeasuredRun(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.Environment.Covered || len(analysis.Environment.Matched) != 1 || len(analysis.Environment.CorrectionBasis) != 1 {
		t.Fatalf("analysis did not find the environment conditions: %+v", analysis.Environment)
	}
}
