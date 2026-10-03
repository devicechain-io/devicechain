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

// Perturber changes persisted ground truth OUT-OF-BAND — directly beneath the
// tenant GraphQL API the oracle reads through — so the self-test can prove the
// oracle DETECTS a lost or an extra row rather than merely echoing whatever the API
// is willing to show it. It is the controls' stimulus, and it is the one privileged
// capability the self-test has that the load test proper never does (the harness is
// an untrusted client with no DB access; the self-test that validates the harness is
// allowed to reach under it).
type Perturber interface {
	// DeleteOneMeasurement removes exactly one persisted base Measurement event
	// whose occurred_time falls inside w (the earliest), returning its identity and
	// the number of base rows it removed. The self-test requires exactly 1: 0 means
	// the perturber could not find the row the oracle just counted (a bug in one of
	// them), >1 means it over-deleted (an unusable, ambiguous stimulus).
	DeleteOneMeasurement(ctx context.Context, w Window) (IdentityKey, int, error)
	// DuplicateOneMeasurement inserts one extra base Measurement row with the SAME
	// device and occurred_time as an existing one in w (the latest) under a different
	// event_id — the shape of a duplicate that escaped the store's content dedup — and
	// returns its identity and the rows inserted (the self-test requires exactly 1).
	DuplicateOneMeasurement(ctx context.Context, w Window) (IdentityKey, int, error)
}

// SelfTestReport is the meta-verdict on the ORACLE, not on the platform. Its
// controls answer the only question that matters about a gate: can it both pass
// when truth is intact AND fail when truth is perturbed — including the perturbation
// equal totals cannot see? A gate that cannot do the second is a check that cannot
// fail — worse than no gate, because it launders "we never looked" as "we looked and
// it was fine" (research/load-test-harness.md §4.1).
type SelfTestReport struct {
	Manifest string    `json:"manifest"`
	Seed     int64     `json:"seed"`
	Tenant   string    `json:"tenant"`
	Started  time.Time `json:"startedAt"`
	Finished time.Time `json:"finishedAt"`

	Accepted int64 `json:"accepted"`

	// PositiveControl: with truth intact, the oracle's windowed count reached the
	// accepted target, the full count reconcile passed, and the identity
	// reconciliation held. If this does not hold the self-test is INCONCLUSIVE (the
	// platform itself dropped or never drained), not a failure of the oracle.
	PersistedIntact int64          `json:"persistedIntact"`
	IdentityIntact  IdentityReport `json:"identityIntact"`
	PositiveControl Invariant      `json:"positiveControl"`

	// NegativeControl: after exactly one row is deleted out-of-band, the oracle's
	// completeness invariant FLIPS to failed (persisted == accepted-1 < accepted), and
	// the identity reconciliation names exactly the deleted event as missing. This is
	// the proof the gate can catch a silent drop.
	Deleted            int            `json:"deleted"`
	DeletedIdentity    string         `json:"deletedIdentity,omitempty"`
	PersistedPerturbed int64          `json:"persistedPerturbed"`
	IdentityAfterDrop  IdentityReport `json:"identityAfterDrop"`
	NegativeControl    Invariant      `json:"negativeControl"`

	// SwapControl: after a second row is duplicated out-of-band, the totals agree
	// again, so every COUNT invariant passes (the proof that the count is blind to
	// this), and the identity reconciliation fails naming the deleted event missing
	// and the duplicated one stored twice. This is the proof the gate catches a loss
	// offset by a duplicate.
	Duplicated         int            `json:"duplicated"`
	DuplicatedIdentity string         `json:"duplicatedIdentity,omitempty"`
	PersistedSwapped   int64          `json:"persistedSwapped"`
	IdentityAfterSwap  IdentityReport `json:"identityAfterSwap"`
	SwapControl        Invariant      `json:"swapControl"`

	// Inconclusive is set (with a reason) when the environment prevented a clean
	// verdict — the positive control did not hold, so the perturbations were never
	// made, or a perturbation was mis-sized. Distinct from a sound/unsound verdict.
	Inconclusive bool   `json:"inconclusive"`
	Reason       string `json:"reason,omitempty"`
}

// Sound reports whether the oracle proved itself trustworthy: it passed with
// truth intact and failed both ways truth was perturbed. An inconclusive run is not
// sound (nothing was proven) — the caller must resolve the environment and rerun.
func (r *SelfTestReport) Sound() bool {
	return !r.Inconclusive && r.PositiveControl.Passed && r.NegativeControl.Passed && r.SwapControl.Passed
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
	fmt.Fprintf(&b, "  [%-4s] swap-control — %s\n", mark(r.SwapControl.Passed), r.SwapControl.Detail)
	return b.String()
}

// SelfTest drives a known, small volume, confirms the oracle counts and identifies it
// exactly (positive control), then deletes one persisted row out-of-band via perturber
// and confirms both the count and the identity verdict flip to FAIL (negative
// control), then duplicates a second row so the totals agree again and confirms the
// count passes while the identity verdict names both rows (swap control). It
// exercises the SAME drive → quiesce → reconcile path as Run against the SAME real
// GraphQL read-back — only the perturbations are privileged — so what it certifies is
// the production oracle, not a stand-in.
func SelfTest(ctx context.Context, hs *sim.Handshake, p Profile, perturber Perturber) (*SelfTestReport, error) {
	if perturber == nil {
		return nil, fmt.Errorf("self-test needs a perturber (the out-of-band perturbation is the whole test)")
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
	// The same ledger Run keeps: the controls reconcile identity through the production
	// path, and without it the positive control could not hold.
	rt.Identity = sim.NewIdentityLedger()

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

	out, err := evaluateControls(ctx, controlInputs{
		counter:        counter,
		reader:         &graphqlIdentityReader{session: rt.Session, endpoint: eventEndpoint},
		perturber:      perturber,
		window:         deriveWindow(start, end),
		ledger:         rt.Identity,
		devices:        deviceTokens(rt.Devices),
		snap:           snap,
		driveEnd:       end,
		minAccepted:    p.MinAccepted,
		poll:           p.QuiescePoll,
		quiesceTimeout: p.QuiesceTimeout,
	})
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
		IdentityIntact:     out.identityIntact,
		PositiveControl:    out.positive,
		Deleted:            out.deleted,
		DeletedIdentity:    out.deletedKey,
		PersistedPerturbed: out.persistedPerturbed,
		IdentityAfterDrop:  out.identityAfterDrop,
		NegativeControl:    out.negative,
		Duplicated:         out.duplicated,
		DuplicatedIdentity: out.duplicatedKey,
		PersistedSwapped:   out.persistedSwapped,
		IdentityAfterSwap:  out.identityAfterSwap,
		SwapControl:        out.swap,
		Inconclusive:       out.inconclusive,
		Reason:             out.reason,
	}, nil
}

// negativeSettleTimeout bounds the perturbed quiesces. After a clean drive and a
// single deletion the count is already settled at accepted-1, so the production
// Await only needs a few polls to confirm it never climbs to the target; capping it
// keeps the self-test from burning the full 2-minute quiesce budget waiting for a
// count that provably will not reach it.
const negativeSettleTimeout = 10 * time.Second

// controlInputs is everything evaluateControls reads.
type controlInputs struct {
	counter        eventCounter
	reader         identityReader
	perturber      Perturber
	window         Window
	ledger         *sim.IdentityLedger
	devices        []string
	snap           sim.Snapshot
	driveEnd       time.Time
	minAccepted    int64
	poll           time.Duration
	quiesceTimeout time.Duration
}

// controlOutcome is evaluateControls' result, merged into the report by SelfTest.
type controlOutcome struct {
	persistedIntact    int64
	identityIntact     IdentityReport
	positive           Invariant
	deleted            int
	deletedKey         string
	persistedPerturbed int64
	identityAfterDrop  IdentityReport
	negative           Invariant
	duplicated         int
	duplicatedKey      string
	persistedSwapped   int64
	identityAfterSwap  IdentityReport
	swap               Invariant
	inconclusive       bool
	reason             string
}

// evaluateControls runs the three controls against the PRODUCTION oracle path. It
// is separated from SelfTest's provisioning so the load-bearing sequencing is
// unit-testable with a scripted counter, a fake identity reader and a fake perturber:
// a failed positive control MUST short-circuit before the perturber is ever invoked,
// and a mis-sized perturbation MUST go inconclusive without producing a verdict.
//
// Crucially the negative control runs the SAME Oracle.Await the load test uses,
// and relies on its BELOW-TARGET (timeout) branch — the exact path a real
// production drop is reported through. Reconciling a bare Count would leave that
// branch unexercised live, so a bug confined to Await's polling/timeout logic
// could let production false-PASS on a drop while the self-test still read SOUND.
// The swap control relies on the REACHED branch the same way: the count must pass,
// through the production Await and Reconcile, on a store whose totals hide a loss.
func evaluateControls(ctx context.Context, in controlInputs) (controlOutcome, error) {
	var out controlOutcome
	accepted, failed := in.snap.Emitted, in.snap.Failed
	identity := func() (IdentityReport, error) {
		rep, _, err := checkIdentity(ctx, in.counter, in.reader, in.ledger, in.devices, in.snap, in.window, in.driveEnd, 0)
		return rep, err
	}
	negTimeout := negativeSettleTimeout
	if in.quiesceTimeout < negTimeout {
		negTimeout = in.quiesceTimeout
	}

	// Positive control: the production Await must REACH the accepted target with
	// truth intact, the full reconcile must pass, and identity must reconcile. If not,
	// the platform (not the oracle) is the variable — inconclusive, and the perturber
	// is not touched.
	posOracle := &Oracle{Counter: in.counter, Poll: in.poll, Timeout: in.quiesceTimeout}
	pos, err := posOracle.Await(ctx, in.window, accepted)
	if err != nil {
		return out, fmt.Errorf("positive-control read-back: %w", err)
	}
	out.persistedIntact = pos.Persisted
	out.identityIntact, err = identity()
	if err != nil {
		return out, fmt.Errorf("positive-control %w", err)
	}
	out.positive = Invariant{
		Name: "positive-control",
		Passed: positiveControlHeld(accepted, pos.Persisted, failed, in.minAccepted, pos.Reached) &&
			out.identityIntact.Reconciled,
		Detail: fmt.Sprintf("oracle Await reached=%v, read persisted %d against accepted %d with truth intact; identity: %s",
			pos.Reached, pos.Persisted, accepted, identityDetail(out.identityIntact)),
	}
	if !out.positive.Passed {
		out.inconclusive = true
		out.reason = fmt.Sprintf("positive control did not hold (persisted %d vs accepted %d, reached=%v, identity reconciled=%v) — resolve the platform/lag and rerun; the perturber was not invoked",
			pos.Persisted, accepted, pos.Reached, out.identityIntact.Reconciled)
		return out, nil
	}

	// Negative control: delete exactly one row beneath the API.
	deletedKey, deleted, err := in.perturber.DeleteOneMeasurement(ctx, in.window)
	if err != nil {
		return out, fmt.Errorf("perturb (delete one row): %w", err)
	}
	out.deleted = deleted
	if deleted != 1 {
		out.inconclusive = true
		out.reason = fmt.Sprintf("perturber removed %d rows, need exactly 1 — cannot run a clean negative control", deleted)
		return out, nil
	}
	out.deletedKey = deletedKey.String()

	// Re-run the SAME Await; it must now TIME OUT below the target, the production
	// completeness invariant must fail on that below-target count, and identity must
	// name exactly the deleted event as missing.
	negOracle := &Oracle{Counter: in.counter, Poll: in.poll, Timeout: negTimeout}
	neg, err := negOracle.Await(ctx, in.window, accepted)
	if err != nil {
		return out, fmt.Errorf("negative-control read-back: %w", err)
	}
	out.persistedPerturbed = neg.Persisted
	out.identityAfterDrop, err = identity()
	if err != nil {
		return out, fmt.Errorf("negative-control %w", err)
	}
	comp := invariantByName(Reconcile(accepted, failed, neg.Persisted, in.minAccepted), InvCompleteness)
	out.negative = Invariant{
		Name: "negative-control",
		Passed: negativeControlDetected(accepted, out.persistedIntact, neg.Persisted, failed, in.minAccepted, neg.Reached,
			out.identityAfterDrop, deletedKey),
		Detail: fmt.Sprintf("after deleting %s: Await reached=%v persisted %d (was %d), completeness=%v; identity: %s — a detected drop requires NOT reaching target, persisted==accepted-1, completeness=failed, and identity naming exactly the deleted event missing",
			deletedKey, neg.Reached, neg.Persisted, out.persistedIntact, invariantPassed(comp), identityDetail(out.identityAfterDrop)),
	}

	// Swap control: duplicate one other row, so the totals agree again.
	dupKey, duplicated, err := in.perturber.DuplicateOneMeasurement(ctx, in.window)
	if err != nil {
		return out, fmt.Errorf("perturb (duplicate one row): %w", err)
	}
	out.duplicated = duplicated
	if duplicated != 1 {
		out.inconclusive = true
		out.reason = fmt.Sprintf("perturber inserted %d rows, need exactly 1 — cannot run a clean swap control", duplicated)
		return out, nil
	}
	out.duplicatedKey = dupKey.String()

	// The SAME Await must now REACH the target, every count invariant must PASS — the
	// count is blind to a loss offset by a duplicate — and identity must fail naming
	// both events.
	swapOracle := &Oracle{Counter: in.counter, Poll: in.poll, Timeout: negTimeout}
	sw, err := swapOracle.Await(ctx, in.window, accepted)
	if err != nil {
		return out, fmt.Errorf("swap-control read-back: %w", err)
	}
	out.persistedSwapped = sw.Persisted
	out.identityAfterSwap, err = identity()
	if err != nil {
		return out, fmt.Errorf("swap-control %w", err)
	}
	out.swap = Invariant{
		Name: "swap-control",
		Passed: swapControlDetected(accepted, sw.Persisted, failed, in.minAccepted, sw.Reached,
			out.identityAfterSwap, deletedKey, dupKey),
		Detail: fmt.Sprintf("after deleting %s and duplicating %s: Await reached=%v persisted %d, count reconcile passed=%v; identity: %s — a detected swap requires the count to PASS and identity to name exactly the deleted event missing and the duplicated one stored twice",
			deletedKey, dupKey, sw.Reached, sw.Persisted, reconcilePasses(accepted, failed, sw.Persisted, in.minAccepted), identityDetail(out.identityAfterSwap)),
	}
	return out, nil
}

// positiveControlHeld is the pure decision for the positive control's COUNT half:
// with truth intact the oracle must have REACHED the accepted target exactly AND the
// full PRODUCTION reconcile must pass on it. Requiring the whole reconcile to pass —
// not just persisted==accepted — is what makes the baseline clean: a dirty drive
// or an unmet load floor would leave completeness INCONCLUSIVE even at
// persisted==accepted, and you cannot prove a detection against a baseline that
// itself does not cleanly pass. evaluateControls also requires identity to reconcile.
func positiveControlHeld(accepted, persisted, failed, minAccepted int64, reached bool) bool {
	if !reached || persisted != accepted {
		return false
	}
	return reconcilePasses(accepted, failed, persisted, minAccepted)
}

// negativeControlDetected is the pure decision for the negative control: after
// exactly one out-of-band deletion, the production Await must have NOT reached the
// target (its below-target/timeout branch — the path a real drop is reported
// through), the settled count must be exactly accepted-1, the PRODUCTION
// reconcile must fail SPECIFICALLY as a drop — completeness failed while
// load-applied and clean-drive still pass — AND the identity reconciliation must name
// exactly the deleted event as missing, with nothing duplicated or unexpected.
// Requiring all of it means a self-test cannot pass by Await spuriously reaching, by a
// mis-sized deletion, by the oracle going inconclusive for the wrong reason (a dirty
// drive, an unmet floor), or by identity failing on some other event; it must catch
// the deletion as the dropped-event class it exists to catch.
func negativeControlDetected(accepted, intact, after, failed, minAccepted int64, reached bool, id IdentityReport, deleted IdentityKey) bool {
	if reached || after != intact-1 {
		return false
	}
	invs := Reconcile(accepted, failed, after, minAccepted)
	comp := invariantByName(invs, InvCompleteness)
	load := invariantByName(invs, InvLoadApplied)
	clean := invariantByName(invs, InvCleanDrive)
	return comp != nil && !comp.Passed &&
		load != nil && load.Passed &&
		clean != nil && clean.Passed &&
		identityNames(id, deleted, nil)
}

// swapControlDetected is the pure decision for the swap control: after one row was
// deleted and another duplicated, the production Await must have REACHED the target,
// the count must be exactly accepted, EVERY count invariant must pass — the proof the
// count is blind to a loss offset by a duplicate, without which a failing identity
// check proves nothing about what it adds — and the identity reconciliation must name
// exactly the deleted event missing and the duplicated event stored twice.
func swapControlDetected(accepted, after, failed, minAccepted int64, reached bool, id IdentityReport, deleted, duplicated IdentityKey) bool {
	if !reached || after != accepted || !reconcilePasses(accepted, failed, after, minAccepted) {
		return false
	}
	return identityNames(id, deleted, &duplicated)
}

// identityNames reports whether an identity report failed for exactly the stimulus: the
// one missing event, the one duplicated event (stored twice) when there is one, nothing
// else, and nothing that made the verdict inconclusive.
func identityNames(id IdentityReport, missing IdentityKey, duplicated *IdentityKey) bool {
	if id.Reconciled || len(id.Inconclusive) > 0 || id.Unexpected != 0 {
		return false
	}
	if id.Missing != 1 || len(id.Samples.Missing) != 1 || id.Samples.Missing[0] != missing.String() {
		return false
	}
	if duplicated == nil {
		return id.DuplicateKeys == 0
	}
	return id.DuplicateKeys == 1 && id.ExtraCopies == 1 &&
		len(id.Samples.Duplicate) == 1 && id.Samples.Duplicate[0] == duplicated.String()+" x2"
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
