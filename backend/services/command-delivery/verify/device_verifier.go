// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package verify checks facts owned by other services over the synchronous
// cross-service call primitive (ADR-044 amendment). It keeps the model layer free
// of the sync-call machinery: model declares the narrow DeviceVerifier interface,
// this package implements it with svcclient.
package verify

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// DeviceVerifier confirms device existence against device-management, the
// authoritative owner. It satisfies command-delivery's model.DeviceVerifier.
type DeviceVerifier struct {
	client *svcclient.Client
	url    string
}

// NewDeviceVerifier binds a verifier to a service client and device-management's
// GraphQL endpoint URL.
func NewDeviceVerifier(client *svcclient.Client, graphqlURL string) *DeviceVerifier {
	return &DeviceVerifier{client: client, url: graphqlURL}
}

// DeviceExists reports whether a device with deviceToken exists in the caller's
// tenant (read from context, then passed explicitly to the service call). It
// queries device-management's devicesByToken (gated on device:read) and treats a
// non-empty result as existence — an absent device is an empty result, not an
// error.
func (v *DeviceVerifier) DeviceExists(ctx context.Context, deviceToken string) (bool, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return false, fmt.Errorf("verify: no tenant in context")
	}
	var out struct {
		DevicesByToken []struct {
			Token string `json:"token"`
		} `json:"devicesByToken"`
	}
	const q = `query($tokens: [String!]!) { devicesByToken(tokens: $tokens) { token } }`
	if err := v.client.Query(ctx, v.url, tenant, q, map[string]any{"tokens": []string{deviceToken}}, &out); err != nil {
		return false, err
	}
	return len(out.DevicesByToken) > 0, nil
}
