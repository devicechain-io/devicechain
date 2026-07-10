// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "context"

// RuleToValidate is one draft detection rule handed to the validation gate: its token
// (for error anchoring) and its opaque rules.Rule JSON definition.
type RuleToValidate struct {
	Token      string
	Definition string
}

// DetectionRuleValidationFailure names one rule event-processing rejected at compile,
// with a console-surfaceable reason. It is a rule that decoded and was found invalid —
// NOT a transport failure (that is the error return).
type DetectionRuleValidationFailure struct {
	Token   string
	Message string
}

// DetectionRuleValidator compiles a profile's draft detection rules against
// event-processing (the single-homed owner of the DETECT schema + cel-go compiler) at
// publish, so a profile carrying an uncompilable rule fails closed (ADR-044
// validate-invariants-sync; ADR-051 slice 4b). It is the reverse-direction sibling of
// command-delivery's device check, and is dependency-inverted the same way: the model
// depends only on this narrow interface, never on the svcclient machinery (which the
// ruleverify package holds). A nil validator (service secret unconfigured) skips the
// gate, preserving publish-anything behavior; device-management logs the disabled mode at
// startup.
//
// The two failure kinds are kept distinct on purpose. The returned slice is the per-rule
// REJECTIONS — rules that compiled-and-failed — whose reasons must reach the author. A
// non-nil error is a TRANSPORT/availability failure (the check could not be performed);
// the publish still fails closed on it, but the caller sanitizes it so the tenant API
// client does not learn the in-cluster topology.
type DetectionRuleValidator interface {
	ValidateDetectionRules(ctx context.Context, rules []RuleToValidate) ([]DetectionRuleValidationFailure, error)
}
