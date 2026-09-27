// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// Run executes one load-test profile end to end against the platform the
// handshake points at: provision the scenario, drive it at load for the hold,
// wait for the pipeline to quiesce, reconcile persisted vs. accepted, wait for
// the live device state to reach every device's last accepted event (unless
// StateTimeout is 0), and return the report. It is the L1 orchestration; the caller (cmd/loadtest) turns the
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
	checkState := p.StateTimeout > 0
	if checkState && strings.TrimSpace(hs.Endpoints.DeviceStateGraphQL) == "" {
		return nil, fmt.Errorf("handshake has no endpoints.deviceStateGraphQL — the state-caught-up check reads the live projection over device-state's tenant API and cannot run without it; re-create the sim record with a current dcctl, or pass --state-timeout 0 to skip the check")
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
	counter := &graphqlEventCounter{session: rt.Session, endpoint: eventEndpoint}
	if checkState {
		rt.Accepted = sim.NewAcceptedLedger()
	}

	// Fresh-tenant precondition. The window reconciliation assumes this tenant is
	// ours exclusively for the run; a persistent sim on the same tenant (dcctl sim
	// start emits the same type-2 events from the same devices) or a prior run
	// still draining would land inside the window and contaminate the count —
	// normally a false FAIL, but a false PASS if the contamination happens to mask
	// a real drop. Refuse to run rather than reconcile against a shared tenant.
	if err := requireCleanTenant(ctx, counter); err != nil {
		return nil, err
	}

	// Provision the topology (idempotent create-or-get); fills rt.Devices. Bootstrap
	// emits no events, so it cannot dirty the tenant the precheck just cleared.
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

	// Reconcile against settled platform truth: poll until the persisted count
	// reaches the accepted target or the timeout backstops.
	oracle := &Oracle{Counter: counter, Poll: p.QuiescePoll, Timeout: p.QuiesceTimeout, Settle: p.QuiesceSettle}
	qr, err := oracle.Await(ctx, deriveWindow(start, end), snap.Emitted)
	if err != nil {
		return nil, fmt.Errorf("oracle read-back: %w", err)
	}

	invariants := Reconcile(snap.Emitted, snap.Failed, qr.Persisted, p.MinAccepted)
	var stateCaughtUp *bool
	var stateLag float64
	if checkState {
		inv, caughtUp, lag, err := awaitLiveState(ctx, rt, hs.Endpoints.DeviceStateGraphQL, end, p)
		if err != nil {
			return nil, err
		}
		invariants = append(invariants, inv)
		stateCaughtUp, stateLag = &caughtUp, lag.Seconds()
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
		Reached:       qr.Reached,
		QuiesceSecs:   qr.Elapsed.Seconds(),
		StateCaughtUp: stateCaughtUp,
		StateLagSecs:  stateLag,
		Invariants:    invariants,
	}
	return report, nil
}

// awaitLiveState is the state-caught-up check: it waits for device-state to reflect every
// device's last accepted event, and returns the verdict, whether it caught up, and how long
// after the drive ended the wait finished.
func awaitLiveState(ctx context.Context, rt *sim.Runtime, endpoint string, end time.Time, p Profile) (Invariant, bool, time.Duration, error) {
	want, unparsed := rt.Accepted.Snapshot()
	if unparsed > 0 {
		// The simulator formats every occurredTime itself, so this is a harness defect, and
		// the ledger cannot say what was accepted.
		return Invariant{Name: InvStateCaughtUp, Passed: false,
			Detail: fmt.Sprintf("inconclusive: %d accepted events' times could not be recorded, so the ledger cannot say what the live state should hold", unparsed)}, false, 0, nil
	}
	reader := &presenceOracle{session: rt.Session, endpoint: endpoint}
	res, err := AwaitState(ctx, reader, want, p.QuiescePoll, p.StateTimeout)
	if err != nil {
		return Invariant{}, false, 0, fmt.Errorf("live-state read-back: %w", err)
	}
	lag := time.Since(end)
	return StateInvariant(res, lag), res.CaughtUp, lag, nil
}

// cleanTenantLookback is how far back requireCleanTenant looks for a competing
// producer's events. It only needs to span "is something emitting right now"
// (a running persistent sim, a just-finished prior run); the drive's own window
// is separate.
const cleanTenantLookback = 30 * time.Second

// requireCleanTenant fails the run if the tenant already carries recent
// measurement events — the window math (deriveWindow) assumes no other producer
// lands events in-window, and a shared tenant breaks that assumption in a way
// that can, at worst, mask a real drop with matching contamination.
func requireCleanTenant(ctx context.Context, c eventCounter) error {
	now := time.Now()
	// End is padded ahead of now so an event a competing producer stamps with a
	// slightly-future occurred_time (clock skew) is still seen.
	recent := Window{Start: now.Add(-cleanTenantLookback), End: now.Add(5 * time.Second)}
	n, err := c.Count(ctx, recent)
	if err != nil {
		return fmt.Errorf("clean-tenant precheck: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("tenant is not clean: %d measurement event(s) in the last %s — stop any running sim on this tenant (dcctl sim stop) or use a fresh tenant; the reconciliation window assumes exclusive ownership", n, cleanTenantLookback)
	}
	return nil
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
