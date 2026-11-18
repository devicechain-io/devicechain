// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-simulator/sim"
)

// The verdicts, as process exit codes: the loadtest harnesses' convention.
const (
	ExitPass      = 0
	ExitFail      = 1
	ExitNoVerdict = 2
)

// Report is the verifier's answer.
type Report struct {
	Scenario   string      `json:"scenario"`
	Tenant     string      `json:"tenant"`
	Day        int         `json:"day"`
	WatchFrom  time.Time   `json:"watchFrom"`
	WatchTo    time.Time   `json:"watchTo"`
	Defects    []string    `json:"defects"`
	Polls      int         `json:"polls"`
	FailedPoll int         `json:"failedPolls"`
	Invariants []Invariant `json:"invariants"`
	// NoVerdict is set when the day could not be judged; Invariants is then empty.
	NoVerdict string `json:"noVerdict,omitempty"`
}

// ExitCode is the report's verdict as an exit status.
func (r *Report) ExitCode() int {
	if r.NoVerdict != "" {
		return ExitNoVerdict
	}
	for _, inv := range r.Invariants {
		if !inv.Passed {
			return ExitFail
		}
	}
	return ExitPass
}

// JSON renders the report.
func (r *Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// Human renders the report for a terminal.
func (r *Report) Human() string {
	var b strings.Builder
	fmt.Fprintf(&b, "harborpulse scenario-verify: tenant %s, sim day %d", r.Tenant, r.Day)
	if len(r.Defects) > 0 {
		fmt.Fprintf(&b, ", PLANTED DEFECTS %s", strings.Join(r.Defects, ","))
	}
	fmt.Fprintf(&b, "\n  watched %s .. %s, %d polls (%d failed)\n", r.WatchFrom.Format(time.RFC3339), r.WatchTo.Format(time.RFC3339), r.Polls, r.FailedPoll)
	if r.NoVerdict != "" {
		fmt.Fprintf(&b, "  NO VERDICT: %s\n", r.NoVerdict)
		return b.String()
	}
	for _, inv := range r.Invariants {
		mark := "PASS"
		if !inv.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "  %s %-26s %s\n", mark, inv.Name, inv.Detail)
	}
	switch r.ExitCode() {
	case ExitPass:
		b.WriteString("  VERDICT: pass\n")
	default:
		b.WriteString("  VERDICT: FAIL\n")
	}
	return b.String()
}

// Config is one verification run.
type Config struct {
	Handshake *sim.Handshake
	// SimURL is the sim's control API, which carries the scenario clock.
	SimURL string
	// HTTP reaches the platform (it may carry the handshake's Host override); StatusHTTP reaches
	// the sim's own control API.
	HTTP       *http.Client
	StatusHTTP *http.Client
	// Poll is how often the alarm list is read through the watch.
	Poll time.Duration
	// Settle is how long the watch runs past its last beat, so the last clear is seen.
	Settle time.Duration
	// StartTimeout bounds the wait for the day to start.
	StartTimeout time.Duration
	// Day, when >= 0, is the sim day to judge; otherwise the first whose watch has not begun.
	Day int
}

func (c Config) withDefaults() Config {
	if c.Poll <= 0 {
		c.Poll = 2 * time.Second
	}
	if c.Settle <= 0 {
		c.Settle = 20 * time.Second
	}
	if c.StartTimeout <= 0 {
		c.StartTimeout = 5 * time.Minute
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if c.StatusHTTP == nil {
		c.StatusHTTP = &http.Client{Timeout: 10 * time.Second}
	}
	return c
}

// noVerdict is an error that ends the run without a verdict.
type noVerdict struct{ err error }

func (n noVerdict) Error() string { return n.err.Error() }

func (n noVerdict) Unwrap() error { return n.err }

func noVerdictf(format string, a ...any) error { return noVerdict{fmt.Errorf(format, a...)} }

// Run watches one sim day and judges it. It returns a report in every case it can describe — a
// verdict, or no verdict with the reason — and an error only when ctx ends.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	cfg = cfg.withDefaults()
	rep := &Report{Scenario: "harborpulse", Tenant: cfg.Handshake.Tenant}
	err := run(ctx, cfg, rep)
	if err == nil {
		return rep, nil
	}
	if ctx.Err() != nil {
		return rep, ctx.Err()
	}
	var nv noVerdict
	if !errors.As(err, &nv) {
		err = noVerdict{err}
	}
	rep.NoVerdict = err.Error()
	rep.Invariants = nil
	return rep, nil
}

func run(ctx context.Context, cfg Config, rep *Report) error {
	if cfg.Handshake.ManifestId != "harborpulse" {
		return noVerdictf("the handshake is for scenario %q; this verifier judges harborpulse", cfg.Handshake.ManifestId)
	}
	reader, err := NewReader(cfg.Handshake, cfg.HTTP)
	if err != nil {
		return noVerdict{err}
	}

	clock, status, err := awaitStart(ctx, cfg)
	if err != nil {
		return err
	}
	rep.Defects = status.Scenario.Defects
	day := cfg.Day
	if day < 0 {
		day = clock.TargetDay(time.Now(), WatchFrom)
	}
	obs := Observations{Clock: clock, Day: day, Readings: map[string][]Reading{}}
	from, to := clock.At(day, WatchFrom), clock.At(day, WatchTo).Add(cfg.Settle)
	rep.Day, rep.WatchFrom, rep.WatchTo = day, from, to
	if time.Now().After(from.Add(maxPollGap)) {
		return noVerdictf("sim day %d's watch began at %s, before this run could start it", day, from.Format(time.RFC3339))
	}
	log.Info().Int("day", day).Time("from", from).Time("to", to).Strs("defects", rep.Defects).
		Msg("waiting for the watch to begin")
	if err := sleepUntil(ctx, from); err != nil {
		return err
	}

	// The watch: the alarm list every Poll, the clock every few polls (a restarted sim has a new
	// day, and its beats would land where this run is not looking).
	tick := time.NewTicker(cfg.Poll)
	defer tick.Stop()
	for i := 0; ; i++ {
		now := time.Now()
		if now.After(to) {
			break
		}
		if i%10 == 0 {
			s, err := ReadStatus(ctx, cfg.StatusHTTP, cfg.SimURL)
			if err == nil {
				if c, cerr := ClockFrom(s.Scenario); cerr == nil && !c.StartedAt.Equal(clock.StartedAt) {
					return noVerdictf("the sim's day restarted during the watch (started %s, now %s)", clock.StartedAt, c.StartedAt)
				}
				if s.State != string(sim.StateRunning) {
					return noVerdictf("the sim is %s during the watch", s.State)
				}
			}
		}
		alarms, err := reader.Alarms(ctx, now)
		if err != nil {
			rep.FailedPoll++
			log.Warn().Err(err).Msg("alarm poll failed")
		} else {
			obs.Alarms = append(obs.Alarms, alarms...)
			obs.Polls = append(obs.Polls, now)
		}
		rep.Polls++
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
	if err := Coverage(obs); err != nil {
		return noVerdict{err}
	}

	// The read-back.
	for _, p := range fleetOf(sim.HarborpulsePumpType, "") {
		if err := readDevice(ctx, reader, &obs, p, obs.at(stormFrom).Add(-responseWithin), obs.at(pumpTo).Add(responseWithin)); err != nil {
			return noVerdict{err}
		}
	}
	if err := readDevice(ctx, reader, &obs, sim.HarborpulseHotCharger, obs.at(WatchFrom), obs.at(derateTo).Add(responseWithin)); err != nil {
		return noVerdict{err}
	}
	gaugeDef, err := manifestRuleDefinition(sim.HarborpulseGaugeProfile, sim.HarborpulseQuayFloodingRule)
	if err != nil {
		return noVerdict{err}
	}
	if obs.Correlation, err = reader.Preview(ctx, gaugeDef, sim.HarborpulseGaugeProfile, obs.at(previewFrom), obs.at(previewTo)); err != nil {
		return noVerdict{err}
	}
	gaugesPerQuay := len(fleetOf(sim.HarborpulseGaugeType, sim.HarborpulseEastQuay))
	control := sim.HarborpulseQuayFloodingDefinition(gaugesPerQuay + 1)
	if obs.CorrelationControl, err = reader.Preview(ctx, control, sim.HarborpulseGaugeProfile, obs.at(previewFrom), obs.at(previewTo)); err != nil {
		return noVerdict{err}
	}
	final, err := ReadStatus(ctx, cfg.StatusHTTP, cfg.SimURL)
	if err != nil {
		return noVerdict{err}
	}
	if final.Scenario == nil {
		return noVerdictf("the sim's final /status has no scenario clock")
	}
	for _, b := range final.Scenario.Backfill {
		if b.Day != day {
			continue
		}
		first, ferr := time.Parse(time.RFC3339Nano, b.First)
		last, lerr := time.Parse(time.RFC3339Nano, b.Last)
		if ferr != nil || lerr != nil {
			return noVerdictf("the sim's backfill ledger for %s has unparseable times %q .. %q", b.Device, b.First, b.Last)
		}
		readings, err := reader.Readings(ctx, b.Device, first.Add(-time.Second), last.Add(time.Second))
		if err != nil {
			return noVerdict{err}
		}
		if len(b.Times) != b.Buffered {
			return noVerdictf("the sim's backfill ledger for %s counts %d buffered readings but carries %d times", b.Device, b.Buffered, len(b.Times))
		}
		bf := Backfill{Device: b.Device, Buffered: b.Buffered, First: first, Last: last}
		for _, s := range b.Times {
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				return noVerdictf("the sim's backfill ledger for %s has an unparseable time %q", b.Device, s)
			}
			bf.Taken = append(bf.Taken, t)
		}
		for _, r := range readings {
			// One row per reading: the metric the charger-offline rule watches.
			if r.Name == sim.HarborpulseChargerPowerKey {
				bf.Stored = append(bf.Stored, r.At)
			}
		}
		obs.Backfill = append(obs.Backfill, bf)
	}
	rep.Invariants = Evaluate(obs)
	return nil
}

func readDevice(ctx context.Context, r *Reader, obs *Observations, device string, from, to time.Time) error {
	cmds, err := r.Commands(ctx, device)
	if err != nil {
		return err
	}
	obs.Commands = append(obs.Commands, cmds...)
	readings, err := r.Readings(ctx, device, from, to)
	if err != nil {
		return err
	}
	obs.Readings[device] = append(obs.Readings[device], readings...)
	return nil
}

// manifestRuleDefinition is a rule's definition as the scenario publishes it.
func manifestRuleDefinition(profile, rule string) (string, error) {
	m, ok := sim.ScenarioManifest("harborpulse")
	if !ok {
		return "", fmt.Errorf("harborpulse is not a registered scenario")
	}
	for _, p := range m.Profiles {
		if p.Token != profile {
			continue
		}
		for _, r := range p.DetectionRules {
			if r.Token == rule {
				return r.Definition, nil
			}
		}
	}
	return "", fmt.Errorf("harborpulse publishes no rule %s on %s", rule, profile)
}

// awaitStart waits for the sim to be running with its day started.
func awaitStart(ctx context.Context, cfg Config) (Clock, Status, error) {
	deadline := time.Now().Add(cfg.StartTimeout)
	var last error
	for time.Now().Before(deadline) {
		s, err := ReadStatus(ctx, cfg.StatusHTTP, cfg.SimURL)
		if err == nil {
			if s.Tenant != "" && s.Tenant != cfg.Handshake.Tenant {
				return Clock{}, Status{}, noVerdictf("the sim at %s runs tenant %s, the handshake names %s", cfg.SimURL, s.Tenant, cfg.Handshake.Tenant)
			}
			c, cerr := ClockFrom(s.Scenario)
			if cerr == nil {
				return c, s, nil
			}
			err = cerr
		}
		last = err
		if err := sleepUntil(ctx, time.Now().Add(2*time.Second)); err != nil {
			return Clock{}, Status{}, err
		}
	}
	return Clock{}, Status{}, noVerdictf("the scenario's day did not start within %s: %v", cfg.StartTimeout, last)
}

func sleepUntil(ctx context.Context, t time.Time) error {
	d := time.Until(t)
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
