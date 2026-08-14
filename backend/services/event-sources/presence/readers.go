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
// 🔴 THE RESULT SET IS AS LARGE AS EVERY DEVICE EVER ASSERTED FOR THE SOURCE, AND THE
// OFFLINE HALF GROWS FOREVER. Nothing removes an ASSERTED row — the inactivity sweep
// skips them and a dead device's row persists until the device is deleted — so this is
// not the current fleet, it is the cumulative one. That is why it is read in PAGES: read
// whole, it crosses the cross-service response cap somewhere in the thousands of devices,
// and the client's answer past the cap is a failed read, not a shorter one. This whole
// tenant would then be skipped for the pass, every pass, on the instances that need
// reconciliation most.
//
// The narrower design remains available if the round trips ever matter: direction 1 only
// needs stored sessions for devices the BROKER is currently holding, so a token-scoped
// read over the inventory (which the projection indexes by tenant and device token) would
// replace the offline half entirely, leaving activeOnly:true for direction 2.
//
// `id` is selected because it is the page cursor, not because the diff wants it.
const assertedStatesQuery = `query($source: String!, $afterId: ID, $pageSize: Int!) {
  assertedDeviceStates(source: $source, activeOnly: false, afterId: $afterId, pageSize: $pageSize) {
    id deviceToken sessionId active
  }
}`

// assertedStatesPageSize is how many rows one page of the walk asks for.
const assertedStatesPageSize = 500

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
// 🔴 A FAILURE PART-WAY THROUGH THE WALK FAILS THE WHOLE READ. Keeping the pages already
// collected would look like a kindness and is the opposite: this map is one side of a
// DIFF, and a device missing from it because its page was never fetched is
// indistinguishable from a device the projection does not hold — so the pass would emit
// connect "repairs" for devices that needed none, stamped with the broker's session. The
// caller already handles a failed read correctly, by skipping this tenant for the pass.
func (r *GraphQLProjectionReader) AssertedStates(ctx context.Context, tenant, source string) (map[string]StoredDevice, error) {
	devices := make(map[string]StoredDevice, assertedStatesPageSize)
	var afterId string
	for {
		rows, next, err := r.assertedPage(ctx, tenant, source, afterId)
		if err != nil {
			return nil, err
		}
		r.foldAssertedPage(tenant, rows, devices)
		if len(rows) < assertedStatesPageSize {
			return devices, nil
		}
		// 🔴🔴 THE GUARD IS THAT THE CURSOR MOVED, NOT THAT THE PAGE COUNT IS SMALL. Row
		// ids ascend strictly, so a FULL page always advances the cursor; one that does
		// not means the server ignored afterId (or compared with >= instead of >), and no
		// number of further pages will make progress. Catching it here costs two pages and
		// is independent of how large the fleet is.
		//
		// It replaces a 200-page ceiling, which was the wrong instrument in a way worth
		// recording: this set is CUMULATIVE — nothing prunes an asserted row, so it grows
		// for the life of the tenant — and a ceiling of 200 pages is a hard limit of
		// 100,000 rows, after which the walk would have failed on EVERY pass, forever. That
		// is exactly the "reconciliation is off for the fleets that need it most" failure
		// this whole change exists to remove, reintroduced above the new bound. A ceiling
		// sized against fleet size can only ever move that cliff; asking whether the walk
		// is making progress removes it.
		if next == afterId {
			return nil, fmt.Errorf("the asserted-state walk for source %s is not advancing past id %q; "+
				"the server is ignoring the page cursor", source, afterId)
		}
		afterId = next
	}
}

// assertedRow is one row of a page, plus the id that carries the cursor.
type assertedRow struct {
	Id          string `json:"id"`
	DeviceToken string `json:"deviceToken"`
	SessionId   string `json:"sessionId"`
	Active      bool   `json:"active"`
}

// assertedPage reads one page and reports the cursor for the next.
//
// 🔑 THE CURSOR COMES FROM THE LAST ROW READ, NOT THE LAST ROW KEPT. foldAssertedPage
// drops rows it cannot make sense of (below), and resuming from the last KEPT row would
// re-read every dropped row on the next page — forever, if the last row of a page is one
// of them.
func (r *GraphQLProjectionReader) assertedPage(ctx context.Context, tenant, source, afterId string) ([]assertedRow, string, error) {
	var out struct {
		AssertedDeviceStates []assertedRow `json:"assertedDeviceStates"`
	}
	vars := map[string]any{"source": source, "pageSize": assertedStatesPageSize}
	if afterId != "" {
		vars["afterId"] = afterId
	}
	if err := r.client.Query(ctx, r.url, tenant, assertedStatesQuery, vars, &out); err != nil {
		return nil, "", err
	}
	rows := out.AssertedDeviceStates
	if len(rows) == 0 {
		return rows, "", nil
	}
	return rows, rows[len(rows)-1].Id, nil
}

// foldAssertedPage folds one page's rows into the accumulating projection snapshot.
func (r *GraphQLProjectionReader) foldAssertedPage(tenant string, rows []assertedRow, devices map[string]StoredDevice) {
	for _, d := range rows {
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
}
