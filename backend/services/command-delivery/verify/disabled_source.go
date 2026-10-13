// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// disabledPageSize is the page this source asks device-management for, which is also that door's
// maximum.
const disabledPageSize = 1000

// DisabledSource reads, from the services that own them, the two lists command-delivery's
// disabled-device record is corrected against: the tenants of the instance (user-management) and
// each tenant's disabled devices (device-management). It satisfies processor.DisabledSource.
//
// listScope is the tenant header the cross-tenant tenantTokens call is made under. The query is
// not about any one tenant, but the client's calling convention requires one, and the resolver
// authorizes on the caller's tier rather than on this value.
type DisabledSource struct {
	client    *svcclient.Client
	dmURL     string
	umURL     string
	listScope string
}

// NewDisabledSource binds the source to a service client carrying tenant:read and device:read.
func NewDisabledSource(client *svcclient.Client, dmURL, umURL, listScope string) *DisabledSource {
	return &DisabledSource{client: client, dmURL: dmURL, umURL: umURL, listScope: listScope}
}

// Tenants lists every tenant on the instance.
func (s *DisabledSource) Tenants(ctx context.Context) ([]string, error) {
	var out struct {
		TenantTokens []string `json:"tenantTokens"`
	}
	if err := s.client.Query(ctx, s.umURL, s.listScope, `query { tenantTokens }`, nil, &out); err != nil {
		return nil, fmt.Errorf("listing tenants for the disabled-device sync: %w", err)
	}
	return out.TenantTokens, nil
}

const disabledDevicePageQuery = `query($afterId: String, $limit: Int!) {
  disabledDevicePage(afterId: $afterId, limit: $limit) {
    deviceTokens
    nextCursor
  }
}`

// DisabledDevices walks every page of the tenant's disabled devices.
//
// 🔴 A DEVICE-MANAGEMENT THAT DOES NOT SERVE THE DOOR IS AN ERROR, NEVER AN EMPTY LIST. The
// caller treats a device it holds as disabled and this list omits as ENABLED, so reading an
// older build's "unknown field" as "nobody is disabled" would release every held command.
func (s *DisabledSource) DisabledDevices(ctx context.Context, tenant string) ([]string, error) {
	var all []string
	var cursor *string
	for {
		var out struct {
			DisabledDevicePage *struct {
				DeviceTokens []string `json:"deviceTokens"`
				NextCursor   *string  `json:"nextCursor"`
			} `json:"disabledDevicePage"`
		}
		err := s.client.Query(ctx, s.dmURL, tenant, disabledDevicePageQuery,
			map[string]any{"afterId": cursor, "limit": disabledPageSize}, &out)
		if err != nil {
			return nil, fmt.Errorf("reading the disabled devices of tenant %q: %w", tenant, err)
		}
		if out.DisabledDevicePage == nil {
			return nil, fmt.Errorf("device-management returned no disabled-device page for tenant %q", tenant)
		}
		all = append(all, out.DisabledDevicePage.DeviceTokens...)
		if out.DisabledDevicePage.NextCursor == nil {
			return all, nil
		}
		cursor = out.DisabledDevicePage.NextCursor
	}
}
