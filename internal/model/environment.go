package model

import "time"

// EnvironmentSample is one field observation of the tank water (temperature,
// salinity) together with the instrument calibration time and the section and
// time window the observation applies to. Samples are never modified in
// place: a correction appends a new sample that supersedes the old one, so
// the original value stays queryable through the version chain.
type EnvironmentSample struct {
    ID           EventID   `json:"id"`
    Sections     []string  `json:"sections"`
    SourceID     string    `json:"source_id"`
    TemperatureC float64   `json:"temperature_c"`
    SalinityPPT  float64   `json:"salinity_ppt"`
    CalibratedAt time.Time `json:"calibrated_at"`
    SampledAt    time.Time `json:"sampled_at"`
    ValidFrom    time.Time `json:"valid_from"`
    ValidTo      time.Time `json:"valid_to"`
    Supersedes   EventID   `json:"supersedes,omitempty"`
    SupersededBy EventID   `json:"superseded_by,omitempty"`
    RecordedAt   time.Time `json:"recorded_at"`
    Durable      bool      `json:"durable"`
}

func NewEnvironmentSample(sections []string, source string, temperatureC float64, salinityPPT float64, calibratedAt time.Time, sampledAt time.Time, validFrom time.Time, validTo time.Time, supersedes EventID, recordedAt time.Time) EnvironmentSample {
    return EnvironmentSample{
        ID:           NewEventID(),
        Sections:     append([]string(nil), sections...),
        SourceID:     source,
        TemperatureC: temperatureC,
        SalinityPPT:  salinityPPT,
        CalibratedAt: calibratedAt.UTC(),
        SampledAt:    sampledAt.UTC(),
        ValidFrom:    validFrom.UTC(),
        ValidTo:      validTo.UTC(),
        Supersedes:   supersedes,
        RecordedAt:   recordedAt.UTC(),
    }
}

func (s EnvironmentSample) WithDurability(durable bool) EnvironmentSample {
    s.Durable = durable
    return s
}

// Effective reports whether the sample is the current value of its version
// chain; a superseded sample only remains for history.
func (s EnvironmentSample) Effective() bool {
    return s.SupersededBy == ""
}

func (s EnvironmentSample) AppliesTo(section string) bool {
    for _, name := range s.Sections {
        if name == section {
            return true
        }
    }
    return false
}

// Covers reports whether at lies inside the declared validity window.
func (s EnvironmentSample) Covers(at time.Time) bool {
    at = at.UTC()
    return !at.Before(s.ValidFrom) && !at.After(s.ValidTo)
}
