// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// BatchValidator asks device-management which of MANY devices may receive one command.
// It satisfies command-delivery's model.BatchEnqueueValidator.
type BatchValidator struct {
	client *svcclient.Client
	url    string
}

// NewBatchValidator binds a fleet validator to a service client and device-management's
// GraphQL endpoint URL.
func NewBatchValidator(client *svcclient.Client, graphqlURL string) *BatchValidator {
	return &BatchValidator{client: client, url: graphqlURL}
}

// validateCommandEnqueueBatch answers with REFUSALS ONLY: a device that may receive the
// command produces no entry, so a healthy fleet costs an empty response rather than one
// verdict per device. Gated on device:read, like its single-device sibling.
const validateCommandEnqueueBatch = `query($deviceTokens: [String!]!, $commandKey: String!, $payload: String) {
  validateCommandEnqueueBatch(deviceTokens: $deviceTokens, commandKey: $commandKey, payload: $payload) {
    deviceToken
    code
    reason
  }
}`

// ValidateEnqueueBatch reports which of deviceTokens may NOT receive commandKey.
//
// 🔴 THE REFUSAL LIST IS DECODED THROUGH A POINTER SO THAT "ABSENT" IS NOT READ AS "NONE",
// and on this call that distinction is a fail-open. Everywhere else in the codebase an
// empty answer is unusual enough to notice; here the empty list is the HEALTHY answer —
// it means every device may receive the command — so a response that lost the field
// entirely decodes to a nil slice and reads as unanimous approval of a fleet.
//
// svcclient already covers the reachable failures: a non-200, a malformed body, an
// oversized body, and any GraphQL `errors` array all come back as errors. What it cannot
// cover is a 200 with an empty `errors` array and no `data` for this field, because it
// skips the decode entirely when data is absent, leaving the slice at its zero value.
// That shape is not produced by a correct server — it means a broken or intercepted
// response — and the correct reading of one is that we did not get a verdict, not that
// the verdict was yes.
func (v *BatchValidator) ValidateEnqueueBatch(ctx context.Context, deviceTokens []string,
	commandKey string, payload []byte) ([]model.BatchDeviceRefusal, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("verify: no tenant in context")
	}
	var out struct {
		ValidateCommandEnqueueBatch *[]struct {
			DeviceToken string `json:"deviceToken"`
			Code        string `json:"code"`
			Reason      string `json:"reason"`
		} `json:"validateCommandEnqueueBatch"`
	}
	vars := map[string]any{
		"deviceTokens": deviceTokens,
		"commandKey":   commandKey,
	}
	// An absent payload is sent as a GraphQL null rather than an empty string, matching
	// the single-device gate: the owner treats null as "no arguments supplied", whereas
	// "" would be a present-but-unparseable body.
	if payload != nil {
		vars["payload"] = string(payload)
	} else {
		vars["payload"] = nil
	}
	if err := v.client.Query(ctx, v.url, tenant, validateCommandEnqueueBatch, vars, &out); err != nil {
		return nil, err
	}
	if out.ValidateCommandEnqueueBatch == nil {
		return nil, fmt.Errorf("verify: device-management returned no batch enqueue verdict")
	}

	refusals := make([]model.BatchDeviceRefusal, 0, len(*out.ValidateCommandEnqueueBatch))
	for _, refusal := range *out.ValidateCommandEnqueueBatch {
		// The code is relayed EXACTLY as the owner sent it, including a value this build
		// has never heard of: device-management owns the command vocabulary and therefore
		// owns the reasons a command can be refused. A translation table here would
		// silently drop any case added after this was written.
		refusals = append(refusals, model.BatchDeviceRefusal{
			DeviceToken: refusal.DeviceToken,
			Code:        model.RejectionCode(refusal.Code),
			Reason:      refusal.Reason,
		})
	}
	return refusals, nil
}
