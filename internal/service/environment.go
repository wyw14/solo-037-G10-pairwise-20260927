package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wyw14/solo-037/internal/model"
)

// ErrEnvironmentSeriesNotFound is returned when an update or history lookup
// names an environment series that was never recorded.
var ErrEnvironmentSeriesNotFound = errors.New("environment series not found")

// EnvironmentInput carries one field sampling of the water column. Sections
// and the valid window mark where and when the values apply.
type EnvironmentInput struct {
	SourceID     string    `json:"source_id"`
	Sections     []string  `json:"sections"`
	ValidFrom    time.Time `json:"valid_from"`
	ValidTo      time.Time `json:"valid_to"`
	TemperatureC float64   `json:"temperature_c"`
	SalinityPSU  float64   `json:"salinity_psu"`
	CalibratedAt time.Time `json:"calibrated_at"`
	RecordedAt   time.Time `json:"recorded_at"`
}

// EnvironmentReading is one immutable version of an environment record.
// Updates append a new reading to the same series and supersede the previous
// head, so historical values stay queryable through History.
type EnvironmentReading struct {
	ID           model.EventID `json:"id"`
	Series       model.EventID `json:"series"`
	Version      int           `json:"version"`
	Supersedes   model.EventID `json:"supersedes,omitempty"`
	SourceID     string        `json:"source_id"`
	Sections     []string      `json:"sections"`
	ValidFrom    time.Time     `json:"valid_from"`
	ValidTo      time.Time     `json:"valid_to"`
	TemperatureC float64       `json:"temperature_c"`
	SalinityPSU  float64       `json:"salinity_psu"`
	CalibratedAt time.Time     `json:"calibrated_at"`
	RecordedAt   time.Time     `json:"recorded_at"`
	Durable      bool          `json:"durable"`
}

// AppliesTo reports whether the reading is marked valid for the section.
func (r EnvironmentReading) AppliesTo(section string) bool {
	for _, name := range r.Sections {
		if name == section {
			return true
		}
	}
	return false
}

// EnvironmentGap marks a sub-interval of the queried window that no current
// reading covers.
type EnvironmentGap struct {
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Reason string    `json:"reason"`
}

// CorrectionBasis identifies the reading an analysis should correct against,
// with the sampled values and the sensor calibration time for traceability.
type CorrectionBasis struct {
	ReadingID    model.EventID `json:"reading_id"`
	Series       model.EventID `json:"series"`
	Version      int           `json:"version"`
	SourceID     string        `json:"source_id"`
	TemperatureC float64       `json:"temperature_c"`
	SalinityPSU  float64       `json:"salinity_psu"`
	CalibratedAt time.Time     `json:"calibrated_at"`
}

// EnvironmentConditions is the environment view for one section and window:
// applicable current readings, uncovered gaps, and the correction basis.
type EnvironmentConditions struct {
	Section    string               `json:"section"`
	From       time.Time            `json:"from"`
	To         time.Time            `json:"to"`
	Readings   []EnvironmentReading `json:"readings"`
	Gaps       []EnvironmentGap     `json:"gaps"`
	Correction *CorrectionBasis     `json:"correction,omitempty"`
}

type EnvironmentArchive struct {
	mu       sync.Mutex
	path     string
	readings map[model.EventID]EnvironmentReading
}

func OpenEnvironmentArchive(directory string) (*EnvironmentArchive, error) {
	a := &EnvironmentArchive{readings: make(map[model.EventID]EnvironmentReading)}
	if directory == "" {
		return a, nil
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, err
	}
	a.path = filepath.Join(directory, "water-environment.json")
	data, err := os.ReadFile(a.path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &a.readings); err != nil {
		return nil, fmt.Errorf("read environment archive: %w", err)
	}
	if a.readings == nil {
		return nil, errors.New("environment archive must contain a reading map")
	}
	return a, nil
}

func cloneEnvironmentReading(value EnvironmentReading) EnvironmentReading {
	value.Sections = append([]string(nil), value.Sections...)
	return value
}

func (a *EnvironmentArchive) commit(next map[model.EventID]EnvironmentReading) error {
	if a.path != "" {
		data, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(a.path), "environment-*.tmp")
		if err != nil {
			return err
		}
		name := file.Name()
		defer os.Remove(name)
		if _, err = file.Write(data); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(name, a.path); err != nil {
			return err
		}
	}
	a.readings = next
	return nil
}

func normalizeEnvironmentInput(input EnvironmentInput, now time.Time) (EnvironmentInput, error) {
	if strings.TrimSpace(input.SourceID) == "" {
		return input, errors.New("source is required")
	}
	if len(input.Sections) == 0 {
		return input, errors.New("at least one section is required")
	}
	sections := make([]string, 0, len(input.Sections))
	seen := make(map[string]struct{}, len(input.Sections))
	for _, name := range input.Sections {
		name = strings.TrimSpace(name)
		if name == "" {
			return input, errors.New("section names must not be empty")
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		sections = append(sections, name)
	}
	sort.Strings(sections)
	input.Sections = sections
	if !input.ValidFrom.Before(input.ValidTo) {
		return input, errors.New("valid_from must be before valid_to")
	}
	if math.IsNaN(input.TemperatureC) || math.IsInf(input.TemperatureC, 0) || math.IsNaN(input.SalinityPSU) || math.IsInf(input.SalinityPSU, 0) || input.SalinityPSU < 0 {
		return input, errors.New("finite temperature and non-negative salinity are required")
	}
	if input.CalibratedAt.IsZero() || input.CalibratedAt.After(now.Add(time.Minute)) {
		return input, errors.New("valid calibration time is required")
	}
	if input.RecordedAt.IsZero() {
		input.RecordedAt = now
	}
	if input.RecordedAt.After(now.Add(time.Minute)) {
		return input, errors.New("recorded_at cannot be in the future")
	}
	input.ValidFrom = input.ValidFrom.UTC()
	input.ValidTo = input.ValidTo.UTC()
	input.CalibratedAt = input.CalibratedAt.UTC()
	input.RecordedAt = input.RecordedAt.UTC()
	return input, nil
}

// Record starts a new environment series from a field sampling.
func (a *EnvironmentArchive) Record(input EnvironmentInput) (EnvironmentReading, error) {
	normalized, err := normalizeEnvironmentInput(input, time.Now().UTC())
	if err != nil {
		return EnvironmentReading{}, err
	}
	reading := EnvironmentReading{
		ID:           model.NewEventID(),
		Series:       model.NewEventID(),
		Version:      1,
		SourceID:     normalized.SourceID,
		Sections:     normalized.Sections,
		ValidFrom:    normalized.ValidFrom,
		ValidTo:      normalized.ValidTo,
		TemperatureC: normalized.TemperatureC,
		SalinityPSU:  normalized.SalinityPSU,
		CalibratedAt: normalized.CalibratedAt,
		RecordedAt:   normalized.RecordedAt,
		Durable:      a.path != "",
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	next := make(map[model.EventID]EnvironmentReading, len(a.readings)+1)
	for id, value := range a.readings {
		next[id] = value
	}
	next[reading.ID] = reading
	if err := a.commit(next); err != nil {
		return EnvironmentReading{}, err
	}
	return cloneEnvironmentReading(reading), nil
}

// Update appends a corrected sampling to an existing series. The previous
// versions are kept untouched and stay queryable through History.
func (a *EnvironmentArchive) Update(series model.EventID, input EnvironmentInput) (EnvironmentReading, error) {
	normalized, err := normalizeEnvironmentInput(input, time.Now().UTC())
	if err != nil {
		return EnvironmentReading{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	head, exists := a.headLocked(series)
	if !exists {
		return EnvironmentReading{}, ErrEnvironmentSeriesNotFound
	}
	reading := EnvironmentReading{
		ID:           model.NewEventID(),
		Series:       series,
		Version:      head.Version + 1,
		Supersedes:   head.ID,
		SourceID:     normalized.SourceID,
		Sections:     normalized.Sections,
		ValidFrom:    normalized.ValidFrom,
		ValidTo:      normalized.ValidTo,
		TemperatureC: normalized.TemperatureC,
		SalinityPSU:  normalized.SalinityPSU,
		CalibratedAt: normalized.CalibratedAt,
		RecordedAt:   normalized.RecordedAt,
		Durable:      a.path != "",
	}
	next := make(map[model.EventID]EnvironmentReading, len(a.readings)+1)
	for id, value := range a.readings {
		next[id] = value
	}
	next[reading.ID] = reading
	if err := a.commit(next); err != nil {
		return EnvironmentReading{}, err
	}
	return cloneEnvironmentReading(reading), nil
}

func (a *EnvironmentArchive) headLocked(series model.EventID) (EnvironmentReading, bool) {
	var head EnvironmentReading
	found := false
	for _, reading := range a.readings {
		if reading.Series == series && (!found || reading.Version > head.Version) {
			head = reading
			found = true
		}
	}
	return head, found
}

// Current lists the latest version of every series, ordered by sampling time.
func (a *EnvironmentArchive) Current() []EnvironmentReading {
	a.mu.Lock()
	defer a.mu.Unlock()
	heads := make(map[model.EventID]EnvironmentReading)
	for _, reading := range a.readings {
		if head, ok := heads[reading.Series]; !ok || reading.Version > head.Version {
			heads[reading.Series] = reading
		}
	}
	result := make([]EnvironmentReading, 0, len(heads))
	for _, head := range heads {
		result = append(result, cloneEnvironmentReading(head))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].RecordedAt.Equal(result[j].RecordedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].RecordedAt.Before(result[j].RecordedAt)
	})
	return result
}

// History returns every version of a series, oldest first, so superseded
// values remain inspectable after updates.
func (a *EnvironmentArchive) History(series model.EventID) ([]EnvironmentReading, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]EnvironmentReading, 0)
	for _, reading := range a.readings {
		if reading.Series == series {
			result = append(result, cloneEnvironmentReading(reading))
		}
	}
	if len(result) == 0 {
		return nil, false
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Version < result[j].Version })
	return result, true
}

// Conditions resolves the environment view for a section over [from, to]:
// the current readings that apply, the uncovered gaps, and the reading to
// use as correction basis (largest overlap, newest sampling wins ties).
func (a *EnvironmentArchive) Conditions(section string, from, to time.Time) EnvironmentConditions {
	from = from.UTC()
	to = to.UTC()
	if to.Before(from) {
		to = from
	}
	result := EnvironmentConditions{
		Section:  section,
		From:     from,
		To:       to,
		Readings: []EnvironmentReading{},
		Gaps:     []EnvironmentGap{},
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	heads := make(map[model.EventID]EnvironmentReading)
	for _, reading := range a.readings {
		if head, ok := heads[reading.Series]; !ok || reading.Version > head.Version {
			heads[reading.Series] = reading
		}
	}
	matches := make([]EnvironmentReading, 0)
	for _, head := range heads {
		if !head.AppliesTo(section) || head.ValidFrom.After(to) || head.ValidTo.Before(from) {
			continue
		}
		matches = append(matches, cloneEnvironmentReading(head))
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].ValidFrom.Equal(matches[j].ValidFrom) {
			return matches[i].RecordedAt.Before(matches[j].RecordedAt)
		}
		return matches[i].ValidFrom.Before(matches[j].ValidFrom)
	})
	result.Readings = matches
	if len(matches) == 0 {
		result.Gaps = append(result.Gaps, EnvironmentGap{From: from, To: to, Reason: "no_reading"})
		return result
	}
	cursor := from
	for _, match := range matches {
		start := match.ValidFrom
		if start.Before(from) {
			start = from
		}
		end := match.ValidTo
		if end.After(to) {
			end = to
		}
		if start.After(cursor) {
			result.Gaps = append(result.Gaps, EnvironmentGap{From: cursor, To: start, Reason: "no_reading"})
		}
		if end.After(cursor) {
			cursor = end
		}
	}
	if cursor.Before(to) {
		result.Gaps = append(result.Gaps, EnvironmentGap{From: cursor, To: to, Reason: "no_reading"})
	}
	best := -1
	var bestOverlap time.Duration
	for index, match := range matches {
		start := match.ValidFrom
		if start.Before(from) {
			start = from
		}
		end := match.ValidTo
		if end.After(to) {
			end = to
		}
		overlap := end.Sub(start)
		if overlap < 0 {
			overlap = 0
		}
		if best == -1 || overlap > bestOverlap || (overlap == bestOverlap && match.RecordedAt.After(matches[best].RecordedAt)) {
			best = index
			bestOverlap = overlap
		}
	}
	if best >= 0 {
		match := matches[best]
		result.Correction = &CorrectionBasis{
			ReadingID:    match.ID,
			Series:       match.Series,
			Version:      match.Version,
			SourceID:     match.SourceID,
			TemperatureC: match.TemperatureC,
			SalinityPSU:  match.SalinityPSU,
			CalibratedAt: match.CalibratedAt,
		}
	}
	return result
}

// environmentWindow derives the time span a measured run needs environment
// coverage for: the evidence span, or the creation instant when no evidence
// was recorded yet.
func environmentWindow(run MeasuredRun) (time.Time, time.Time) {
	from := run.Operation.CreatedAt
	to := from
	for _, evidence := range run.Evidence {
		if evidence.RecordedAt.Before(from) {
			from = evidence.RecordedAt
		}
		if evidence.RecordedAt.After(to) {
			to = evidence.RecordedAt
		}
	}
	return from, to
}

func (r *Runtime) RecordEnvironment(input EnvironmentInput) (EnvironmentReading, error) {
	return r.Environment.Record(input)
}

func (r *Runtime) UpdateEnvironment(series model.EventID, input EnvironmentInput) (EnvironmentReading, error) {
	return r.Environment.Update(series, input)
}

func (r *Runtime) EnvironmentReadings() []EnvironmentReading { return r.Environment.Current() }

func (r *Runtime) EnvironmentHistory(series model.EventID) ([]EnvironmentReading, bool) {
	return r.Environment.History(series)
}
