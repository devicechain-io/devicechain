// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-microservice/auth"
)

// detectionRuleInput is one rule submitted to the validation gate: its authoring token
// (for error anchoring) and the opaque rules.Rule JSON to compile.
type detectionRuleInput struct {
	Token      string
	Definition string
}

// ValidateDetectionRules compiles and cost-gates a batch of detection-rule definitions —
// the ADR-044 synchronous validation gate device-management calls at profile publish
// (ADR-051 slice 4b) so a profile carrying an uncompilable rule fails closed. It is the
// single-homed authority for rule structure, type coherence, CEL type-checking, and the
// predicate-cost ceiling: device-management stores the rule blob opaquely and never parses
// the taxonomy, so this is where a bad rule is actually caught.
//
// It gates on device:read — the least-privilege authority for a read-only check over the
// device/profile aggregate that rules belong to (the caller mints a service token carrying
// only it). The compile is pure: no state is read or written, so a hostile caller can gain
// nothing beyond a CEL-compilation oracle, which the cost gate and auth already bound.
func (r *SchemaResolver) ValidateDetectionRules(ctx context.Context, args struct {
	Rules []detectionRuleInput
}) (*DetectionRuleValidationResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	// Platform-default compile limits, shared verbatim with the runtime fact-consumer
	// (runtime.CompilePublishedRules, slice 4b-3) via rules.DefaultLimits so a rule that
	// passes this publish gate compiles identically when the engine consumes it — see the
	// doc on DefaultLimits.
	limits := rules.DefaultLimits()

	errs := make([]*DetectionRuleValidationErrorResolver, 0)
	for i, in := range args.Rules {
		rule, err := rules.Decode([]byte(in.Definition))
		if err != nil {
			errs = append(errs, newValidationError(i, in.Token, err))
			continue
		}
		// The stored blob carries no runtime id (that is composed at fact-emit time), so
		// force the token as the compile-time id: Compile requires a non-empty id and uses
		// it only to anchor its error messages, so this yields token-named errors without
		// affecting what is validated.
		rule.ID = in.Token
		if _, err := rules.Compile(rule, limits); err != nil {
			errs = append(errs, newValidationError(i, in.Token, err))
			continue
		}
	}

	return &DetectionRuleValidationResultResolver{errors: errs}, nil
}

// newValidationError builds a rejection resolver anchored to the offending rule.
func newValidationError(index int, token string, err error) *DetectionRuleValidationErrorResolver {
	return &DetectionRuleValidationErrorResolver{index: int32(index), token: token, message: err.Error()}
}

// DetectionRuleValidationResultResolver resolves the batch outcome. It carries only the
// rejections; valid is derived (no rejections ⇒ valid) so the two can never disagree.
type DetectionRuleValidationResultResolver struct {
	errors []*DetectionRuleValidationErrorResolver
}

// Valid reports whether every submitted rule compiled.
func (r *DetectionRuleValidationResultResolver) Valid() bool { return len(r.errors) == 0 }

// Errors returns one entry per rejected rule (empty when valid).
func (r *DetectionRuleValidationResultResolver) Errors() []*DetectionRuleValidationErrorResolver {
	return r.errors
}

// DetectionRuleValidationErrorResolver resolves a single rejection.
type DetectionRuleValidationErrorResolver struct {
	index   int32
	token   string
	message string
}

// Index resolves the zero-based position of the offending definition in the input list.
func (r *DetectionRuleValidationErrorResolver) Index() int32 { return r.index }

// Token resolves the rejected rule's device-management token.
func (r *DetectionRuleValidationErrorResolver) Token() string { return r.token }

// Message resolves the console-surfaceable failure reason.
func (r *DetectionRuleValidationErrorResolver) Message() string { return r.message }
