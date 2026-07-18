// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package verify checks facts owned by other services over the synchronous
// cross-service call primitive (ADR-044 amendment). It keeps the model layer free
// of the sync-call machinery: model declares the narrow CommandEnqueueValidator
// interface, this package implements it with svcclient.
package verify

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// EnqueueValidator asks device-management — the authoritative owner of both the
// device and the command vocabulary — whether a command may be enqueued. It
// satisfies command-delivery's model.CommandEnqueueValidator.
type EnqueueValidator struct {
	client *svcclient.Client
	url    string
}

// NewEnqueueValidator binds a validator to a service client and device-management's
// GraphQL endpoint URL.
func NewEnqueueValidator(client *svcclient.Client, graphqlURL string) *EnqueueValidator {
	return &EnqueueValidator{client: client, url: graphqlURL}
}

// validateCommandEnqueue resolves device → published command vocabulary →
// definition → payload validation in one hop (ADR-043 decision 3), gated on
// device:read.
const validateCommandEnqueue = `query($deviceToken: String!, $commandKey: String!, $payload: String) {
  validateCommandEnqueue(deviceToken: $deviceToken, commandKey: $commandKey, payload: $payload) {
    allowed
    reason
  }
}`

// ValidateEnqueue reports whether a command may be enqueued to deviceToken in the
// caller's tenant (read from context, then passed explicitly to the service call).
//
// The two failure modes are kept distinct, and the distinction is the whole point
// of the verdict shape: a REJECTION (the device does not exist, the command is not
// in the profile's published vocabulary, or the payload violates its schema) comes
// back as a successful query with allowed=false and is returned as
// *model.EnqueueRejected for the caller to relay to its API client; a transport or
// availability failure comes back as a plain error, on which the caller fails
// closed. If both arrived as errors, an outage would be indistinguishable from a
// bad command.
func (v *EnqueueValidator) ValidateEnqueue(ctx context.Context, deviceToken string, commandKey string, payload []byte) error {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return fmt.Errorf("verify: no tenant in context")
	}
	// Allowed is a *bool, not a bool, so a response that omits the field is
	// distinguishable from an explicit false. The schema declares it non-null, so an
	// absent value means a broken or intercepted response rather than a verdict —
	// which must fail closed as an availability error, NOT be reported to the client
	// as "your command is invalid".
	var out struct {
		ValidateCommandEnqueue struct {
			Allowed *bool   `json:"allowed"`
			Reason  *string `json:"reason"`
		} `json:"validateCommandEnqueue"`
	}
	vars := map[string]any{
		"deviceToken": deviceToken,
		"commandKey":  commandKey,
	}
	// An absent payload is sent as a GraphQL null rather than an empty string: the
	// owner treats null as "no arguments supplied" (valid unless a required
	// parameter is declared), whereas "" would be a present-but-unparseable body.
	if payload != nil {
		vars["payload"] = string(payload)
	} else {
		vars["payload"] = nil
	}
	if err := v.client.Query(ctx, v.url, tenant, validateCommandEnqueue, vars, &out); err != nil {
		return err
	}
	if out.ValidateCommandEnqueue.Allowed == nil {
		return fmt.Errorf("verify: device-management returned no enqueue verdict")
	}
	if !*out.ValidateCommandEnqueue.Allowed {
		reason := "command is not valid for this device"
		if out.ValidateCommandEnqueue.Reason != nil && *out.ValidateCommandEnqueue.Reason != "" {
			reason = *out.ValidateCommandEnqueue.Reason
		}
		return &model.EnqueueRejected{Reason: reason}
	}
	return nil
}
