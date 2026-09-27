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

// EnvironmentSampleInput is one field observation of the tank water. The
// operator declares the sections and the validity window the observation
// applies to; Supersedes optionally names the current sample this record
// corrects.
type EnvironmentSampleInput struct {
	Sections     []string      `json:"sections"`
	SourceID     string        `json:"source_id"`
	TemperatureC float64       `json:"temperature_c"`
	SalinityPPT  float64       `json:"salinity_ppt"`
	CalibratedAt time.Time     `json:"calibrated_at"`
	SampledAt    time.Time     `json:"sampled_at"`
	ValidFrom    time.Time     `json:"valid_from"`
	ValidTo      time.Time     `json:"valid_to"`
	Supersedes   model.EventID `json:"supersedes"`
}

// EnvironmentGap is a sub-interval of a test window with no applicable
// environment observation.
type EnvironmentGap struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// CorrectionBasis identifies the observation (and its calibration and
// sampling times) that justifies correcting test data over CoversFrom..CoversTo.
type CorrectionBasis struct {
	SampleID     model.EventID `json:"sample_id"`
	SourceID     string        `json:"source_id"`
	CalibratedAt time.Time     `json:"calibrated_at"`
	SampledAt    time.Time     `json:"sampled_at"`
	TemperatureC float64       `json:"temperature_c"`
	SalinityPPT  float64       `json:"salinity_ppt"`
	CoversFrom   time.Time     `json:"covers_from"`
	CoversTo     time.Time     `json:"covers_to"`
}

// EnvironmentCoverage reports, for one section and one test window, the
// applicable environment observations, the uncovered gaps, and the basis for
// corrections. Only effective (non-superseded) samples take part.
type EnvironmentCoverage struct {
	Section         string                    `json:"section"`
	WindowFrom      time.Time                 `json:"window_from"`
	WindowTo        time.Time                 `json:"window_to"`
	Covered         bool                      `json:"covered"`
	Matched         []model.EnvironmentSample `json:"matched"`
	Gaps            []EnvironmentGap          `json:"gaps"`
	CorrectionBasis []CorrectionBasis         `json:"correction_basis"`
}

// EnvironmentArchive stores water environment observations. Like the
// measurement archive, updates are committed to disk before they are
// published in memory, and superseded samples are kept so historical values
// remain queryable.
type EnvironmentArchive struct {
	mu      sync.Mutex
	path    string
	samples []model.EnvironmentSample
}

func OpenEnvironmentArchive(directory string) (*EnvironmentArchive, error) {
	a := &EnvironmentArchive{samples: []model.EnvironmentSample{}}
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
	if err := json.Unmarshal(data, &a.samples); err != nil {
		return nil, fmt.Errorf("read environment archive: %w", err)
	}
	if a.samples == nil {
		return nil, errors.New("environment archive must contain a sample list")
	}
	return a, nil
}

func cloneEnvironmentSample(value model.EnvironmentSample) model.EnvironmentSample {
	value.Sections = append([]string(nil), value.Sections...)
	return value
}

func (a *EnvironmentArchive) commit(next []model.EnvironmentSample) error {
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
	a.samples = next
	return nil
}

func validateEnvironmentInput(input EnvironmentSampleInput) error {
	if len(input.Sections) == 0 {
		return errors.New("at least one section is required")
	}
	for _, section := range input.Sections {
		if strings.TrimSpace(section) == "" {
			return errors.New("sections must not be blank")
		}
	}
	if strings.TrimSpace(input.SourceID) == "" {
		return errors.New("source is required")
	}
	if math.IsNaN(input.TemperatureC) || math.IsInf(input.TemperatureC, 0) || math.IsNaN(input.SalinityPPT) || math.IsInf(input.SalinityPPT, 0) || input.SalinityPPT < 0 {
		return errors.New("finite temperature and non-negative salinity are required")
	}
	now := time.Now().Add(time.Minute)
	if input.CalibratedAt.IsZero() || input.CalibratedAt.After(now) || input.SampledAt.IsZero() || input.SampledAt.After(now) {
		return errors.New("valid calibration and sampling times are required")
	}
	if input.ValidFrom.IsZero() || input.ValidTo.IsZero() || !input.ValidFrom.Before(input.ValidTo) {
		return errors.New("validity window requires valid_from before valid_to")
	}
	return nil
}

// Record validates and appends one observation. When input.Supersedes names
// a sample, that sample must be the current version of its chain; it is
// marked superseded in the same atomic commit, keeping the original value.
func (a *EnvironmentArchive) Record(input EnvironmentSampleInput) (model.EnvironmentSample, error) {
	if err := validateEnvironmentInput(input); err != nil {
		return model.EnvironmentSample{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	replaced := -1
	if input.Supersedes != "" {
		for index, sample := range a.samples {
			if sample.ID == input.Supersedes {
				replaced = index
				break
			}
		}
		if replaced < 0 {
			return model.EnvironmentSample{}, errors.New("superseded sample not found")
		}
		if !a.samples[replaced].Effective() {
			return model.EnvironmentSample{}, errors.New("superseded sample is not the current version")
		}
	}
	sample := model.NewEnvironmentSample(input.Sections, input.SourceID, input.TemperatureC, input.SalinityPPT, input.CalibratedAt, input.SampledAt, input.ValidFrom, input.ValidTo, input.Supersedes, time.Now().UTC()).WithDurability(a.path != "")
	next := make([]model.EnvironmentSample, len(a.samples), len(a.samples)+1)
	copy(next, a.samples)
	if replaced >= 0 {
		next[replaced].SupersededBy = sample.ID
	}
	next = append(next, sample)
	if err := a.commit(next); err != nil {
		return model.EnvironmentSample{}, err
	}
	return sample, nil
}

// List returns observations in record order, by default only the current
// version of each chain. section and at optionally restrict to samples
// applying to a section and covering a moment; includeSuperseded adds the
// historical originals.
func (a *EnvironmentArchive) List(section string, at time.Time, includeSuperseded bool) []model.EnvironmentSample {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]model.EnvironmentSample, 0, len(a.samples))
	for _, sample := range a.samples {
		if !includeSuperseded && !sample.Effective() {
			continue
		}
		if section != "" && !sample.AppliesTo(section) {
			continue
		}
		if !at.IsZero() && !sample.Covers(at) {
			continue
		}
		result = append(result, cloneEnvironmentSample(sample))
	}
	return result
}

// History returns the full version chain containing id, oldest first, so the
// original values of corrected records stay queryable.
func (a *EnvironmentArchive) History(id model.EventID) ([]model.EnvironmentSample, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	index := make(map[model.EventID]int, len(a.samples))
	for position, sample := range a.samples {
		index[sample.ID] = position
	}
	root, exists := index[id]
	if !exists {
		return nil, errors.New("sample not found")
	}
	for a.samples[root].Supersedes != "" {
		previous, ok := index[a.samples[root].Supersedes]
		if !ok {
			break
		}
		root = previous
	}
	chain := []model.EnvironmentSample{}
	for current := root; ; {
		chain = append(chain, cloneEnvironmentSample(a.samples[current]))
		next, ok := index[a.samples[current].SupersededBy]
		if a.samples[current].SupersededBy == "" || !ok {
			return chain, nil
		}
		current = next
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Coverage finds the effective observations applying to section over
// [from, to], the uncovered gaps inside that window, and the correction
// basis. A zero or inverted window yields empty matches, gaps and basis.
func (a *EnvironmentArchive) Coverage(section string, from time.Time, to time.Time) EnvironmentCoverage {
	from, to = from.UTC(), to.UTC()
	result := EnvironmentCoverage{
		Section:         section,
		WindowFrom:      from,
		WindowTo:        to,
		Matched:         []model.EnvironmentSample{},
		Gaps:            []EnvironmentGap{},
		CorrectionBasis: []CorrectionBasis{},
	}
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return result
	}
	a.mu.Lock()
	for _, sample := range a.samples {
		if !sample.Effective() || !sample.AppliesTo(section) {
			continue
		}
		if sample.ValidTo.Before(from) || sample.ValidFrom.After(to) {
			continue
		}
		result.Matched = append(result.Matched, cloneEnvironmentSample(sample))
	}
	a.mu.Unlock()
	sort.SliceStable(result.Matched, func(i, j int) bool {
		left, right := result.Matched[i], result.Matched[j]
		if left.ValidFrom.Equal(right.ValidFrom) {
			return left.RecordedAt.Before(right.RecordedAt)
		}
		return left.ValidFrom.Before(right.ValidFrom)
	})
	cursor := from
	for _, sample := range result.Matched {
		start := maxTime(sample.ValidFrom, from)
		end := minTime(sample.ValidTo, to)
		if start.After(cursor) {
			result.Gaps = append(result.Gaps, EnvironmentGap{From: cursor, To: start})
		}
		if end.After(cursor) {
			cursor = end
		}
		result.CorrectionBasis = append(result.CorrectionBasis, CorrectionBasis{
			SampleID:     sample.ID,
			SourceID:     sample.SourceID,
			CalibratedAt: sample.CalibratedAt,
			SampledAt:    sample.SampledAt,
			TemperatureC: sample.TemperatureC,
			SalinityPPT:  sample.SalinityPPT,
			CoversFrom:   start,
			CoversTo:     end,
		})
	}
	if cursor.Before(to) {
		result.Gaps = append(result.Gaps, EnvironmentGap{From: cursor, To: to})
	}
	result.Covered = len(result.Matched) > 0 && len(result.Gaps) == 0
	return result
}

func (r *Runtime) RecordEnvironmentSample(input EnvironmentSampleInput) (model.EnvironmentSample, error) {
	return r.Environment.Record(input)
}

func (r *Runtime) EnvironmentSamples(section string, at time.Time, includeSuperseded bool) []model.EnvironmentSample {
	return r.Environment.List(section, at, includeSuperseded)
}

func (r *Runtime) EnvironmentSampleHistory(id model.EventID) ([]model.EnvironmentSample, error) {
	return r.Environment.History(id)
}
