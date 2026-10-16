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
//
// The same document reads the device's scope anchors (ADR-081) — what the command is
// stamped with, and what decides whether a member limited to part of the tenant may send it
// at all. Two root fields of ONE request rather than a second round trip: every enqueue,
// REACT's included, needs the stamp, and the two answers then describe the device as one
// request saw it.
const validateCommandEnqueue = `query($deviceToken: String!, $deviceTokens: [String!]!, $commandKey: String!, $payload: String) {
  validateCommandEnqueue(deviceToken: $deviceToken, commandKey: $commandKey, payload: $payload) {
    allowed
    code
    reason
  }
  deviceScopeAnchors(deviceTokens: $deviceTokens) {
    deviceToken
    anchors
  }
}`

// ValidateEnqueue reports whether a command may be enqueued to deviceToken in the
// caller's tenant (read from context, then passed explicitly to the service call), and
// the device's scope anchors: nil when the device does not exist, an empty non-nil list
// for a device with no tracked relationship. The anchors come back alongside a rejection
// too, because the caller decides a scoped member's right to the device BEFORE it relays
// any verdict about it.
//
// 🔑 IT MAY BE CALLED WHILE SERVING A SCOPED MEMBER (ADR-081), and it says so to svcclient
// on this one call — derived here, never left on a shared context. The service token it
// presents reads the device unscoped, which is safe for exactly one reason: nothing it
// returns reaches the member before CreateCommand has compared the anchors with the
// member's scope, and a device outside it is answered as one that does not exist.
//
// The two failure modes are kept distinct, and the distinction is the whole point
// of the verdict shape: a REJECTION (the device does not exist, the command is not
// in the profile's published vocabulary, or the payload violates its schema) comes
// back as a successful query with allowed=false and is returned as
// *model.EnqueueRejected for the caller to relay to its API client; a transport or
// availability failure comes back as a plain error, on which the caller fails
// closed. If both arrived as errors, an outage would be indistinguishable from a
// bad command.
func (v *EnqueueValidator) ValidateEnqueue(ctx context.Context, deviceToken string, commandKey string, payload []byte) ([]string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("verify: no tenant in context")
	}
	// Allowed is a *bool, not a bool, so a response that omits the field is
	// distinguishable from an explicit false. The schema declares it non-null, so an
	// absent value means a broken or intercepted response rather than a verdict —
	// which must fail closed as an availability error, NOT be reported to the client
	// as "your command is invalid".
	var out struct {
		ValidateCommandEnqueue struct {
			Allowed *bool   `json:"allowed"`
			Code    *string `json:"code"`
			Reason  *string `json:"reason"`
		} `json:"validateCommandEnqueue"`
		// A pointer so an answer that omits the list is told apart from an empty one: the
		// schema declares it non-null, so its absence is a broken response, and reading it as
		// "no such device" would turn that into a verdict.
		DeviceScopeAnchors *[]struct {
			DeviceToken string   `json:"deviceToken"`
			Anchors     []string `json:"anchors"`
		} `json:"deviceScopeAnchors"`
	}
	vars := map[string]any{
		"deviceToken":  deviceToken,
		"deviceTokens": []string{deviceToken},
		"commandKey":   commandKey,
	}
	// An absent payload is sent as a GraphQL null rather than an empty string: the
	// owner treats null as "no arguments supplied" (valid unless a required
	// parameter is declared), whereas "" would be a present-but-unparseable body.
	if payload != nil {
		vars["payload"] = string(payload)
	} else {
		vars["payload"] = nil
	}
	if err := v.client.Query(svcclient.OnBehalfOfScopedCaller(ctx), v.url, tenant, validateCommandEnqueue, vars, &out); err != nil {
		return nil, err
	}
	if out.ValidateCommandEnqueue.Allowed == nil {
		return nil, fmt.Errorf("verify: device-management returned no enqueue verdict")
	}
	if out.DeviceScopeAnchors == nil {
		return nil, fmt.Errorf("verify: device-management returned no device anchors")
	}
	var anchors []string
	for _, d := range *out.DeviceScopeAnchors {
		if d.DeviceToken == deviceToken {
			anchors = append([]string{}, d.Anchors...)
			break
		}
	}
	if !*out.ValidateCommandEnqueue.Allowed {
		reason := "command is not valid for this device"
		if out.ValidateCommandEnqueue.Reason != nil && *out.ValidateCommandEnqueue.Reason != "" {
			reason = *out.ValidateCommandEnqueue.Reason
		}
		// The code is relayed EXACTLY as the owner sent it, including a value this
		// build has never heard of: device-management owns the vocabulary and
		// therefore owns the reasons a command can be refused, and a translation table
		// here would silently drop any case it was added after. An absent or empty
		// code stays empty and is marked unclassified upstream — a rejection is still
		// a rejection even when we cannot say which kind.
		code := model.RejectionCode("")
		if out.ValidateCommandEnqueue.Code != nil {
			code = model.RejectionCode(*out.ValidateCommandEnqueue.Code)
		}
		return anchors, &model.EnqueueRejected{Code: code, Reason: reason}
	}
	return anchors, nil
}
