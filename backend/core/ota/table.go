// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ota

// rule says what a report at a given stage does to an attempt in a given state. The zero value is
// deliberately not a rule: a missing table entry must be loud, never a quiet default.
type rule int

const (
	_            rule = iota
	ruleProgress      // move to the stage's state (same rank = a progress update); forward jumps allowed
	ruleRegress       // the stage ranks below the current state: rejected
	ruleLate          // nothing but evidence leaves this state, or it is terminal: rejected
	ruleEvidence      // RUNNING: UPDATED iff the confirmation evidence holds
	ruleFail          // FAILED: the device says the attempt failed
	ruleAbandon       // ABANDONED: CANCELLED iff a cancel was requested and rank <= VERIFIED
)

// Row shorthands. Each in-flight state's row is written out in full below so the table reads as
// the contract; TestTransitionTableIsTotal fails if any (state, stage) pair is missing.
var (
	lateRow = map[ReportStage]rule{
		StageReceived: ruleLate, StageDownloading: ruleLate, StageDownloaded: ruleLate,
		StageVerified: ruleLate, StageInstalling: ruleLate, StageRebooting: ruleLate,
		StageRunning: ruleLate, StageFailed: ruleLate, StageAbandoned: ruleLate,
	}
)

// transitions is the rule table, as data. Terminal states reject everything; UNKNOWN accepts only
// evidence (RUNNING, FAILED); in-flight states accept their own stage or later and reject earlier.
var transitions = map[State]map[ReportStage]rule{
	StateQueued: {
		StageReceived: ruleProgress, StageDownloading: ruleProgress, StageDownloaded: ruleProgress,
		StageVerified: ruleProgress, StageInstalling: ruleProgress, StageRebooting: ruleProgress,
		StageRunning: ruleEvidence, StageFailed: ruleFail, StageAbandoned: ruleAbandon,
	},
	StateInitiated: {
		StageReceived: ruleProgress, StageDownloading: ruleProgress, StageDownloaded: ruleProgress,
		StageVerified: ruleProgress, StageInstalling: ruleProgress, StageRebooting: ruleProgress,
		StageRunning: ruleEvidence, StageFailed: ruleFail, StageAbandoned: ruleAbandon,
	},
	StateDownloading: {
		StageReceived: ruleRegress, StageDownloading: ruleProgress, StageDownloaded: ruleProgress,
		StageVerified: ruleProgress, StageInstalling: ruleProgress, StageRebooting: ruleProgress,
		StageRunning: ruleEvidence, StageFailed: ruleFail, StageAbandoned: ruleAbandon,
	},
	StateDownloaded: {
		StageReceived: ruleRegress, StageDownloading: ruleRegress, StageDownloaded: ruleProgress,
		StageVerified: ruleProgress, StageInstalling: ruleProgress, StageRebooting: ruleProgress,
		StageRunning: ruleEvidence, StageFailed: ruleFail, StageAbandoned: ruleAbandon,
	},
	StateVerified: {
		StageReceived: ruleRegress, StageDownloading: ruleRegress, StageDownloaded: ruleRegress,
		StageVerified: ruleProgress, StageInstalling: ruleProgress, StageRebooting: ruleProgress,
		StageRunning: ruleEvidence, StageFailed: ruleFail, StageAbandoned: ruleAbandon,
	},
	StateUpdating: {
		StageReceived: ruleRegress, StageDownloading: ruleRegress, StageDownloaded: ruleRegress,
		StageVerified: ruleRegress, StageInstalling: ruleProgress, StageRebooting: ruleProgress,
		StageRunning: ruleEvidence, StageFailed: ruleFail, StageAbandoned: ruleAbandon,
	},
	StateRebooting: {
		StageReceived: ruleRegress, StageDownloading: ruleRegress, StageDownloaded: ruleRegress,
		StageVerified: ruleRegress, StageInstalling: ruleRegress, StageRebooting: ruleProgress,
		StageRunning: ruleEvidence, StageFailed: ruleFail, StageAbandoned: ruleAbandon,
	},
	// UNKNOWN leaves only on evidence: a RUNNING confirmation or a FAILED for this attempt.
	StateUnknown: {
		StageReceived: ruleLate, StageDownloading: ruleLate, StageDownloaded: ruleLate,
		StageVerified: ruleLate, StageInstalling: ruleLate, StageRebooting: ruleLate,
		StageRunning: ruleEvidence, StageFailed: ruleFail, StageAbandoned: ruleAbandon,
	},
	StateUpdated:   lateRow,
	StateFailed:    lateRow,
	StateTimedOut:  lateRow,
	StateCancelled: lateRow,
}
