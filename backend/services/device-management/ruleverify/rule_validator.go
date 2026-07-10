// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package ruleverify validates a device profile's draft detection rules against
// event-processing over the synchronous cross-service call primitive (ADR-044
// amendment) — the reverse direction of command-delivery's device check. It keeps the
// model layer free of the svcclient machinery: model declares the narrow
// DetectionRuleValidator interface, this package implements it with svcclient.
package ruleverify

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// Validator compiles detection rules against event-processing, the single-homed owner of
// the DETECT schema + cel-go compiler. It satisfies device-management's
// model.DetectionRuleValidator.
type Validator struct {
	client *svcclient.Client
	url    string
}

// NewValidator binds a validator to a service client and event-processing's GraphQL
// endpoint URL.
func NewValidator(client *svcclient.Client, graphqlURL string) *Validator {
	return &Validator{client: client, url: graphqlURL}
}

// validateQuery compiles + cost-gates each submitted rule, returning the per-rule
// rejections; it is gated on device:read (the token the client mints).
const validateQuery = `query($rules: [DetectionRuleInput!]!) {
  validateDetectionRules(rules: $rules) {
    valid
    errors { index token message }
  }
}`

// ValidateDetectionRules submits the rules to event-processing's validateDetectionRules
// gate in the caller's tenant and returns the per-rule rejections (empty ⇒ all compiled).
// A non-nil error is a transport/availability failure — the check could not be performed —
// distinct from a rule that compiled and was rejected (which rides in the slice).
func (v *Validator) ValidateDetectionRules(ctx context.Context, rules []model.RuleToValidate) ([]model.DetectionRuleValidationFailure, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("ruleverify: no tenant in context")
	}

	inputs := make([]map[string]any, 0, len(rules))
	for _, r := range rules {
		inputs = append(inputs, map[string]any{"token": r.Token, "definition": r.Definition})
	}

	var out struct {
		ValidateDetectionRules struct {
			Valid  bool `json:"valid"`
			Errors []struct {
				Index   int32  `json:"index"`
				Token   string `json:"token"`
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"validateDetectionRules"`
	}
	if err := v.client.Query(ctx, v.url, tenant, validateQuery, map[string]any{"rules": inputs}, &out); err != nil {
		return nil, err
	}

	res := out.ValidateDetectionRules
	if res.Valid {
		return nil, nil
	}
	failures := make([]model.DetectionRuleValidationFailure, 0, len(res.Errors))
	for _, e := range res.Errors {
		failures = append(failures, model.DetectionRuleValidationFailure{Token: e.Token, Message: e.Message})
	}
	// valid=false always carries at least one rejection (event-processing derives valid
	// from the rejection count), but guard the impossible empty case so a bad publish is
	// never silently allowed through.
	if len(failures) == 0 {
		failures = append(failures, model.DetectionRuleValidationFailure{
			Message: "rule validation reported failure without detail",
		})
	}
	return failures, nil
}
