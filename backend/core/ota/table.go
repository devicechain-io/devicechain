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

// transitions is the rule table (stub: empty).
var transitions = map[State]map[ReportStage]rule{}
