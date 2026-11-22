// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ledger

// Dispatch is an operation's dispatch phase. Reserved values are not valid yet.
type Dispatch string

const (
	DispatchPlanning   Dispatch = "PLANNING"
	DispatchReady      Dispatch = "READY"
	DispatchRunning    Dispatch = "RUNNING"
	DispatchPaused     Dispatch = "PAUSED"
	DispatchDispatched Dispatch = "DISPATCHED"
	DispatchAborted    Dispatch = "ABORTED"
	DispatchDraft      Dispatch = "DRAFT" // Reserved for a later release.
)

func (v Dispatch) Valid() bool {
	switch v {
	case DispatchPlanning, DispatchReady, DispatchRunning, DispatchPaused, DispatchDispatched, DispatchAborted:
		return true
	default:
		return false
	}
}

// Evidence is the operation's evidence collection phase.
type Evidence string

const (
	EvidenceOpen   Evidence = "OPEN"
	EvidenceClosed Evidence = "CLOSED"
)

func (v Evidence) Valid() bool { return v == EvidenceOpen || v == EvidenceClosed }

// Posting is a line's posting phase. Reserved values are not valid yet.
type Posting string

const (
	PostingPlanned     Posting = "PLANNED"
	PostingPosted      Posting = "POSTED"
	PostingRefused     Posting = "REFUSED"
	PostingSkipped     Posting = "SKIPPED"
	PostingCancelled   Posting = "CANCELLED"
	PostingDispatching Posting = "DISPATCHING"     // Reserved for a later release.
	PostingUnknown     Posting = "POSTING_UNKNOWN" // Reserved for a later release.
)

func (v Posting) Valid() bool {
	switch v {
	case PostingPlanned, PostingPosted, PostingRefused, PostingSkipped, PostingCancelled:
		return true
	default:
		return false
	}
}

// Settlement is a verdict on a posted line. Absence is represented by nil.
type Settlement string

const (
	SettlementConfirmed         Settlement = "CONFIRMED"
	SettlementFailed            Settlement = "FAILED"
	SettlementSuperseded        Settlement = "SUPERSEDED"
	SettlementAmbiguous         Settlement = "AMBIGUOUS"
	SettlementResolvedConfirmed Settlement = "RESOLVED_CONFIRMED"
	SettlementResolvedFailed    Settlement = "RESOLVED_FAILED"
)

func (v Settlement) Valid() bool {
	switch v {
	case SettlementConfirmed, SettlementFailed, SettlementSuperseded, SettlementAmbiguous, SettlementResolvedConfirmed, SettlementResolvedFailed:
		return true
	default:
		return false
	}
}

type lineState struct {
	posting    Posting
	settlement Settlement // Empty is the internal table key for nil, never a valid verdict.
}

var legalLineStates = map[lineState]bool{
	{PostingPlanned, ""}:                         true,
	{PostingPosted, ""}:                          true,
	{PostingRefused, ""}:                         true,
	{PostingSkipped, ""}:                         true,
	{PostingCancelled, ""}:                       true,
	{PostingPosted, SettlementConfirmed}:         true,
	{PostingPosted, SettlementFailed}:            true,
	{PostingPosted, SettlementSuperseded}:        true,
	{PostingPosted, SettlementAmbiguous}:         true,
	{PostingPosted, SettlementResolvedConfirmed}: true,
	{PostingPosted, SettlementResolvedFailed}:    true,
}

// LegalLineState accepts only listed pairs; settlement is possible only on POSTED.
func LegalLineState(posting Posting, settlement *Settlement) bool {
	if settlement == nil {
		return legalLineStates[lineState{posting, ""}]
	}
	if !settlement.Valid() {
		return false
	}
	return legalLineStates[lineState{posting, *settlement}]
}

var postingTransitions = map[[2]Posting]bool{
	{PostingPlanned, PostingPosted}:    true,
	{PostingPlanned, PostingRefused}:   true,
	{PostingPlanned, PostingSkipped}:   true,
	{PostingPlanned, PostingCancelled}: true,
}

// LegalPostingTransition refuses all unlisted pairs, including self transitions.
func LegalPostingTransition(from, to Posting) bool { return postingTransitions[[2]Posting{from, to}] }

var settlementTransitions = map[[2]Settlement]bool{
	{"", SettlementConfirmed}:                          true,
	{"", SettlementFailed}:                             true,
	{"", SettlementSuperseded}:                         true,
	{"", SettlementAmbiguous}:                          true,
	{SettlementAmbiguous, SettlementConfirmed}:         true,
	{SettlementAmbiguous, SettlementFailed}:            true,
	{SettlementAmbiguous, SettlementSuperseded}:        true,
	{SettlementAmbiguous, SettlementResolvedConfirmed}: true,
	{SettlementAmbiguous, SettlementResolvedFailed}:    true,
}

// LegalSettlementTransition includes the initial nil verdict. Terminal verdicts
// never flip, and no transition clears a verdict back to nil.
func LegalSettlementTransition(from, to *Settlement) bool {
	if to == nil || !to.Valid() || (from != nil && !from.Valid()) {
		return false
	}
	var source Settlement
	if from != nil {
		source = *from
	}
	return settlementTransitions[[2]Settlement{source, *to}]
}

var dispatchTransitions = map[[2]Dispatch]bool{
	{DispatchPlanning, DispatchReady}:     true,
	{DispatchPlanning, DispatchAborted}:   true,
	{DispatchReady, DispatchRunning}:      true,
	{DispatchReady, DispatchAborted}:      true,
	{DispatchRunning, DispatchPaused}:     true,
	{DispatchRunning, DispatchDispatched}: true,
	{DispatchRunning, DispatchAborted}:    true,
	{DispatchPaused, DispatchRunning}:     true,
	{DispatchPaused, DispatchAborted}:     true,
}

// LegalDispatchTransition refuses every pair absent from the transition table.
func LegalDispatchTransition(from, to Dispatch) bool {
	return dispatchTransitions[[2]Dispatch{from, to}]
}
