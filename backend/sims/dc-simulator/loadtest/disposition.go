// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"fmt"
	"sort"
	"strings"
)

// Shared verdict machinery for the harnesses that have something to say beyond
// pass/fail, and for the negative controls that keep them honest.
//
// 🔴 THERE ARE THREE DISPOSITIONS, NOT TWO, AND THE THIRD IS THE ONE THAT KEEPS THE
// OTHER TWO MEANINGFUL. A gate that can only say PASS or FAIL has to report an
// environment it could not measure in as one of them, and whichever it picks is
// wrong: a false green certifies nothing, and a false red sends someone hunting a
// defect that is not there. Both are expensive at a release gate; the second is the
// one this project has already paid for.

// Disposition is a run's verdict.
type Disposition string

const (
	DispositionPass Disposition = "PASS"
	DispositionFail Disposition = "FAIL"
	// DispositionInconclusive means the run could not measure what it claims to
	// measure. It is NOT a soft failure and NOT a soft pass — it is the statement that
	// this run has no opinion, and it carries its own exit code so a caller cannot
	// mistake it for either.
	DispositionInconclusive Disposition = "INCONCLUSIVE"
)

// measurability accumulates the reasons a run could not measure what it set out to.
// It is a value rather than a boolean so the report can say WHICH condition fired —
// an operator reading a non-zero exit needs to know whether to fix a cluster or hunt
// a defect, and "inconclusive" alone answers neither.
type measurability struct {
	reasons []string
}

// cannot records one reason this run has no opinion.
func (m *measurability) cannot(format string, args ...any) {
	m.reasons = append(m.reasons, fmt.Sprintf(format, args...))
}

func (m *measurability) blocked() bool { return len(m.reasons) > 0 }

func (m *measurability) reason() string { return strings.Join(m.reasons, "; ") }

// requireInvariantSet reports whether the invariants a classifier produced are
// EXACTLY the set it declares it produces.
//
// 🔴 THIS IS THE BACKSTOP UNDER EVERY OTHER CHECK HERE, AND ITS ABSENCE WAS A REAL
// HOLE. Both a plain verdict and a negative control read the invariants that ARE in
// the report: a plain run fails only if one of them failed, and a control compares
// only the names it expects to flip. An invariant that stopped being appended at all
// — a guard that is false at runtime, a merge that dropped an append — is therefore
// invisible to both. A gate could ship asserting two of its six properties, print
// PASS, and have its own negative control confirm it was working.
//
// The declared set is a SPECIFICATION written beside the classifier, not read out of
// it: a check that derived its expectation from the same list it is checking would
// agree with itself no matter what the list said.
func requireInvariantSet(declared []string, invs []Invariant) error {
	want := make(map[string]bool, len(declared))
	for _, n := range declared {
		want[n] = true
	}
	got := make(map[string]bool, len(invs))
	var duplicated []string
	for _, inv := range invs {
		if got[inv.Name] {
			duplicated = append(duplicated, inv.Name)
		}
		got[inv.Name] = true
	}
	var missing, unexpected []string
	for _, n := range declared {
		if !got[n] {
			missing = append(missing, n)
		}
	}
	for n := range got {
		if !want[n] {
			unexpected = append(unexpected, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	sort.Strings(duplicated)
	if len(missing) == 0 && len(unexpected) == 0 && len(duplicated) == 0 {
		return nil
	}
	return fmt.Errorf("this run evaluated %d invariant(s) but the harness declares %d — not evaluated: [%s]; unrecognised: [%s]; duplicated: [%s]. A verdict over a partial set is not the verdict this gate promises",
		len(invs), len(declared), strings.Join(missing, ", "), strings.Join(unexpected, ", "), strings.Join(duplicated, ", "))
}

// decideDisposition folds a run's observations into its verdict. Pure, and
// deliberately so: the INCONCLUSIVE branches are the ones a live run reaches least
// often and can least afford to get wrong, so they are decided by a function a unit
// test can drive into every branch without a cluster.
//
// The ORDER is the substance. Unmeasurability outranks a failed invariant, because
// every condition that records one is a reason an invariant would fail for a cause
// that is not the platform. Reporting FAIL in that case names the platform for the
// environment's problem — which is the more expensive of the two mistakes, since a
// false red at a release gate costs a day of hunting and a false green costs one
// release.
func decideDisposition(m measurability, declared []string, invs []Invariant) (Disposition, string) {
	if m.blocked() {
		return DispositionInconclusive, m.reason()
	}
	// An empty invariant set is not a pass, and neither is a partial one.
	if err := requireInvariantSet(declared, invs); err != nil {
		return DispositionFail, err.Error()
	}
	var failed []string
	for _, inv := range invs {
		if !inv.Passed {
			failed = append(failed, inv.Name)
		}
	}
	if len(failed) > 0 {
		return DispositionFail, "failed: " + strings.Join(failed, ", ")
	}
	return DispositionPass, "every invariant held"
}

// evaluateExpectedFailureSet is the shared control judgement, used by every harness
// that ships a negative control. Pure.
//
// It demands two things, and the first was missing for long enough to be worth
// stating: the report must contain EXACTLY the declared invariant set (see
// requireInvariantSet — otherwise a control certifies an oracle that has quietly
// stopped asserting most of what it claims), and within that set the expected
// invariants must be red and every other one green.
//
// Demanding "at least" the expected failures would be satisfied by an oracle that
// failed everything — the very breakage a control exists to detect — and demanding
// "only" them would fail a healthy oracle whose other invariants legitimately flip as
// a consequence of the same perturbation. Neither weaker reading discriminates.
func evaluateExpectedFailureSet(control string, declared, want []string, known bool, invs []Invariant) (satisfied bool, detail string) {
	if !known {
		return false, fmt.Sprintf("no expected failure set is declared for control %q", control)
	}
	if err := requireInvariantSet(declared, invs); err != nil {
		return false, fmt.Sprintf("control %q cannot be judged: %s", control, err.Error())
	}
	wantSet := make(map[string]bool, len(want))
	for _, n := range want {
		wantSet[n] = true
	}
	var missing, unexpected []string
	for _, inv := range invs {
		switch {
		case wantSet[inv.Name] && inv.Passed:
			missing = append(missing, inv.Name)
		case !wantSet[inv.Name] && !inv.Passed:
			unexpected = append(unexpected, inv.Name)
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	if len(missing) == 0 && len(unexpected) == 0 {
		return true, fmt.Sprintf("the control flipped exactly its expected set {%s}", strings.Join(want, ", "))
	}
	return false, fmt.Sprintf("control %q expected exactly {%s} to fail; still passing: [%s]; unexpectedly failing: [%s]",
		control, strings.Join(want, ", "), strings.Join(missing, ", "), strings.Join(unexpected, ", "))
}
