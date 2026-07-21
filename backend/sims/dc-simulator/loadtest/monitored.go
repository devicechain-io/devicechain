// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/loadtest/monitor"
	"github.com/devicechain-io/dc-simulator/sim"
)

// DefaultMonitorCohort is how many probe devices the live safety monitor
// subscribes to. Bounded on purpose (§4.0): big enough to be representative,
// small enough that the monitor never becomes the bottleneck — whole-fleet
// completeness is the L1 aggregate oracle's job, not this one's.
const DefaultMonitorCohort = 16

// monitorDrainWait lets the tail of in-flight events land after the drive stops,
// before the monitor stops watching, so the safety window covers the whole run
// and the observed count is healthy. Lag past this is the aggregate oracle's job.
const monitorDrainWait = 3 * time.Second

// MonitorReport pairs the applied load with the live safety verdict.
type MonitorReport struct {
	Manifest       string         `json:"manifest"`
	Seed           int64          `json:"seed"`
	Tenant         string         `json:"tenant"`
	MinAccepted    int64          `json:"minAccepted"`
	DrainTruncated bool           `json:"drainTruncated"`
	Drive          DriveStats     `json:"drive"`
	Safety         monitor.Report `json:"safety"`
}

// Passed requires the run to have actually applied load AND the live safety
// monitor to be clean. The load floor is not incidental: without it, a drive that
// applied nothing (ingress rejected everything) while a stray producer fed the
// cohort subscriptions would read as a safety PASS — a false green for a run that
// tested nothing (the same lesson L1's Run learned). The safety half then requires
// every cohort device watched and zero violations.
func (r *MonitorReport) Passed() bool {
	return r.Drive.Accepted >= r.MinAccepted && r.Safety.Passed()
}

// JSON renders the report for a CI artifact.
func (r *MonitorReport) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// Human renders an operator summary.
func (r *MonitorReport) Human() string {
	var b strings.Builder
	verdict := "FAIL"
	if r.Passed() {
		verdict = "PASS"
	}
	fmt.Fprintf(&b, "safety-monitor %s — %s (seed %d, tenant %s)\n", verdict, r.Manifest, r.Seed, r.Tenant)
	fmt.Fprintf(&b, "  drive: %d devices, target %.1f ev/s, achieved %.1f ev/s over %.0fs — accepted %d, failed %d\n",
		r.Drive.Devices, r.Drive.TargetRatePS, r.Drive.AchievedRatePS, r.Drive.HoldSeconds, r.Drive.Accepted, r.Drive.Failed)
	fmt.Fprintf(&b, "  monitor: cohort %d devices, observed %d events, %d violation(s)\n",
		r.Safety.Cohort, r.Safety.Observed, len(r.Safety.Violations))
	for _, v := range r.Safety.Violations {
		fmt.Fprintf(&b, "  [FAIL] %s (%s) — %s\n", v.Kind, v.DeviceToken, v.Detail)
	}
	if r.Safety.Observed == 0 {
		fmt.Fprintf(&b, "  [FAIL] the monitor observed no events — it watched nothing, so it proved nothing\n")
	}
	if r.Drive.Accepted < r.MinAccepted {
		fmt.Fprintf(&b, "  [FAIL] load floor not met: accepted %d < %d — the run applied no meaningful load\n",
			r.Drive.Accepted, r.MinAccepted)
	}
	if r.DrainTruncated {
		fmt.Fprintf(&b, "  [note] post-drive drain was cut short (aborted) — the tail window is truncated\n")
	}
	return b.String()
}

// RunMonitored drives the scenario while the live safety monitor watches a
// bounded probe cohort over the real measurementStream, then reports the safety
// verdict. The monitor subscribes BEFORE the drive so it sees the first tick, and
// keeps watching briefly after the drive stops so the tail of in-flight events is
// covered.
func RunMonitored(ctx context.Context, hs *sim.Handshake, p Profile, cohortSize int) (*MonitorReport, error) {
	p = p.withDefaults()
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if cohortSize <= 0 {
		cohortSize = DefaultMonitorCohort
	}

	eventEndpoint, err := httpGraphQLFromWS(hs.Endpoints.EventMgmtWS)
	if err != nil {
		return nil, err
	}
	driver, err := sim.NewSim(p.Manifest, p.Seed, p.Load())
	if err != nil {
		return nil, err
	}
	deviceCount := sim.DeviceCount(driver.Manifest())
	rt, err := sim.NewRuntime(hs, p.Load(), deviceCount)
	if err != nil {
		return nil, err
	}

	// Clean-tenant precondition, same as L1's Run. A persistent sim (dcctl sim
	// start) emits from the SAME deterministic device tokens, so its traffic would
	// interleave on the cohort subscriptions — a false monotonicity violation from
	// cross-producer stamp interleaving, or worse a false PASS if it feeds the
	// cohort while this drive applied no load.
	counter := &graphqlEventCounter{session: rt.Session, endpoint: eventEndpoint}
	if err := requireCleanTenant(ctx, counter); err != nil {
		return nil, err
	}
	if err := driver.Bootstrap(ctx, rt); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}

	tokens := cohortTokens(rt, cohortSize)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("no devices provisioned to monitor")
	}

	// Subscribe before driving so the monitor catches events from the first tick.
	mon, err := monitor.Dial(ctx, hs.Endpoints.EventMgmtWS, rt.Session.AccessToken)
	if err != nil {
		return nil, err
	}
	if err := mon.Watch(ctx, tokens); err != nil {
		_ = mon.Stop()
		return nil, err
	}

	start, end, err := drive(ctx, rt, driver, p.Hold)
	if err != nil {
		_ = mon.Stop()
		return nil, fmt.Errorf("drive aborted: %w", err)
	}
	snap := rt.Stats.Snapshot(end)

	// Let the tail land, then stop watching. A cancelled ctx cuts the drain short.
	drainTruncated := false
	select {
	case <-ctx.Done():
		drainTruncated = true
	case <-time.After(monitorDrainWait):
	}
	_ = mon.Stop() // a socket-close error does not change the safety verdict already recorded

	return &MonitorReport{
		Manifest:       p.Manifest,
		Seed:           p.Seed,
		Tenant:         hs.Tenant,
		MinAccepted:    p.MinAccepted,
		DrainTruncated: drainTruncated,
		Drive: DriveStats{
			Devices:        len(rt.Devices),
			TargetRatePS:   rt.Load.TargetRate(len(rt.Devices)),
			AchievedRatePS: snap.Rate,
			Accepted:       snap.Emitted,
			Failed:         snap.Failed,
			Ticks:          snap.Ticks,
			HoldSeconds:    end.Sub(start).Seconds(),
		},
		Safety: mon.Report(),
	}, nil
}

// cohortTokens returns the first n provisioned device tokens — a bounded,
// deterministic probe cohort (Devices is seeded-deterministic, ADR-050).
func cohortTokens(rt *sim.Runtime, n int) []string {
	if n > len(rt.Devices) {
		n = len(rt.Devices)
	}
	toks := make([]string, 0, n)
	for i := 0; i < n; i++ {
		toks = append(toks, rt.Devices[i].Token)
	}
	return toks
}
