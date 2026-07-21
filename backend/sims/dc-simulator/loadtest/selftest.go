// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// Perturber removes persisted ground truth OUT-OF-BAND — directly beneath the
// tenant GraphQL API the oracle reads through — so the self-test can prove the
// oracle DETECTS a lost row rather than merely echoing whatever the API is
// willing to show it. It is the negative control's stimulus, and it is the one
// privileged capability the self-test has that the load test proper never does
// (the harness is an untrusted client with no DB access; the self-test that
// validates the harness is allowed to reach under it).
type Perturber interface {
	// DeleteOneMeasurement removes exactly one persisted base Measurement event
	// whose occurred_time falls inside w, returning the number of base rows it
	// removed. The self-test requires exactly 1: 0 means the perturber could not
	// find the row the oracle just counted (a bug in one of them), >1 means it
	// over-deleted (an unusable, ambiguous stimulus).
	DeleteOneMeasurement(ctx context.Context, w Window) (int, error)
}

// SelfTestReport is the meta-verdict on the ORACLE, not on the platform. Its two
// controls answer the only question that matters about a gate: can it both pass
// when truth is intact AND fail when truth is perturbed? A gate that cannot do
// the second is a check that cannot fail — worse than no gate, because it
// launders "we never looked" as "we looked and it was fine"
// (research/load-test-harness.md §4.1; the L1 residue this slice closes).
type SelfTestReport struct {
	Manifest string    `json:"manifest"`
	Seed     int64     `json:"seed"`
	Tenant   string    `json:"tenant"`
	Started  time.Time `json:"startedAt"`
	Finished time.Time `json:"finishedAt"`

	Accepted int64 `json:"accepted"`

	// PositiveControl: with truth intact, the oracle's windowed count reached the
	// accepted target — the real GraphQL count really does count the real
	// persisted rows. If this does not hold the self-test is INCONCLUSIVE (the
	// platform itself dropped or never drained), not a failure of the oracle.
	PersistedIntact int64     `json:"persistedIntact"`
	PositiveControl Invariant `json:"positiveControl"`

	// NegativeControl: after exactly one row is deleted out-of-band, the oracle's
	// completeness invariant FLIPS to failed (persisted == accepted-1 < accepted).
	// This is the proof the gate can catch a silent drop.
	Deleted            int       `json:"deleted"`
	PersistedPerturbed int64     `json:"persistedPerturbed"`
	NegativeControl    Invariant `json:"negativeControl"`

	// Inconclusive is set (with a reason) when the environment prevented a clean
	// verdict — the positive control did not hold, so the negative control was
	// never run. Distinct from a sound/unsound verdict.
	Inconclusive bool   `json:"inconclusive"`
	Reason       string `json:"reason,omitempty"`
}

// Sound reports whether the oracle proved itself trustworthy: it passed with
// truth intact and failed when truth was perturbed. An inconclusive run is not
// sound (nothing was proven) — the caller must resolve the environment and rerun.
func (r *SelfTestReport) Sound() bool {
	return !r.Inconclusive && r.PositiveControl.Passed && r.NegativeControl.Passed
}

// JSON renders the self-test report as indented JSON for a CI artifact.
func (r *SelfTestReport) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// Human renders an operator-readable summary. The headline is deliberately about
// the ORACLE ("oracle SOUND/UNSOUND"), not the platform, so nobody confuses a
// self-test verdict with a load-test verdict.
func (r *SelfTestReport) Human() string {
	var b strings.Builder
	verdict := "UNSOUND"
	switch {
	case r.Inconclusive:
		verdict = "INCONCLUSIVE"
	case r.Sound():
		verdict = "SOUND"
	}
	fmt.Fprintf(&b, "oracle self-test %s — %s (seed %d, tenant %s)\n", verdict, r.Manifest, r.Seed, r.Tenant)
	fmt.Fprintf(&b, "  accepted %d\n", r.Accepted)
	if r.Inconclusive {
		fmt.Fprintf(&b, "  inconclusive: %s\n", r.Reason)
		return b.String()
	}
	mark := func(p bool) string {
		if p {
			return "ok"
		}
		return "FAIL"
	}
	fmt.Fprintf(&b, "  [%-4s] positive-control — %s\n", mark(r.PositiveControl.Passed), r.PositiveControl.Detail)
	fmt.Fprintf(&b, "  [%-4s] negative-control — %s\n", mark(r.NegativeControl.Passed), r.NegativeControl.Detail)
	return b.String()
}

// SelfTest drives a known, small volume, confirms the oracle counts it exactly
// (positive control), then deletes one persisted row out-of-band via perturber
// and confirms the oracle's completeness verdict flips to FAIL (negative
// control). It exercises the SAME drive → quiesce → reconcile path as Run against
// the SAME real GraphQL read-back — only the deletion is privileged — so what it
// certifies is the production oracle, not a stand-in.
func SelfTest(ctx context.Context, hs *sim.Handshake, p Profile, perturber Perturber) (*SelfTestReport, error) {
	if perturber == nil {
		return nil, fmt.Errorf("self-test needs a perturber (the out-of-band deletion is the whole test)")
	}
	p = p.withDefaults()
	if err := p.Validate(); err != nil {
		return nil, err
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
	counter := &graphqlEventCounter{session: rt.Session, endpoint: eventEndpoint}

	if err := requireCleanTenant(ctx, counter); err != nil {
		return nil, err
	}
	if err := driver.Bootstrap(ctx, rt); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}

	started := time.Now()
	start, end, err := drive(ctx, rt, driver, p.Hold)
	if err != nil {
		return nil, fmt.Errorf("drive aborted: %w", err)
	}
	snap := rt.Stats.Snapshot(end)
	window := deriveWindow(start, end)

	out, err := evaluateControls(ctx, counter, perturber, window, snap.Emitted, snap.Failed, p.MinAccepted, p.QuiescePoll, p.QuiesceTimeout)
	if err != nil {
		return nil, err
	}
	return &SelfTestReport{
		Manifest:           p.Manifest,
		Seed:               p.Seed,
		Tenant:             hs.Tenant,
		Started:            started.UTC(),
		Finished:           time.Now().UTC(),
		Accepted:           snap.Emitted,
		PersistedIntact:    out.persistedIntact,
		PositiveControl:    out.positive,
		Deleted:            out.deleted,
		PersistedPerturbed: out.persistedPerturbed,
		NegativeControl:    out.negative,
		Inconclusive:       out.inconclusive,
		Reason:             out.reason,
	}, nil
}

// negativeSettleTimeout bounds the negative-control quiesce. After a clean drive
// and a single deletion the count is already settled at accepted-1, so the
// production Await only needs a few polls to confirm it never climbs to the
// target; capping it keeps the self-test from burning the full 2-minute quiesce
// budget waiting for a count that provably will not reach it.
const negativeSettleTimeout = 10 * time.Second

// controlOutcome is evaluateControls' result, merged into the report by SelfTest.
type controlOutcome struct {
	persistedIntact    int64
	positive           Invariant
	deleted            int
	persistedPerturbed int64
	negative           Invariant
	inconclusive       bool
	reason             string
}

// evaluateControls runs the two controls against the PRODUCTION oracle path. It
// is separated from SelfTest's provisioning so the load-bearing sequencing is
// unit-testable with a scripted counter and a fake perturber: a failed positive
// control MUST short-circuit before the perturber is ever invoked, and a
// mis-sized deletion MUST go inconclusive without producing a negative verdict.
//
// Crucially the negative control runs the SAME Oracle.Await the load test uses,
// and relies on its BELOW-TARGET (timeout) branch — the exact path a real
// production drop is reported through. Reconciling a bare Count would leave that
// branch unexercised live, so a bug confined to Await's polling/timeout logic
// could let production false-PASS on a drop while the self-test still read SOUND.
func evaluateControls(ctx context.Context, counter eventCounter, perturber Perturber, window Window, accepted, failed, minAccepted int64, poll, quiesceTimeout time.Duration) (controlOutcome, error) {
	var out controlOutcome

	// Positive control: the production Await must REACH the accepted target with
	// truth intact, and the full reconcile must pass. If not, the platform (not
	// the oracle) is the variable — inconclusive, and the perturber is not touched.
	posOracle := &Oracle{Counter: counter, Poll: poll, Timeout: quiesceTimeout}
	pos, err := posOracle.Await(ctx, window, accepted)
	if err != nil {
		return out, fmt.Errorf("positive-control read-back: %w", err)
	}
	out.persistedIntact = pos.Persisted
	out.positive = Invariant{
		Name:   "positive-control",
		Passed: positiveControlHeld(accepted, pos.Persisted, failed, minAccepted, pos.Reached),
		Detail: fmt.Sprintf("oracle Await reached=%v, read persisted %d against accepted %d with truth intact", pos.Reached, pos.Persisted, accepted),
	}
	if !out.positive.Passed {
		out.inconclusive = true
		out.reason = fmt.Sprintf("positive control did not hold (persisted %d vs accepted %d, reached=%v) — resolve the platform/lag and rerun; the perturber was not invoked",
			pos.Persisted, accepted, pos.Reached)
		return out, nil
	}

	// Negative control: delete exactly one row beneath the API.
	deleted, err := perturber.DeleteOneMeasurement(ctx, window)
	if err != nil {
		return out, fmt.Errorf("perturb (delete one row): %w", err)
	}
	out.deleted = deleted
	if deleted != 1 {
		out.inconclusive = true
		out.reason = fmt.Sprintf("perturber removed %d rows, need exactly 1 — cannot run a clean negative control", deleted)
		return out, nil
	}

	// Re-run the SAME Await; it must now TIME OUT below the target, and the
	// production completeness invariant must fail on that below-target count.
	negTimeout := negativeSettleTimeout
	if quiesceTimeout < negTimeout {
		negTimeout = quiesceTimeout
	}
	negOracle := &Oracle{Counter: counter, Poll: poll, Timeout: negTimeout}
	neg, err := negOracle.Await(ctx, window, accepted)
	if err != nil {
		return out, fmt.Errorf("negative-control read-back: %w", err)
	}
	out.persistedPerturbed = neg.Persisted
	comp := invariantByName(Reconcile(accepted, failed, neg.Persisted, minAccepted), InvCompleteness)
	out.negative = Invariant{
		Name:   "negative-control",
		Passed: negativeControlDetected(accepted, out.persistedIntact, neg.Persisted, failed, minAccepted, neg.Reached),
		Detail: fmt.Sprintf("after deleting 1 row: Await reached=%v persisted %d (was %d), completeness=%v — a detected drop requires NOT reaching target, persisted==accepted-1, completeness=failed",
			neg.Reached, neg.Persisted, out.persistedIntact, invariantPassed(comp)),
	}
	return out, nil
}

// positiveControlHeld is the pure decision for the positive control: with truth
// intact the oracle must have REACHED the accepted target exactly AND the full
// PRODUCTION reconcile must pass on it. Requiring the whole reconcile to pass —
// not just persisted==accepted — is what makes the baseline clean: a dirty drive
// or an unmet load floor would leave completeness INCONCLUSIVE even at
// persisted==accepted, and you cannot prove a detection against a baseline that
// itself does not cleanly pass.
func positiveControlHeld(accepted, persisted, failed, minAccepted int64, reached bool) bool {
	if !reached || persisted != accepted {
		return false
	}
	return reconcilePasses(accepted, failed, persisted, minAccepted)
}

// negativeControlDetected is the pure decision for the negative control: after
// exactly one out-of-band deletion, the production Await must have NOT reached the
// target (its below-target/timeout branch — the path a real drop is reported
// through), the settled count must be exactly accepted-1, AND the PRODUCTION
// reconcile must fail SPECIFICALLY as a drop — completeness failed while
// load-applied and clean-drive still pass. Requiring all three means a self-test
// cannot pass by Await spuriously reaching, by a mis-sized deletion, or by the
// oracle going inconclusive for the wrong reason (a dirty drive, an unmet floor);
// it must catch the deletion as the dropped-event class it exists to catch.
func negativeControlDetected(accepted, intact, after, failed, minAccepted int64, reached bool) bool {
	if reached || after != intact-1 {
		return false
	}
	invs := Reconcile(accepted, failed, after, minAccepted)
	comp := invariantByName(invs, InvCompleteness)
	load := invariantByName(invs, InvLoadApplied)
	clean := invariantByName(invs, InvCleanDrive)
	return comp != nil && !comp.Passed &&
		load != nil && load.Passed &&
		clean != nil && clean.Passed
}

// reconcilePasses reports whether every invariant in a reconcile result held.
func reconcilePasses(accepted, failed, persisted, minAccepted int64) bool {
	for _, inv := range Reconcile(accepted, failed, persisted, minAccepted) {
		if !inv.Passed {
			return false
		}
	}
	return true
}

// invariantByName returns the named invariant from a reconcile result, or nil.
func invariantByName(inv []Invariant, name string) *Invariant {
	for i := range inv {
		if inv[i].Name == name {
			return &inv[i]
		}
	}
	return nil
}

func invariantPassed(inv *Invariant) bool {
	return inv != nil && inv.Passed
}
