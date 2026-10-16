// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// DeviceAnchorLookup asks device-management for devices' scope anchors (ADR-081): one
// "type:token" per tracked relationship, the stamp a command carries and the fact that
// decides whether a member limited to part of the tenant owns a device. It satisfies
// model.DeviceAnchorLookup.
type DeviceAnchorLookup struct {
	client *svcclient.Client
	url    string
}

// NewDeviceAnchorLookup binds a lookup to a service client and device-management's GraphQL
// endpoint URL.
func NewDeviceAnchorLookup(client *svcclient.Client, graphqlURL string) *DeviceAnchorLookup {
	return &DeviceAnchorLookup{client: client, url: graphqlURL}
}

const deviceScopeAnchorsQuery = `query($deviceTokens: [String!]!) {
  deviceScopeAnchors(deviceTokens: $deviceTokens) {
    deviceToken
    anchors
  }
}`

// DeviceScopeAnchors returns each named device's anchors; a device that does not exist is
// absent from the map, and one that nothing owns maps to an empty list.
//
// 🔑 IT MAY BE CALLED WHILE SERVING A SCOPED MEMBER, and it tells svcclient so on this one
// call, derived here and never left on a shared context. Its answer is ownership itself: a
// caller compares it with the member's scope and answers a device outside it as one that
// does not exist, so nothing it returns reaches the member as data.
func (l *DeviceAnchorLookup) DeviceScopeAnchors(ctx context.Context, deviceTokens []string) (map[string][]string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("verify: no tenant in context")
	}
	var out struct {
		// A pointer so an answer without the list is a broken response, not "no devices".
		DeviceScopeAnchors *[]struct {
			DeviceToken string   `json:"deviceToken"`
			Anchors     []string `json:"anchors"`
		} `json:"deviceScopeAnchors"`
	}
	vars := map[string]any{"deviceTokens": deviceTokens}
	if err := l.client.Query(svcclient.OnBehalfOfScopedCaller(ctx), l.url, tenant, deviceScopeAnchorsQuery, vars, &out); err != nil {
		return nil, err
	}
	if out.DeviceScopeAnchors == nil {
		return nil, fmt.Errorf("verify: device-management returned no device anchors")
	}
	found := make(map[string][]string, len(*out.DeviceScopeAnchors))
	for _, d := range *out.DeviceScopeAnchors {
		found[d.DeviceToken] = append([]string{}, d.Anchors...)
	}
	return found, nil
}
