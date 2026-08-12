// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"fmt"
	"strconv"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/rs/zerolog/log"
)

// tenantTokensQuery reads the instance's tenant list from user-management over a
// service token. It is the one cross-tenant query the data plane serves, gated on the
// SYSTEM-tier tenant:read authority so only a service or the superuser can call it.
const tenantTokensQuery = `query { tenantTokens }`

// assertedOnlineQuery asks device-state which of this source's devices it currently
// believes are asserted-online, keyed by device token.
//
// It reads deviceToken where adapter.Reconciler reads externalId, and that is the
// whole reason it is a separate query rather than a reuse — see ProjectionReader.
const assertedOnlineQuery = `query($source: String!) {
  assertedActiveDeviceStates(source: $source) { deviceToken sessionId }
}`

// GraphQLTenantLister reads the tenant list from user-management.
type GraphQLTenantLister struct {
	client adapter.GraphQLClient
	url    string
	// tenant is the tenant scope the service token is minted under. The query itself
	// is cross-tenant, but the client's calling convention requires one, and the
	// resolver authorizes on the authority's tier rather than on this value.
	tenant string
}

// NewGraphQLTenantLister binds a lister to user-management's GraphQL endpoint.
func NewGraphQLTenantLister(client adapter.GraphQLClient, url, tenant string) *GraphQLTenantLister {
	return &GraphQLTenantLister{client: client, url: url, tenant: tenant}
}

// TenantTokens lists every tenant on the instance.
func (l *GraphQLTenantLister) TenantTokens(ctx context.Context) ([]string, error) {
	var out struct {
		TenantTokens []string `json:"tenantTokens"`
	}
	if err := l.client.Query(ctx, l.url, l.tenant, tenantTokensQuery, nil, &out); err != nil {
		return nil, fmt.Errorf("listing tenants for presence reconciliation: %w", err)
	}
	return out.TenantTokens, nil
}

// GraphQLProjectionReader reads asserted-online devices from device-state.
type GraphQLProjectionReader struct {
	client adapter.GraphQLClient
	url    string
}

// NewGraphQLProjectionReader binds a reader to device-state's GraphQL endpoint.
func NewGraphQLProjectionReader(client adapter.GraphQLClient, url string) *GraphQLProjectionReader {
	return &GraphQLProjectionReader{client: client, url: url}
}

// AssertedOnline returns the tenant's asserted-online devices for source, keyed by
// DeviceKey.
//
// 🔑 THE SOURCE FILTER IS WHAT KEEPS THIS PASS FROM KILLING OTHER TRANSPORTS' DEVICES.
// The diff marks anything it finds here and not in the BROKER's inventory as offline,
// and a Sparkplug or LwM2M device is never in that inventory — it is not an MQTT
// connection on our broker at all. Without the filter, one reconciliation pass would
// declare every asserted device on the instance dead.
//
// The residual case, stated because it is a real if narrow one: a device's row records
// the source of the last event that touched it (device-state/model/api.go:133-135), so
// an MQTT-connected device that also posts over HTTP has its row attributed to the HTTP
// source and is invisible here. It is then never repaired — but also never wrongly
// killed, which is the direction to fail in.
func (r *GraphQLProjectionReader) AssertedOnline(ctx context.Context, tenant, source string) (map[string]LiveDevice, error) {
	var out struct {
		AssertedActiveDeviceStates []struct {
			DeviceToken string `json:"deviceToken"`
			SessionId   string `json:"sessionId"`
		} `json:"assertedActiveDeviceStates"`
	}
	vars := map[string]any{"source": source}
	if err := r.client.Query(ctx, r.url, tenant, assertedOnlineQuery, vars, &out); err != nil {
		return nil, err
	}
	devices := make(map[string]LiveDevice, len(out.AssertedActiveDeviceStates))
	for _, d := range out.AssertedActiveDeviceStates {
		if d.DeviceToken == "" {
			continue
		}
		// The session rides the wire as a string: it is a UnixNano value, which
		// overflows a 32-bit GraphQL Int.
		session, err := strconv.ParseUint(d.SessionId, 10, 64)
		if err != nil {
			// Skip rather than default to 0. A zero session on a synthetic disconnect
			// would be REJECTED by presence.Decide against any real stored session, so
			// the repair would silently do nothing — worse than not attempting it,
			// because it would be counted as a repair.
			log.Warn().Str("tenant", tenant).Str("device", d.DeviceToken).Str("sessionId", d.SessionId).
				Msg("Skipping a device with an unreadable presence session during reconciliation.")
			continue
		}
		devices[DeviceKey(tenant, d.DeviceToken)] = LiveDevice{
			Tenant:      tenant,
			DeviceToken: d.DeviceToken,
			SessionId:   session,
		}
	}
	return devices, nil
}
