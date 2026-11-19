// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package scenarioverify is the harborpulse oracle: it watches one compressed day of a running
// harborpulse sim and judges what the PLATFORM did, read back over the tenant GraphQL APIs the
// way any client would. It never trusts the simulator's own account of what happened, with one
// stated exception: the store-and-forward ledger (what each charger buffered), which is the
// expectation the stored readings are held to, as a load harness holds its stored events to the
// driver's accepted count.
//
// The judging is pure (Evaluate over Observations), so every invariant is unit-tested against a
// clean day and against the planted defect that must turn it red. The reading is the Run loop.
package scenarioverify

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// Clock maps a sim time of the compressed day to the wall clock.
type Clock struct {
	StartedAt time.Time
	DayLength time.Duration
	StartHour float64
}

// ClockFrom reads the clock off the sim's /status scenario block. A run whose day has not started
// has no clock yet.
func ClockFrom(c *sim.ScenarioClock) (Clock, error) {
	if c == nil {
		return Clock{}, fmt.Errorf("the sim's /status has no scenario clock: it is not a scenario with a day")
	}
	if !c.Started || c.StartedAt == "" {
		return Clock{}, fmt.Errorf("the scenario's day has not started")
	}
	at, err := time.Parse(time.RFC3339Nano, c.StartedAt)
	if err != nil {
		return Clock{}, fmt.Errorf("scenario startedAt %q: %w", c.StartedAt, err)
	}
	if c.DayLengthSeconds <= 0 {
		return Clock{}, fmt.Errorf("scenario dayLengthSeconds %v is not positive", c.DayLengthSeconds)
	}
	return Clock{StartedAt: at, DayLength: time.Duration(c.DayLengthSeconds * float64(time.Second)), StartHour: c.StartHour}, nil
}

// At is the wall time of sim hour `hour` on sim day `day` (day 0 is the day the run opened on).
func (c Clock) At(day int, hour float64) time.Time {
	simHours := float64(day)*24 + hour - c.StartHour
	return c.StartedAt.Add(time.Duration(simHours / 24 * float64(c.DayLength)))
}

// TargetDay is the first day whose sim hour `from` is still ahead of now: the first day the
// verifier can watch from the start of its first beat.
func (c Clock) TargetDay(now time.Time, from float64) int {
	for day := 0; ; day++ {
		if c.At(day, from).After(now) {
			return day
		}
	}
}

// AlarmSample is one alarm as one poll of the alarm list saw it.
type AlarmSample struct {
	At     time.Time `json:"at"`
	Token  string    `json:"token"`
	Device string    `json:"device"`
	// Originator is the alarm's "<originatorType> <originatorId>", the source a duty mail
	// names when device-management could not resolve the device's name or token.
	Originator string `json:"originator,omitempty"`
	Key        string `json:"key"`
	Severity   string `json:"severity"`
	State      string `json:"state"`
}

// Command is one stored command.
type Command struct {
	Device    string    `json:"device"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Queued    time.Time `json:"queued"`
	Responded time.Time `json:"responded"`
}

// Reading is one stored measurement.
type Reading struct {
	Name  string    `json:"name"`
	At    time.Time `json:"at"`
	Value float64   `json:"value"`
}

// Firing is one edge previewRule says a rule would have produced.
type Firing struct {
	At     time.Time `json:"at"`
	Series string    `json:"series"`
	Signal string    `json:"signal"`
}

// Preview is one previewRule answer.
type Preview struct {
	OK       bool     `json:"ok"`
	Degraded string   `json:"degraded,omitempty"`
	Firings  []Firing `json:"firings"`
}

// Backfill is one charger's outage: what the device buffered (the sim's ledger: how many readings,
// the first and last, and the time each was taken) and the times of the readings the platform
// stored inside it.
type Backfill struct {
	Device   string      `json:"device"`
	Buffered int         `json:"buffered"`
	First    time.Time   `json:"first"`
	Last     time.Time   `json:"last"`
	Taken    []time.Time `json:"taken"`
	Stored   []time.Time `json:"stored"`
}

// Observations is everything the verifier read about one sim day.
type Observations struct {
	Clock Clock `json:"-"`
	Day   int   `json:"day"`
	// Alarms is every poll of the alarm list from the start of the watch to its end.
	Alarms []AlarmSample `json:"-"`
	// Polls is the wall time of every poll that answered, in order.
	Polls    []time.Time          `json:"-"`
	Commands []Command            `json:"-"`
	Readings map[string][]Reading `json:"-"`
	// Correlation is the quay-flooding rule previewed over the storm, and CorrelationControl the
	// same rule with a count above the gauges a quay has, which must not fire.
	Correlation        Preview    `json:"-"`
	CorrelationControl Preview    `json:"-"`
	Backfill           []Backfill `json:"-"`
	// S3 is what was read for the deliveries, notifications, geofence, calculated metric and
	// LoRaWAN beats.
	S3 S3 `json:"-"`
	// S4 is what was read for the fleet operations: groups, campaigns, the maintenance window,
	// the asset hierarchy and the relationship graph.
	S4    S4          `json:"-"`
	Maint Maintenance `json:"-"`
}

// The watch: from before the first judged beat to after the last one clears, sim hours.
const (
	WatchFrom = 13.5
	WatchTo   = 23.5
	// stormFrom is the start of the storm's beats.
	stormFrom = 16.0
	// derateTo is the end of the connector-hot beat's window.
	derateTo = 16.5
	// pumpTo is the end of the wet-well beat's window.
	pumpTo = 22.0
	// previewFrom and previewTo bound the correlation preview.
	previewFrom = 17.0
	previewTo   = 22.0
	// maxPollGap is the longest gap between answered polls that still lets a tier be seen: a flood
	// tier lasts tens of seconds at the default day.
	maxPollGap = 20 * time.Second
	// responseWithin is how long after a command's answer its effect must show in the telemetry.
	responseWithin = 30 * time.Second
)

func (o Observations) at(hour float64) time.Time { return o.Clock.At(o.Day, hour) }

// end is the end of the watch, including the settle after it.
func (o Observations) end() time.Time {
	if len(o.Polls) > 0 {
		return o.Polls[len(o.Polls)-1]
	}
	return o.at(WatchTo)
}
