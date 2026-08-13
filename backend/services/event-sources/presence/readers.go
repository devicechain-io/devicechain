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

// assertedStatesQuery asks device-state what it currently believes about this source's
// asserted devices, keyed by device token.
//
// It reads deviceToken where adapter.Reconciler reads externalId, and that is the whole
// reason it is a separate query rather than a reuse — see ProjectionReader.
//
// activeOnly:false because the offline rows are the ones that need repairing and their
// stored sessionId is what makes a repair applicable; `active` is what splits the two
// directions of the diff back apart on this side.
//
// 🔴 THE RESULT SET IS UNBOUNDED, AND THE OFFLINE HALF IS THE ONE THAT GROWS FOREVER.
// Nothing ever removes an ASSERTED row — the inactivity sweep skips them and a dead
// device's row persists until the device is deleted — so this returns every device ever
// asserted for the source, not the current fleet. The field is unpaged and the projection
// carries no index on (presence_source, source), so a churny tenant pays a full scan and a
// full JSON round trip every reconcile interval. The active-only read this replaced had
// the same plan and the same absence of paging, so this is a widening rather than a new
// shape — but it is a widening in the direction that accumulates.
//
// The bounded design, when it is worth building: direction 1 only needs stored sessions
// for devices the BROKER is currently holding, so a token-scoped read over the inventory
// (which the projection does index, by tenant and device token) replaces the offline half
// entirely, leaving activeOnly:true for direction 2.
const assertedStatesQuery = `query($source: String!) {
  assertedDeviceStates(source: $source, activeOnly: false) { deviceToken sessionId active }
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

// AssertedStates returns the tenant's asserted devices for source, keyed by DeviceKey,
// each carrying whether the projection believes it is currently online.
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
func (r *GraphQLProjectionReader) AssertedStates(ctx context.Context, tenant, source string) (map[string]StoredDevice, error) {
	var out struct {
		AssertedDeviceStates []struct {
			DeviceToken string `json:"deviceToken"`
			SessionId   string `json:"sessionId"`
			Active      bool   `json:"active"`
		} `json:"assertedDeviceStates"`
	}
	vars := map[string]any{"source": source}
	if err := r.client.Query(ctx, r.url, tenant, assertedStatesQuery, vars, &out); err != nil {
		return nil, err
	}
	devices := make(map[string]StoredDevice, len(out.AssertedDeviceStates))
	for _, d := range out.AssertedDeviceStates {
		if d.DeviceToken == "" {
			continue
		}
		// The session rides the wire as a string: it is a UnixNano value, which
		// overflows a 32-bit GraphQL Int.
		session, err := strconv.ParseUint(d.SessionId, 10, 64)
		if err != nil {
			// Skip rather than default to 0. A zero session tells both directions of the
			// diff a lie: a synthetic disconnect carrying it would be REJECTED by
			// presence.Decide against any real stored session, and a connect repair would
			// read the row as having no session to defer to — so it would be "repaired"
			// without anything changing, and counted.
			//
			// 🔴 Dropping is not free either, and the halves differ. For a device the
			// broker is NOT holding, the row simply goes unrepaired this pass. For one it
			// IS holding, the connect direction now sees no stored row at all and emits
			// with the broker's own id — which, if that id has regressed, is exactly the
			// rejected-forever wedge this reader exists to avoid. Both are worse than
			// dropping is bad, and neither is reachable unless the projection returns a
			// sessionId we did not write.
			log.Warn().Str("tenant", tenant).Str("device", d.DeviceToken).Str("sessionId", d.SessionId).
				Msg("Skipping a device with an unreadable presence session during reconciliation.")
			continue
		}
		devices[DeviceKey(tenant, d.DeviceToken)] = StoredDevice{
			Tenant:      tenant,
			DeviceToken: d.DeviceToken,
			SessionId:   session,
			Active:      d.Active,
		}
	}
	return devices, nil
}
