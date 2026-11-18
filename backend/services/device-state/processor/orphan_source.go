// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// deviceLookupChunk matches device-management's devicesByToken maximum (and command-delivery).
const deviceLookupChunk = 1000

type DeviceLookup struct {
	Client                  *svcclient.Client
	DMURL, UMURL, ListScope string
}

func (s *DeviceLookup) Tenants(ctx context.Context) ([]string, error) {
	var out struct {
		TenantTokens *[]string `json:"tenantTokens"`
	}
	if err := s.Client.Query(ctx, s.UMURL, s.ListScope, `query { tenantTokens }`, nil, &out); err != nil {
		return nil, err
	}
	if out.TenantTokens == nil {
		return nil, fmt.Errorf("user-management returned no tenant list")
	}
	return *out.TenantTokens, nil
}

func (s *DeviceLookup) Devices(ctx context.Context, tenant string, tokens []string) (map[string]LiveDevice, error) {
	live := make(map[string]LiveDevice)
	for start := 0; start < len(tokens); start += deviceLookupChunk {
		var out struct {
			DevicesByToken *[]struct {
				Token     string    `json:"token"`
				ID        string    `json:"id"`
				CreatedAt time.Time `json:"createdAt"`
			} `json:"devicesByToken"`
		}
		chunk := tokens[start:min(start+deviceLookupChunk, len(tokens))]
		err := s.Client.Query(ctx, s.DMURL, tenant, `query($tokens: [String!]!) { devicesByToken(tokens: $tokens) { token id createdAt } }`, map[string]any{"tokens": chunk}, &out)
		if err != nil {
			return nil, err
		}
		if out.DevicesByToken == nil {
			return nil, fmt.Errorf("device-management returned no devicesByToken answer")
		}
		for _, d := range *out.DevicesByToken {
			id, err := strconv.ParseInt(d.ID, 10, 64)
			if err != nil || id <= 0 || d.CreatedAt.IsZero() || d.Token == "" {
				return nil, fmt.Errorf("invalid identity for device %q", d.Token)
			}
			live[d.Token] = LiveDevice{Token: d.Token, ID: id, CreatedAt: d.CreatedAt}
		}
	}
	return live, nil
}
