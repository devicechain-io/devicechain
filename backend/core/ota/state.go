// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ota

// State is the platform's view of one update attempt. Names marked "ADR-012" are the accepted
// vocabulary; the four marked "proposed" are an amendment awaiting maintainer confirmation.
type State string

const (
	StateQueued      State = "QUEUED"      // ADR-012
	StateInitiated   State = "INITIATED"   // ADR-012 (device stage: RECEIVED)
	StateDownloading State = "DOWNLOADING" // ADR-012
	StateDownloaded  State = "DOWNLOADED"  // ADR-012
	StateVerified    State = "VERIFIED"    // ADR-012 — integrity only (digest matched)
	StateUpdating    State = "UPDATING"    // ADR-012 (device stage: INSTALLING)
	StateRebooting   State = "REBOOTING"   // proposed addition
	StateUpdated     State = "UPDATED"     // ADR-012 — only on confirmation evidence
	StateFailed      State = "FAILED"      // ADR-012
	StateTimedOut    State = "TIMED_OUT"   // proposed addition, terminal
	StateUnknown     State = "UNKNOWN"     // proposed addition, awaiting reconciliation
	StateCancelled   State = "CANCELLED"   // proposed addition, terminal
)

// allStates is every declared State; the table-totality test iterates it.
var allStates = []State{
	StateQueued, StateInitiated, StateDownloading, StateDownloaded, StateVerified, StateUpdating,
	StateRebooting, StateUpdated, StateFailed, StateTimedOut, StateUnknown, StateCancelled,
}

// Valid reports whether s is a declared state.
func (s State) Valid() bool {
	return false
}

// Terminal reports whether no further progress can apply: UPDATED, FAILED, TIMED_OUT, CANCELLED.
// UNKNOWN is deliberately NOT terminal — it waits for evidence.
func (s State) Terminal() bool {
	return false
}

// ReportStage is what the device says it is doing, in its own words. The platform maps stages to
// States; the two vocabularies are kept apart on purpose ("INSTALLING" is the device's verb,
// "UPDATING" the platform's name).
type ReportStage string

const (
	StageReceived    ReportStage = "RECEIVED"
	StageDownloading ReportStage = "DOWNLOADING"
	StageDownloaded  ReportStage = "DOWNLOADED"
	StageVerified    ReportStage = "VERIFIED"
	StageInstalling  ReportStage = "INSTALLING"
	StageRebooting   ReportStage = "REBOOTING"
	StageRunning     ReportStage = "RUNNING"
	StageFailed      ReportStage = "FAILED"
	StageAbandoned   ReportStage = "ABANDONED"
)

// allStages is every declared ReportStage; the table-totality test iterates it.
var allStages = []ReportStage{
	StageReceived, StageDownloading, StageDownloaded, StageVerified, StageInstalling, StageRebooting,
	StageRunning, StageFailed, StageAbandoned,
}

// Valid reports whether s is a declared stage.
func (s ReportStage) Valid() bool {
	return false
}

// rank orders the in-flight states; ok is false for states outside that ladder.
func rank(s State) (r int, ok bool) {
	return 0, false
}

// stateForStage maps a progress stage to the state it moves the attempt to; ok is false for the
// non-progress stages (RUNNING, FAILED, ABANDONED), which have their own rules.
func stateForStage(s ReportStage) (State, bool) {
	return "", false
}

// stageForState is the inverse of stateForStage, used to say which stage a failure happened in.
// QUEUED and UNKNOWN have no device stage and return "".
func stageForState(s State) ReportStage {
	return ""
}
