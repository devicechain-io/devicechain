// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// ledger issues an admitted dim (lamp-1, lamp-2 at level 40, t0+1h..t0+3h) and a refused dim
// (lamp-9 at level 10, t0+1h..t0+3h), the two entries every reconciliation test starts from.
func ledger(t *testing.T) (*Issuer, *Verifier, []Entry) {
	t.Helper()
	iss, ver := issuerAndVerifier(t)
	admitted := dimProposal()
	ra, err := iss.Issue(t0, admitted, lightingPolicy())
	if err != nil || ra.Verdict != Admitted {
		t.Fatalf("setup admitted: %+v %v", ra, err)
	}
	refused := dimProposal()
	refused.Targets = []string{"lamp-9"}
	refused.Parameters = json.RawMessage(`{"level":10}`)
	rr, err := iss.Issue(t0, refused, lightingPolicy())
	if err != nil || rr.Verdict != Refused {
		t.Fatalf("setup refused: %+v %v", rr, err)
	}
	return iss, ver, []Entry{{ra, admitted}, {rr, refused}}
}

func exec(ref, target string, at time.Time, params string) Execution {
	x := Execution{Ref: ref, Tenant: "city-a", Action: "lighting.dim", Target: target, At: at}
	if params != "" {
		x.Parameters = json.RawMessage(params)
	}
	return x
}

func outcomes(rep Report) map[string]ExecutionFinding {
	out := map[string]ExecutionFinding{}
	for _, f := range rep.Executions {
		out[f.Ref] = f
	}
	return out
}

func TestReconcileClassifiesEveryObservedExecution(t *testing.T) {
	_, ver, entries := ledger(t)
	in := t0.Add(2 * time.Hour)
	execs := []Execution{
		exec("covered", "lamp-1", in, `{"ramp":{"seconds":5},"level":40}`),
		exec("covered-unchecked", "lamp-2", in, ""),
		exec("louder", "lamp-1", in, `{"level":100,"ramp":{"seconds":5}}`),
		exec("late", "lamp-1", t0.Add(5*time.Hour), `{"level":40,"ramp":{"seconds":5}}`),
		exec("told-no", "lamp-9", in, `{"level":10}`),
		exec("stranger", "lamp-7", in, ""),
		exec("garbled", "lamp-1", in, `{"level":40,"level":40}`),
		{Ref: "other-tenant", Tenant: "city-b", Action: "lighting.dim", Target: "lamp-1", At: in},
		{Ref: "other-action", Tenant: "city-a", Action: "lighting.report", Target: "lamp-1", At: in},
	}
	rep, err := ver.Reconcile(entries, execs, ReconcileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Executions) != len(execs) {
		t.Fatalf("%d findings for %d executions", len(rep.Executions), len(execs))
	}
	for i, f := range rep.Executions {
		if f.Ref != execs[i].Ref {
			t.Fatalf("finding %d is %q, want input order (%q)", i, f.Ref, execs[i].Ref)
		}
	}
	admittedID, refusedID := entries[0].Receipt.ID(), entries[1].Receipt.ID()
	want := map[string]ExecutionFinding{
		"covered":           {Ref: "covered", Outcome: Covered, ReceiptID: admittedID, ParametersChecked: true},
		"covered-unchecked": {Ref: "covered-unchecked", Outcome: Covered, ReceiptID: admittedID},
		"louder":            {Ref: "louder", Outcome: ParametersDiffer, ReceiptID: admittedID},
		"late":              {Ref: "late", Outcome: OutsideWindow, ReceiptID: admittedID},
		"told-no":           {Ref: "told-no", Outcome: RefusedBefore, ReceiptID: refusedID},
		"stranger":          {Ref: "stranger", Outcome: Unadmitted},
		"garbled":           {Ref: "garbled", Outcome: MalformedObservation},
		"other-tenant":      {Ref: "other-tenant", Outcome: Unadmitted},
		"other-action":      {Ref: "other-action", Outcome: Unadmitted},
	}
	got := outcomes(rep)
	for ref, w := range want {
		if got[ref] != w {
			t.Errorf("%s: %+v, want %+v", ref, got[ref], w)
		}
	}
	if len(rep.Invalid) != 0 || len(rep.Unobserved) != 0 {
		t.Errorf("unexpected invalid %v or unobserved %v", rep.Invalid, rep.Unobserved)
	}
}

// 🔴 THE NEGATIVE CONTROL. A receipt that does not verify must never cover an execution. The
// same execution is COVERED against the genuine ledger and UNADMITTED against a ledger whose
// only entry is the same receipt with one bit of its signature flipped, a receipt re-signed
// under a key the verifier does not trust, or a genuine receipt shown with a proposal it was
// not issued for.
func TestAReceiptThatDoesNotVerifyCoversNothing(t *testing.T) {
	_, ver, entries := ledger(t)
	x := exec("x", "lamp-1", t0.Add(2*time.Hour), `{"level":40,"ramp":{"seconds":5}}`)

	rep, _ := ver.Reconcile(entries[:1], []Execution{x}, ReconcileOptions{})
	if rep.Executions[0].Outcome != Covered {
		t.Fatalf("control: the genuine receipt does not cover: %+v", rep.Executions[0])
	}

	flipped := entries[0]
	flipped.Receipt.Signature = append([]byte(nil), flipped.Receipt.Signature...)
	flipped.Receipt.Signature[10] ^= 0x01

	_, foreignKey := keys(t)
	forger, _ := NewIssuer("k1", foreignKey) // claims the trusted key id
	forged, _ := forger.Issue(t0, entries[0].Proposal, lightingPolicy())

	swapped := entries[0]
	swapped.Proposal = dimProposal()
	swapped.Proposal.Targets = []string{"lamp-1", "lamp-2", "lamp-3"}

	for name, bad := range map[string]Entry{
		"flipped signature": flipped,
		"foreign key":       {forged, entries[0].Proposal},
		"swapped proposal":  swapped,
	} {
		rep, err := ver.Reconcile([]Entry{bad}, []Execution{x}, ReconcileOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if rep.Executions[0].Outcome != Unadmitted {
			t.Errorf("%s: %+v, want UNADMITTED", name, rep.Executions[0])
		}
		if len(rep.Invalid) != 1 || rep.Invalid[0].Index != 0 {
			t.Errorf("%s: invalid = %+v, want entry 0 listed", name, rep.Invalid)
		}
	}
	if rep, _ := ver.Reconcile([]Entry{flipped}, nil, ReconcileOptions{}); !errors.Is(rep.Invalid[0].Err, ErrBadSignature) {
		t.Errorf("flipped signature reported as %v", rep.Invalid[0].Err)
	}
	if rep, _ := ver.Reconcile([]Entry{swapped}, nil, ReconcileOptions{}); !errors.Is(rep.Invalid[0].Err, ErrProposalMismatch) {
		t.Errorf("swapped proposal reported as %v", rep.Invalid[0].Err)
	}
}

func TestToleranceWidensTheWindowOnBothSides(t *testing.T) {
	_, ver, entries := ledger(t)
	early := exec("early", "lamp-1", t0.Add(time.Hour-30*time.Second), "")
	late := exec("late", "lamp-1", t0.Add(3*time.Hour+30*time.Second), "")
	edge := exec("edge", "lamp-1", t0.Add(3*time.Hour+time.Minute), "")

	strict, _ := ver.Reconcile(entries, []Execution{early, late}, ReconcileOptions{})
	for _, f := range strict.Executions {
		if f.Outcome != OutsideWindow {
			t.Errorf("no tolerance: %s = %s, want OUTSIDE_WINDOW", f.Ref, f.Outcome)
		}
	}
	loose, _ := ver.Reconcile(entries, []Execution{early, late, edge}, ReconcileOptions{Tolerance: time.Minute})
	got := outcomes(loose)
	if got["early"].Outcome != Covered || got["late"].Outcome != Covered {
		t.Errorf("one-minute tolerance: early %s, late %s, want COVERED", got["early"].Outcome, got["late"].Outcome)
	}
	if got["edge"].Outcome != OutsideWindow {
		t.Errorf("exactly NotAfter+tolerance is outside (half-open): %s", got["edge"].Outcome)
	}
	if _, err := ver.Reconcile(entries, nil, ReconcileOptions{Tolerance: -1}); err == nil {
		t.Error("negative tolerance accepted")
	}
}

func TestARefusalForAnotherTimeExplainsNothing(t *testing.T) {
	_, ver, entries := ledger(t)
	rep, _ := ver.Reconcile(entries, []Execution{exec("x", "lamp-9", t0.Add(10*time.Hour), "")}, ReconcileOptions{})
	if rep.Executions[0].Outcome != Unadmitted || rep.Executions[0].ReceiptID != "" {
		t.Fatalf("%+v, want UNADMITTED with no receipt", rep.Executions[0])
	}
}

func TestTheMostSpecificNearMissWins(t *testing.T) {
	iss, ver, entries := ledger(t)
	// A second admitted receipt for lamp-9 at another time, and a third for lamp-9 in the
	// refused window but with other parameters: the refusal is still the explanation.
	later := dimProposal()
	later.Targets = []string{"lamp-9"}
	later.NotBefore, later.NotAfter = t0.Add(5*time.Hour), t0.Add(6*time.Hour)
	rl, _ := iss.Issue(t0, later, lightingPolicy())
	other := dimProposal()
	other.Targets = []string{"lamp-9"}
	other.Parameters = json.RawMessage(`{"level":50}`)
	ro, _ := iss.Issue(t0, other, lightingPolicy())
	all := append([]Entry{{rl, later}, {ro, other}}, entries...)
	x := exec("x", "lamp-9", t0.Add(2*time.Hour), `{"level":10}`)
	rep, _ := ver.Reconcile(all, []Execution{x}, ReconcileOptions{})
	if f := rep.Executions[0]; f.Outcome != RefusedBefore || f.ReceiptID != entries[1].Receipt.ID() {
		t.Fatalf("%+v, want REFUSED_BEFORE on the refusal", f)
	}
	// Without the refusal, parameters-differ beats outside-window.
	rep, _ = ver.Reconcile(all[:2], []Execution{x}, ReconcileOptions{})
	if f := rep.Executions[0]; f.Outcome != ParametersDiffer || f.ReceiptID != ro.ID() {
		t.Fatalf("%+v, want PARAMETERS_DIFFER on the in-window receipt", f)
	}
	// And outside-window beats nothing at all.
	rep, _ = ver.Reconcile(all[:1], []Execution{x}, ReconcileOptions{})
	if f := rep.Executions[0]; f.Outcome != OutsideWindow || f.ReceiptID != rl.ID() {
		t.Fatalf("%+v, want OUTSIDE_WINDOW", f)
	}
}

func TestUnobservedTargetsAreListedOnlyAfterTheWindowCloses(t *testing.T) {
	_, ver, entries := ledger(t)
	x := exec("x", "lamp-1", t0.Add(2*time.Hour), "")
	// Still open (with tolerance): nothing listed.
	rep, _ := ver.Reconcile(entries, []Execution{x}, ReconcileOptions{AsOf: t0.Add(3 * time.Hour), Tolerance: time.Second})
	if len(rep.Unobserved) != 0 {
		t.Fatalf("listed before the window closed: %+v", rep.Unobserved)
	}
	rep, _ = ver.Reconcile(entries, []Execution{x}, ReconcileOptions{AsOf: t0.Add(3*time.Hour + time.Second), Tolerance: time.Second})
	want := Unobserved{ReceiptID: entries[0].Receipt.ID(), Target: "lamp-2"}
	if len(rep.Unobserved) != 1 || rep.Unobserved[0] != want {
		t.Fatalf("unobserved = %+v, want only %+v (lamp-1 was seen; the refusal lists nothing)", rep.Unobserved, want)
	}
	// An execution that is NOT covered (other parameters) does not count as observed.
	louder := exec("louder", "lamp-1", t0.Add(2*time.Hour), `{"level":100}`)
	rep, _ = ver.Reconcile(entries, []Execution{louder}, ReconcileOptions{AsOf: t0.Add(4 * time.Hour)})
	if len(rep.Unobserved) != 2 {
		t.Fatalf("unobserved = %+v, want both targets", rep.Unobserved)
	}
}

func TestReconcileWithOnlyForeignKeysTrustsNothing(t *testing.T) {
	_, _, entries := ledger(t)
	pub, _ := keys(t)
	stranger, _ := NewVerifier(map[string]ed25519.PublicKey{"k1": pub})
	rep, _ := stranger.Reconcile(entries, []Execution{exec("x", "lamp-1", t0.Add(2*time.Hour), "")}, ReconcileOptions{})
	if rep.Executions[0].Outcome != Unadmitted || len(rep.Invalid) != 2 {
		t.Fatalf("%+v", rep)
	}
}

func TestAmongEqualNearMissesTheFirstInLedgerOrderIsNamed(t *testing.T) {
	iss, ver, _ := ledger(t)
	var entries []Entry
	for _, h := range []time.Duration{5, 7} {
		p := dimProposal()
		p.NotBefore, p.NotAfter = t0.Add(h*time.Hour), t0.Add((h+1)*time.Hour)
		r, err := iss.Issue(t0, p, lightingPolicy())
		if err != nil || r.Verdict != Admitted {
			t.Fatalf("setup: %+v %v", r, err)
		}
		entries = append(entries, Entry{r, p})
	}
	rep, _ := ver.Reconcile(entries, []Execution{exec("x", "lamp-1", t0.Add(10*time.Hour), "")}, ReconcileOptions{})
	if f := rep.Executions[0]; f.Outcome != OutsideWindow || f.ReceiptID != entries[0].Receipt.ID() {
		t.Fatalf("%+v, want OUTSIDE_WINDOW naming the first entry", f)
	}
}

func TestAReceiptDoesNotReachBackBeforeItWasIssued(t *testing.T) {
	iss, ver := issuerAndVerifier(t)
	open := dimProposal()
	open.Action = "lighting.report"
	open.Parameters = nil
	open.NotBefore, open.NotAfter = t0, t0.Add(time.Hour)
	issued := t0.Add(30 * time.Minute)
	r, err := iss.Issue(issued, open, lightingPolicy())
	if err != nil || r.Verdict != Admitted {
		t.Fatalf("setup: %+v %v", r, err)
	}
	x := func(ref string, at time.Time) Execution {
		return Execution{Ref: ref, Tenant: "city-a", Action: "lighting.report", Target: "lamp-1", At: at}
	}
	rep, _ := ver.Reconcile([]Entry{{r, open}}, []Execution{
		x("before", issued.Add(-time.Second)), x("at", issued),
	}, ReconcileOptions{Tolerance: time.Minute})
	got := outcomes(rep)
	if got["before"].Outcome != BeforeReceipt || got["at"].Outcome != Covered {
		t.Fatalf("before=%s at=%s, want BEFORE_RECEIPT and COVERED", got["before"].Outcome, got["at"].Outcome)
	}

	// A refusal issued after the execution says nothing about it.
	refused := dimProposal()
	refused.Targets = []string{"lamp-9"}
	refused.Parameters = json.RawMessage(`{"level":10}`)
	rr, err := iss.Issue(t0.Add(2*time.Hour), refused, lightingPolicy())
	if err != nil || rr.Verdict != Refused {
		t.Fatalf("setup: %+v %v", rr, err)
	}
	rep, _ = ver.Reconcile([]Entry{{rr, refused}}, []Execution{
		exec("earlier", "lamp-9", t0.Add(90*time.Minute), `{"level":10}`),
		exec("later", "lamp-9", t0.Add(150*time.Minute), `{"level":10}`),
	}, ReconcileOptions{})
	got = outcomes(rep)
	if got["earlier"].Outcome != Unadmitted || got["later"].Outcome != RefusedBefore {
		t.Fatalf("earlier=%s later=%s, want UNADMITTED and REFUSED_BEFORE", got["earlier"].Outcome, got["later"].Outcome)
	}
}

func TestARefusalForOtherParametersExplainsNothing(t *testing.T) {
	_, ver, entries := ledger(t)
	rep, _ := ver.Reconcile(entries, []Execution{exec("x", "lamp-9", t0.Add(2*time.Hour), `{"level":80}`)}, ReconcileOptions{})
	if f := rep.Executions[0]; f.Outcome != Unadmitted {
		t.Fatalf("%+v, want UNADMITTED: level 10 was refused, level 80 was never asked", f)
	}
	rep, _ = ver.Reconcile(entries, []Execution{exec("x", "lamp-9", t0.Add(2*time.Hour), "")}, ReconcileOptions{})
	if f := rep.Executions[0]; f.Outcome != RefusedBefore {
		t.Fatalf("%+v, want REFUSED_BEFORE when the parameters were not observed", f)
	}
}

func TestEveryCoveringReceiptCountsAsObserved(t *testing.T) {
	_, ver, entries := ledger(t)
	dup := append([]Entry{entries[0]}, entries...)
	rep, _ := ver.Reconcile(dup, []Execution{
		exec("a", "lamp-1", t0.Add(2*time.Hour), ""), exec("b", "lamp-2", t0.Add(2*time.Hour), ""),
	}, ReconcileOptions{AsOf: t0.Add(5 * time.Hour)})
	if len(rep.Unobserved) != 0 {
		t.Fatalf("a covered target listed as unobserved: %+v", rep.Unobserved)
	}
}
