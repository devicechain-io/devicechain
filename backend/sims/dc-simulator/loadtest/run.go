// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// Run executes one load-test profile end to end against the platform the
// handshake points at: provision the scenario, drive it at load for the hold,
// wait for the pipeline to quiesce, reconcile persisted vs. accepted, and return
// the report. It is the L1 orchestration; the caller (cmd/loadtest) turns the
// report's verdict into a process exit code (the CI gate).
func Run(ctx context.Context, hs *sim.Handshake, p Profile) (*Report, error) {
	p = p.withDefaults()
	if err := p.Validate(); err != nil {
		return nil, err
	}

	// Resolve the oracle's read-back endpoint BEFORE provisioning, so a
	// misconfigured handshake fails fast rather than after a full drive.
	eventEndpoint, err := httpGraphQLFromWS(hs.Endpoints.EventMgmtWS)
	if err != nil {
		return nil, err
	}

	// Build the driver (validates the load profile against the scenario) and the
	// runtime (the authenticated tenant session + connection pool sized to the
	// concurrency). Same wiring the persistent sim uses — no special access.
	driver, err := sim.NewSim(p.Manifest, p.Seed, p.Load())
	if err != nil {
		return nil, err
	}
	deviceCount := sim.DeviceCount(driver.Manifest())
	rt, err := sim.NewRuntime(hs, p.Load(), deviceCount)
	if err != nil {
		return nil, err
	}

	// Provision the topology (idempotent create-or-get); fills rt.Devices.
	if err := driver.Bootstrap(ctx, rt); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}

	start, end, err := drive(ctx, rt, driver, p.Hold)
	if err != nil {
		// The drive was aborted (context cancelled). No verdict — the accepted
		// ledger is not a clean stop boundary, so reconciling it would be
		// misleading. Surface the abort.
		return nil, fmt.Errorf("drive aborted: %w", err)
	}
	snap := rt.Stats.Snapshot(end)

	// Reconcile against settled platform truth.
	oracle := &Oracle{
		Counter: &graphqlEventCounter{session: rt.Session, endpoint: eventEndpoint},
		Poll:    p.QuiescePoll,
		Stable:  p.QuiesceStable,
		Timeout: p.QuiesceTimeout,
	}
	qr, err := oracle.Await(ctx, deriveWindow(start, end))
	if err != nil {
		return nil, fmt.Errorf("oracle read-back: %w", err)
	}

	report := &Report{
		Manifest:   p.Manifest,
		Seed:       p.Seed,
		Tenant:     hs.Tenant,
		StartedAt:  start.UTC(),
		FinishedAt: time.Now().UTC(),
		Drive: DriveStats{
			Devices:        len(rt.Devices),
			TargetRatePS:   rt.Load.TargetRate(len(rt.Devices)),
			AchievedRatePS: snap.Rate,
			Accepted:       snap.Emitted,
			Failed:         snap.Failed,
			Ticks:          snap.Ticks,
			HoldSeconds:    end.Sub(start).Seconds(),
		},
		PersistedSeen: qr.Persisted,
		Quiesced:      qr.Settled,
		QuiesceSecs:   qr.Elapsed.Seconds(),
		Invariants:    Reconcile(snap.Emitted, qr.Persisted, qr.Settled),
	}
	return report, nil
}

// drive runs the emit loop for hold, then stops BETWEEN ticks. It deliberately
// does not use sim.Lifecycle.Start/Stop: Stop cancels the tick context, which
// cancels in-flight ingress POSTs — and a POST the server already accepted (202)
// but the client cancelled gets counted Failed, not Emitted, so the accepted
// ledger would understate the truth and the oracle would see persisted > accepted
// and falsely fail. Stopping between whole ticks gives every emit a definitive
// accepted/failed verdict, keeping the ledger exact at the stop boundary. Only a
// real abort (ctx cancelled by the caller) cancels in flight, and that path
// returns an error rather than a verdict.
func drive(ctx context.Context, rt *sim.Runtime, driver sim.Sim, hold time.Duration) (start, end time.Time, err error) {
	start = time.Now()
	rt.Stats.Reset(start)
	deadline := start.Add(hold)

	// Immediate first tick so a hold shorter than one interval still applies a
	// full tick of load. Tick's returned error summarises per-emit failures
	// (counted in Stats, not fatal); only a cancelled context aborts the run.
	// We drive Tick directly (not via Lifecycle) so we own the Ticks counter too.
	rt.Stats.Ticks.Add(1)
	_ = driver.Tick(ctx, rt)
	if ctx.Err() != nil {
		end = time.Now()
		rt.Stats.Freeze(end)
		return start, end, ctx.Err()
	}

	ticker := time.NewTicker(rt.Load.Interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			end = time.Now()
			rt.Stats.Freeze(end)
			return start, end, ctx.Err()
		case now := <-ticker.C:
			if !now.Before(deadline) {
				end = time.Now()
				rt.Stats.Freeze(end)
				return start, end, nil
			}
			rt.Stats.Ticks.Add(1)
			_ = driver.Tick(ctx, rt)
		}
	}
}
