// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ScenarioOptions are the run-time knobs a STORY scenario takes beyond its load: how long its
// compressed day lasts, and which planted defects it runs with. A defect exists so that a verifier
// can be shown to fail: each one breaks exactly one thing the scenario otherwise demonstrates, and a
// verifier that stays green against it is checking nothing.
//
// The zero value is the scenario as authored.
type ScenarioOptions struct {
	// DayLength is the wall time one sim day takes. 0 keeps the scenario's default.
	DayLength time.Duration
	// Defects names the planted defects to run with. Empty runs none.
	Defects []string
}

// IsZero reports whether the options ask for nothing beyond the scenario's defaults.
func (o ScenarioOptions) IsZero() bool { return o.DayLength == 0 && len(o.Defects) == 0 }

// ScenarioConfigurable is a Sim that takes ScenarioOptions. Only a scenario with a day and
// defects implements it; asking any other for either is refused rather than ignored.
type ScenarioConfigurable interface {
	// ConfigureScenario applies the options. It is called once, before the first Manifest the
	// lifecycle reads, and refuses an option the scenario does not support.
	ConfigureScenario(ScenarioOptions) error
}

// ConfigureScenario applies the options to s, refusing them when s takes none. Zero options are a
// no-op on every scenario.
func ConfigureScenario(s Sim, o ScenarioOptions) error {
	if o.IsZero() {
		return nil
	}
	c, ok := s.(ScenarioConfigurable)
	if !ok {
		return fmt.Errorf("--day-length and --scenario-defect apply only to a scenario with a day (harborpulse); this one has none")
	}
	return c.ConfigureScenario(o)
}

// ScenarioClock is the /status view of a story scenario's compressed day: where in the day the
// run is, and what a verifier needs to map a sim time to the wall clock. It is additive JSON on
// the sim's own control API, which only dcctl, scripts and the scenario verifier read.
//
// 🔴 THE JSON TAGS ARE THE CONTRACT with cmd/scenario-verify, which maps a beat's sim time to wall
// time as StartedAt + ((Day*24 + hour) - StartHour) / 24 * DayLength.
type ScenarioClock struct {
	Name string `json:"name"`
	// Started is false until the first tick, which is the instant the day is measured from.
	Started bool `json:"started"`
	// StartedAt is that first tick, RFC 3339 with nanoseconds; empty until Started.
	StartedAt        string  `json:"startedAt,omitempty"`
	DayLengthSeconds float64 `json:"dayLengthSeconds"`
	// StartHour is the sim hour the run opened at.
	StartHour float64 `json:"startHour"`
	// Day is how many sim midnights have passed since the first tick.
	Day int `json:"day"`
	// SimHour is the hour of the sim day, in [0, 24).
	SimHour float64 `json:"simHour"`
	// SimTime is SimHour as HH:MM.
	SimTime string `json:"simTime"`
	// Beat names the story beats in progress, comma separated, or "quiet".
	Beat string `json:"beat"`
	// Defects are the planted defects this run carries; omitted when it carries none.
	Defects []string `json:"defects,omitempty"`
	// Backfill is what each store-and-forward device buffered in each outage so far.
	Backfill            []ScenarioBackfill       `json:"backfill,omitempty"`
	ThresholdChanges    []ThresholdChange        `json:"thresholdChanges,omitempty"`
	CalculatedBackfills []CalculatedBackfillBeat `json:"calculatedBackfills,omitempty"`
}

// ScenarioBackfill is what one device buffered in one outage: how many readings, the times the
// first and last were taken, and the time of every one. A verifier holds the platform's stored
// readings in [First, Last] to exactly that set of times.
type ScenarioBackfill struct {
	Device   string `json:"device"`
	Day      int    `json:"day"`
	Buffered int    `json:"buffered"`
	First    string `json:"first"`
	Last     string `json:"last"`
	// Times is when each buffered reading was taken, oldest first, RFC 3339 with nanoseconds: the
	// exact set a verifier holds the stored readings in [First, Last] to.
	Times []string `json:"times"`
}

// ScenarioClockReporter is a Sim with a compressed day to report on /status.
type ScenarioClockReporter interface {
	ScenarioClock(now time.Time) *ScenarioClock
}

// formatSimTime renders a sim hour as HH:MM.
func formatSimTime(h float64) string {
	minutes := int(h*60) % (24 * 60)
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

// parseDefects checks each named defect against the scenario's own list and returns them sorted
// and de-duplicated. An unknown name is refused: a typo that ran the clean scenario would make a
// control's expected red look like a verifier that cannot fail.
func parseDefects(names []string, known []string) ([]string, error) {
	allowed := map[string]bool{}
	for _, k := range known {
		allowed[k] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range names {
		n := strings.TrimSpace(raw)
		if n == "" {
			continue
		}
		if !allowed[n] {
			return nil, fmt.Errorf("unknown scenario defect %q (known: %s)", n, strings.Join(known, ", "))
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}
