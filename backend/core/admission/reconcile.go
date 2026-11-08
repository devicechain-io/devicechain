// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

// Entry is one issued receipt together with the proposal it answered. The ledger keeps the
// proposal because a receipt carries only its digest, and coverage needs the targets and the
// parameters.
type Entry struct {
	Receipt  Receipt
	Proposal Proposal
}

// Execution is one action the head-end was OBSERVED to perform, typically derived from the
// telemetry a read-only pilot already receives.
type Execution struct {
	// Ref identifies the observation (an event id), so a finding can be traced back.
	Ref    string
	Tenant string
	Action string
	Target string
	At     time.Time
	// Parameters are the observed arguments. Nil means they were not observed, and coverage
	// is then judged without them (the finding says so).
	Parameters json.RawMessage
}

// Outcome classifies one observed execution. See the package doc.
type Outcome string

const (
	Covered              Outcome = "COVERED"
	RefusedBefore        Outcome = "REFUSED_BEFORE"
	ParametersDiffer     Outcome = "PARAMETERS_DIFFER"
	OutsideWindow        Outcome = "OUTSIDE_WINDOW"
	BeforeReceipt        Outcome = "BEFORE_RECEIPT"
	Unadmitted           Outcome = "UNADMITTED"
	MalformedObservation Outcome = "MALFORMED_OBSERVATION"
)

// ExecutionFinding is the verdict on one observed execution.
type ExecutionFinding struct {
	Ref     string
	Outcome Outcome
	// ReceiptID names the receipt the finding rests on: the covering one, or the near miss
	// that explains it. Empty for UNADMITTED and MALFORMED_OBSERVATION.
	ReceiptID string
	// ParametersChecked is false when the execution was covered without its parameters
	// having been observed: the action was admitted, its arguments were not confirmed.
	ParametersChecked bool
}

// InvalidEntry is a ledger entry that was set aside because its receipt is not genuine or does
// not match its proposal. It covers nothing.
type InvalidEntry struct {
	Index int
	Err   error
}

// Unobserved is one target of an admitted receipt, whose window has closed, on which no covered
// execution was seen. It is not an accusation: the head-end may have decided not to act.
type Unobserved struct {
	ReceiptID string
	Target    string
}

// Report is the result of a reconciliation. Executions are in input order.
type Report struct {
	Executions []ExecutionFinding
	Invalid    []InvalidEntry
	Unobserved []Unobserved
}

// ReconcileOptions tunes a reconciliation.
type ReconcileOptions struct {
	// Tolerance widens every window on both sides, for the lag between an action and its
	// observation. Negative is refused.
	Tolerance time.Duration
	// AsOf, when set, also lists admitted targets never observed in a window that closed
	// (with tolerance) at or before it. Zero skips that listing.
	AsOf time.Time
}

type validEntry struct {
	id      string
	r       Receipt
	targets map[string]bool
	params  []byte
}

// near-miss ranking: a higher rank is the more specific explanation. Among equal ranks the
// first entry in ledger order is named, so a report is deterministic for a given ledger.
var missRank = map[Outcome]int{Unadmitted: 0, OutsideWindow: 1, BeforeReceipt: 2, ParametersDiffer: 3, RefusedBefore: 4}

// Reconcile holds every observed execution against the ledger. Entries that do not verify are
// set aside and listed, never used.
func (v *Verifier) Reconcile(entries []Entry, execs []Execution, opt ReconcileOptions) (Report, error) {
	if opt.Tolerance < 0 {
		return Report{}, errors.New("admission: tolerance is negative")
	}
	var rep Report
	valid := make([]validEntry, 0, len(entries))
	for i, e := range entries {
		if err := v.Verify(e.Receipt, e.Proposal); err != nil {
			rep.Invalid = append(rep.Invalid, InvalidEntry{Index: i, Err: err})
			continue
		}
		c, err := e.Proposal.canonical() // cannot fail: Verify digested it
		if err != nil {
			rep.Invalid = append(rep.Invalid, InvalidEntry{Index: i, Err: err})
			continue
		}
		ts := make(map[string]bool, len(c.Targets))
		for _, t := range c.Targets {
			ts[t] = true
		}
		valid = append(valid, validEntry{id: e.Receipt.ID(), r: e.Receipt, targets: ts, params: c.Parameters})
	}

	type key struct {
		entry  int
		target string
	}
	observed := map[key]bool{}

	for _, x := range execs {
		f := ExecutionFinding{Ref: x.Ref}
		var observedParams []byte
		if x.Parameters != nil {
			cp, err := canonicalJSON(x.Parameters)
			if err != nil {
				f.Outcome = MalformedObservation
				rep.Executions = append(rep.Executions, f)
				continue
			}
			observedParams = cp
		}
		best, bestID := Unadmitted, ""
		covered := -1
		for i, e := range valid {
			if e.r.Tenant != x.Tenant || e.r.Action != x.Action || !e.targets[x.Target] {
				continue
			}
			inWindow := !x.At.Before(e.r.NotBefore.Add(-opt.Tolerance)) &&
				x.At.Before(e.r.NotAfter.Add(opt.Tolerance))
			paramsMatch := observedParams == nil || bytes.Equal(observedParams, e.params)
			// A receipt speaks only for what was observed at or after it was issued. The
			// tolerance does not reach back across the issue time: an observation stamped
			// before the receipt existed cannot have been acting on it.
			afterIssue := !x.At.Before(e.r.IssuedAt)
			var miss Outcome
			switch {
			case e.r.Verdict != Admitted:
				if !inWindow || !afterIssue || !paramsMatch {
					// A refusal for another time, another argument, or one given only
					// after the fact explains nothing about this execution.
					continue
				}
				miss = RefusedBefore
			case !inWindow:
				miss = OutsideWindow
			case !afterIssue:
				miss = BeforeReceipt
			case !paramsMatch:
				miss = ParametersDiffer
			default:
				// Every covering entry counts as observed; the finding names the first.
				observed[key{i, x.Target}] = true
				if covered < 0 {
					covered = i
				}
				continue
			}
			if missRank[miss] > missRank[best] {
				best, bestID = miss, e.id
			}
		}
		if covered >= 0 {
			f.Outcome, f.ReceiptID, f.ParametersChecked = Covered, valid[covered].id, observedParams != nil
		} else {
			f.Outcome, f.ReceiptID = best, bestID
		}
		rep.Executions = append(rep.Executions, f)
	}

	if !opt.AsOf.IsZero() {
		for i, e := range valid {
			if e.r.Verdict != Admitted || e.r.NotAfter.Add(opt.Tolerance).After(opt.AsOf) {
				continue
			}
			for _, t := range sortedKeys(e.targets) {
				if !observed[key{i, t}] {
					rep.Unobserved = append(rep.Unobserved, Unobserved{ReceiptID: e.id, Target: t})
				}
			}
		}
	}
	return rep, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
