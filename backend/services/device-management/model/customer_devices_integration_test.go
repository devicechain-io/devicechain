// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// customerDevices on the real engine: the edge filter is an EXISTS over the relationship
// and type tables written by core/rdb, beside the device's own scope EXISTS over the same
// tables, both under the same aliases, so the statement is run on PostgreSQL rather than
// trusted from SQLite.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/... -run CustomerDevices -v
package model

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func customerDeviceTokens(t *testing.T, found *DeviceSearchResults) []string {
	t.Helper()
	out := make([]string, 0, len(found.Results))
	for _, d := range found.Results {
		out = append(out, d.Token)
	}
	sort.Strings(out)
	return out
}

func TestCustomerDevicesOnPostgres(t *testing.T) {
	r := newScopeAlarmRig(t)
	ctx := r.operator
	// shared-pump: assigned to acme AND globex. globex-pump: also "monitors" (untracked) acme.
	d, err := r.api.CreateDevice(ctx, &DeviceCreateRequest{Token: "shared-pump", DeviceTypeToken: "pump"})
	require.NoError(t, err)
	r.devices["shared-pump"] = d.ID
	r.assign(t, "shared-pump", "acme")
	r.assign(t, "shared-pump", "globex")
	_, err = r.api.CreateEntityRelationshipType(ctx, &EntityRelationshipTypeCreateRequest{Token: "monitors"})
	require.NoError(t, err)
	_, err = r.api.CreateEntityRelationship(ctx, &EntityRelationshipCreateRequest{Token: "globex-monitors-acme",
		SourceType: "device", Source: "globex-pump", TargetType: "customer", Target: "acme", RelationshipType: "monitors"})
	require.NoError(t, err)

	ask := func(c context.Context, customer, relType string, ownership bool) []string {
		found, err := r.api.CustomerDevices(c, CustomerDeviceSearchCriteria{Customer: customer,
			RelationshipType: relType, OwnershipOnly: ownership})
		require.NoError(t, err)
		require.EqualValues(t, len(found.Results), found.Pagination.TotalRecords, "the count and the page agree")
		return customerDeviceTokens(t, found)
	}

	// Unscoped: any type, any customer.
	require.Equal(t, []string{"acme-pump", "shared-pump"}, ask(ctx, "acme", AssignmentRelationshipType, false))
	require.Equal(t, []string{"globex-pump", "shared-pump"}, ask(ctx, "globex", AssignmentRelationshipType, false))
	require.Equal(t, []string{"globex-pump"}, ask(ctx, "acme", "monitors", false))
	require.Empty(t, ask(ctx, "acme", "monitors", true))
	require.Empty(t, ask(ctx, "nobody", AssignmentRelationshipType, false))

	// Scoped: the device rule applies beside the filter, so even with the resolver's
	// in-scope check bypassed (as here, calling the store directly) acme's member reads
	// only devices it owns — shared-pump, never globex-pump.
	acme := r.member("customer:acme")
	require.Equal(t, []string{"acme-pump", "shared-pump"}, ask(acme, "acme", AssignmentRelationshipType, true))
	require.Equal(t, []string{"shared-pump"}, ask(acme, "globex", AssignmentRelationshipType, true))
	require.Empty(t, ask(acme, "acme", "monitors", true))
}
