// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// This is L3 — the tiered ADR-063 contention profile, the acceptance test for
// preferential shedding (research/load-test-harness.md §4; ADR-064 decision 5). Two
// probe tenants drive the SAME scenario at the SAME rate over the shared platform,
// distinguished ONLY by their ADR-063 shed priority (set at `dcctl sim create
// --shed-priority`): a GOLD probe (never-shed band) and a SHED probe (best-effort
// band). Under an operator-set contention floor (event-sources config), the mechanism
// lowers the shed probe's effective ingest ceiling so it sheds (429s) while gold's
// ceiling is untouched.
//
// The oracle is fidelity-pure and needs no server counter: a shed is an HTTP 429 the
// driver SEES directly (Stats.Shed), returned before the body is read, so a shed event
// provably never entered the pipeline. The gate therefore reconciles, per tenant:
//   - GOLD rides through — zero sheds AND persisted == accepted (zero loss);
//   - the SHED probe loses ONLY what it was told it lost — persisted == accepted with
//     the 429s correctly absent (shedding may drop, it must never corrupt), and it
//     actually shed (the mechanism engaged — else "gold rode through" is vacuous).
//
// The floor is NOT read by the harness (it is event-sources instance config); the
// operator states it via ExpectFloor so the classifier knows the regime: ExpectFloor
// 0 is the NEGATIVE CONTROL — the mechanism is off, so NEITHER tenant may shed, which
// proves the sheds in the positive run were caused by the floor, not the base ceiling.

// Invariant names for the contention gate.
const (
	InvContentionCleanGold = "gold-clean-drive"
	InvContentionCleanShed = "shed-clean-drive"
	InvGoldNeverShed       = "gold-never-shed"
	InvGoldZeroLoss        = "gold-zero-loss"
	InvShedEngaged         = "shed-mechanism-engaged"
	InvShedNoCorruption    = "shed-no-corruption"
	InvContentionLoadFloor = "contention-load-floor"
)

// ContentionConfig is one contention-profile run's configuration. The Profile sizes
// and paces the drive applied to EACH probe tenant identically (determinism per
// ADR-064 decision 6); ExpectFloor tells the classifier which regime the operator set
// event-sources to.
type ContentionConfig struct {
	Profile

	// ExpectFloor is the ADR-063 contention manual_floor the operator set on
	// event-sources for this run (0..3). The harness cannot read it (it is instance
	// config, not exposed), so it is stated here and the classifier derives its
	// expectations from it: 0 => neither tenant may shed (the negative control); >= 1
	// => the best-effort SHED probe must shed while gold rides through.
	ExpectFloor int
}

// withDefaults fills the embedded Profile's optional fields and picks a drive rate that
// sits in the contention sweet spot: above the shed probe's floor-reduced ceiling (so
// it sheds) yet below the base ceiling (so GOLD never hits its own ADR-023 limit — a
// gold shed there would be an ADR-023 artifact, not an ADR-063 failure, and would
// wrongly fail gold-never-shed). At the platform default 1000/s ceiling, 100 devices ×
// 200ms ≈ 500/s satisfies both for floors 1 and 2.
func (c ContentionConfig) withDefaults() ContentionConfig {
	if c.Devices == 0 {
		c.Devices = 100
	}
	if c.EmitInterval == 0 {
		c.EmitInterval = 200 * time.Millisecond
	}
	c.Profile = c.Profile.withDefaults()
	return c
}

// Validate rejects a config that cannot be run as asked.
func (c ContentionConfig) Validate() error {
	if c.ExpectFloor < 0 || c.ExpectFloor > 3 {
		return fmt.Errorf("expectFloor must be between 0 and 3 (got %d)", c.ExpectFloor)
	}
	return c.Profile.Validate()
}

// tenantOutcome is one probe tenant's drive ledger and reconciliation, the raw input
// to the pure classifier.
type tenantOutcome struct {
	Role      string
	Tenant    string
	Devices   int
	Accepted  int64 // HTTP 202
	Shed      int64 // HTTP 429 (governed, clean non-accept)
	Failed    int64 // real errors / indeterminate outcomes
	Persisted int64 // oracle-observed persisted count over the window
	Reached   bool  // whether persisted reached the accepted target within the quiesce timeout
	Rate      float64
}

// runOneTenant drives one probe tenant through the full L1 path — clean-tenant
// precheck, bootstrap, drive-then-stop-between-ticks, quiesce reconcile — and returns
// its raw ledger + persisted count. It is the per-tenant half of the contention run,
// called concurrently for gold and shed so they genuinely contend on the shared
// platform (and so gold is proven to ride through WHILE the shed probe is being shed).
func runOneTenant(ctx context.Context, role string, hs *sim.Handshake, p Profile) (*tenantOutcome, error) {
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
		return nil, fmt.Errorf("%s tenant %q: %w", role, hs.Tenant, err)
	}
	if err := driver.Bootstrap(ctx, rt); err != nil {
		return nil, fmt.Errorf("%s bootstrap: %w", role, err)
	}

	start, end, err := drive(ctx, rt, driver, p.Hold)
	if err != nil {
		return nil, fmt.Errorf("%s drive aborted: %w", role, err)
	}
	snap := rt.Stats.Snapshot(end)

	oracle := &Oracle{Counter: counter, Poll: p.QuiescePoll, Timeout: p.QuiesceTimeout, Settle: p.QuiesceSettle}
	qr, err := oracle.Await(ctx, deriveWindow(start, end), snap.Emitted)
	if err != nil {
		return nil, fmt.Errorf("%s oracle read-back: %w", role, err)
	}
	return &tenantOutcome{
		Role:      role,
		Tenant:    hs.Tenant,
		Devices:   len(rt.Devices),
		Accepted:  snap.Emitted,
		Shed:      snap.Shed,
		Failed:    snap.Failed,
		Persisted: qr.Persisted,
		Reached:   qr.Reached,
		Rate:      snap.Rate,
	}, nil
}

// RunContentionProbes drives the gold and shed probe tenants concurrently and returns
// the acceptance-test verdict. gold and shed are separate handshakes for separate
// tenants created at different shed priorities (dcctl sim create --shed-priority: gold
// in the 80–100 band, shed in the 1–19 band); the operator has set event-sources'
// contention floor to cfg.ExpectFloor before the run.
func RunContentionProbes(ctx context.Context, gold, shed *sim.Handshake, cfg ContentionConfig) (*ContentionReport, error) {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if gold.Tenant == shed.Tenant {
		return nil, fmt.Errorf("gold and shed handshakes name the same tenant %q — the contention profile needs two distinct tenants at different shed priorities", gold.Tenant)
	}

	started := time.Now()
	// Drive both tenants concurrently so they contend on the shared platform at the
	// same time — gold must be shown to ride through WHILE the shed probe is shedding.
	var (
		wg               sync.WaitGroup
		goldOut, shedOut *tenantOutcome
		goldErr, shedErr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); goldOut, goldErr = runOneTenant(ctx, "gold", gold, cfg.Profile) }()
	go func() { defer wg.Done(); shedOut, shedErr = runOneTenant(ctx, "shed", shed, cfg.Profile) }()
	wg.Wait()
	if goldErr != nil {
		return nil, goldErr
	}
	if shedErr != nil {
		return nil, shedErr
	}

	return &ContentionReport{
		Manifest:    cfg.Manifest,
		Seed:        cfg.Seed,
		ExpectFloor: cfg.ExpectFloor,
		StartedAt:   started.UTC(),
		FinishedAt:  time.Now().UTC(),
		Gold:        *goldOut,
		Shed:        *shedOut,
		Invariants:  classifyContention(cfg, goldOut, shedOut),
	}, nil
}

// ContentionReport is the machine- and human-readable result of one L3 run: the
// profile that produced it, both probe tenants' ledgers, and the per-invariant verdicts.
type ContentionReport struct {
	Manifest    string        `json:"manifest"`
	Seed        int64         `json:"seed"`
	ExpectFloor int           `json:"expectFloor"`
	StartedAt   time.Time     `json:"startedAt"`
	FinishedAt  time.Time     `json:"finishedAt"`
	Gold        tenantOutcome `json:"gold"`
	Shed        tenantOutcome `json:"shed"`
	Invariants  []Invariant   `json:"invariants"`
}

// Passed reports whether every invariant held. An empty set is NOT a pass — a report
// that asserted nothing has proven nothing (same rule as the L1 Report).
func (r *ContentionReport) Passed() bool {
	if len(r.Invariants) == 0 {
		return false
	}
	for _, inv := range r.Invariants {
		if !inv.Passed {
			return false
		}
	}
	return true
}

// JSON renders the report as indented JSON for the CI artifact.
func (r *ContentionReport) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// Human renders a terse operator-readable summary.
func (r *ContentionReport) Human() string {
	var b strings.Builder
	verdict := "FAIL"
	if r.Passed() {
		verdict = "PASS"
	}
	regime := "negative control (floor 0 — no tenant may shed)"
	if r.ExpectFloor >= 1 {
		regime = fmt.Sprintf("contended (floor %d — the shed probe must shed, gold must not)", r.ExpectFloor)
	}
	fmt.Fprintf(&b, "contention %s — %s, seed %d — %s\n", verdict, r.Manifest, r.Seed, regime)
	line := func(o tenantOutcome) {
		fmt.Fprintf(&b, "  %-4s %q: accepted %d, shed %d, failed %d, persisted %d (%.0f ev/s)\n",
			o.Role, o.Tenant, o.Accepted, o.Shed, o.Failed, o.Persisted, o.Rate)
	}
	line(r.Gold)
	line(r.Shed)
	for _, inv := range r.Invariants {
		mark := "FAIL"
		if inv.Passed {
			mark = "ok"
		}
		fmt.Fprintf(&b, "  [%-4s] %s — %s\n", mark, inv.Name, inv.Detail)
	}
	return b.String()
}

// classifyContention is the pure ADR-063 acceptance verdict. Side-effect-free so every
// branch is unit-tested and mutation-verified — a gate that false-PASSes is worse than
// none.
//
// The regime is set by ExpectFloor:
//   - ExpectFloor == 0 (negative control): the mechanism is OFF, so NEITHER tenant may
//     shed. If the shed probe shed here, the sheds are an ADR-023 base-ceiling artifact,
//     not ADR-063 — the whole positive test would be measuring the wrong thing.
//   - ExpectFloor >= 1: the best-effort shed probe MUST shed (the mechanism engaged —
//     else "gold rode through" is vacuous), while gold must not.
//
// In BOTH regimes: gold never sheds, and each tenant's accepted events all persist
// (shedding drops, it must never corrupt what it let through).
func classifyContention(cfg ContentionConfig, gold, shed *tenantOutcome) []Invariant {
	var inv []Invariant
	add := func(name string, passed bool, detail string) {
		inv = append(inv, Invariant{Name: name, Passed: passed, Detail: detail})
	}

	// Clean drive per tenant: a real failure (not a governed shed) makes the accepted
	// ledger ambiguous, so neither completeness check below is trustworthy.
	add(InvContentionCleanGold, gold.Failed == 0,
		fmt.Sprintf("gold %q had %d real emit failures (need 0; a shed is not a failure)", gold.Tenant, gold.Failed))
	add(InvContentionCleanShed, shed.Failed == 0,
		fmt.Sprintf("shed %q had %d real emit failures (need 0; a shed is not a failure)", shed.Tenant, shed.Failed))

	// The promise: gold is NEVER shed, at any floor.
	add(InvGoldNeverShed, gold.Shed == 0,
		fmt.Sprintf("gold %q shed %d events — gold must never be shed (the ADR-063 promise)", gold.Tenant, gold.Shed))

	// Gold zero-loss: every accepted gold event persisted. Only trustworthy if the gold
	// drive was clean.
	add(InvGoldZeroLoss, gold.Failed == 0 && gold.Persisted == gold.Accepted,
		fmt.Sprintf("gold %q persisted %d of %d accepted (want equal — zero loss)", gold.Tenant, gold.Persisted, gold.Accepted))

	// Shed-mechanism behavior, regime-dependent.
	if cfg.ExpectFloor >= 1 {
		add(InvShedEngaged, shed.Shed > 0,
			fmt.Sprintf("shed %q shed %d events at floor %d — the mechanism must engage (else gold riding through is vacuous)", shed.Tenant, shed.Shed, cfg.ExpectFloor))
	} else {
		// Negative control: no floor ⇒ no shedding. A shed here is an ADR-023 base-ceiling
		// artifact and would invalidate the positive test's attribution.
		add(InvShedEngaged, shed.Shed == 0,
			fmt.Sprintf("shed %q shed %d events at floor 0 — the negative control requires ZERO shedding (a shed here is a base-ceiling artifact, not ADR-063)", shed.Tenant, shed.Shed))
	}

	// No corruption: everything the shed probe DID get accepted persisted. This is the
	// "loses only what it was told it lost" half — the 429s are correctly absent, and
	// what got through is exactly correct.
	add(InvShedNoCorruption, shed.Failed == 0 && shed.Persisted == shed.Accepted,
		fmt.Sprintf("shed %q persisted %d of %d accepted (want equal — shedding drops, it must not corrupt)", shed.Tenant, shed.Persisted, shed.Accepted))

	// Load floor: gold must apply real load (it is the zero-loss tenant, and a trivial
	// drive proves nothing), and the shed probe must have ATTEMPTED real load
	// (accepted + shed) so its shedding is meaningful rather than a no-op.
	add(InvContentionLoadFloor, gold.Accepted >= cfg.MinAccepted && (shed.Accepted+shed.Shed) >= cfg.MinAccepted,
		fmt.Sprintf("gold accepted %d, shed attempted %d (floor %d each — a gate must apply real load)", gold.Accepted, shed.Accepted+shed.Shed, cfg.MinAccepted))

	// Tenant isolation is covered by the exact per-tenant completeness checks above:
	// each persisted count is read on its OWN tenant-scoped session, and a cross-tenant
	// bleed would make persisted != accepted (an extra event from the other tenant) and
	// fail gold-zero-loss / shed-no-corruption. A separate weak "both reached" invariant
	// would only duplicate that, so it is deliberately omitted rather than shipped soft.

	return inv
}
