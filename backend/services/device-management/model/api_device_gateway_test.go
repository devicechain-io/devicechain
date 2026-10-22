// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// gatewayFixture is a tenant with a device type and a handful of devices, on the harness that
// enforces foreign keys (replacementTestApi), so a rule the model forgets cannot hide behind a
// permissive fixture.
func gatewayFixture(t *testing.T, tokens ...string) (*Api, context.Context) {
	t.Helper()
	api, ctx := replacementTestApi(t)
	require.NoError(t, api.RDB.DB(ctx).Create(func() *DeviceType {
		dt := &DeviceType{}
		dt.Token = "dt"
		return dt
	}()).Error)
	for _, token := range tokens {
		_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: token, DeviceTypeToken: "dt"})
		require.NoError(t, err, "seed %s", token)
	}
	return api, ctx
}

func deviceOf(t *testing.T, api *Api, ctx context.Context, token string) *Device {
	t.Helper()
	rows, err := api.DevicesByToken(ctx, []string{token})
	require.NoError(t, err)
	require.Len(t, rows, 1, "device %s", token)
	return rows[0]
}

func gatewayOf(t *testing.T, api *Api, ctx context.Context, token string) *uint {
	t.Helper()
	return deviceOf(t, api, ctx, token).GatewayId
}

// Assigning puts the child behind the gateway; the gateway lists it; clearing reverses it. The
// positive half is the control for every refusal below: a model that refused everything would
// pass them.
func TestAssignAndClearADeviceGateway(t *testing.T) {
	api, ctx := gatewayFixture(t, "gw", "c1", "c2")
	gw := deviceOf(t, api, ctx, "gw")

	assigned, err := api.AssignDeviceGateway(ctx, "c1", "gw")
	require.NoError(t, err)
	require.NotNil(t, assigned.GatewayId)
	require.Equal(t, gw.ID, *assigned.GatewayId)
	require.NotNil(t, gatewayOf(t, api, ctx, "c1"), "the assignment is stored, not just returned")
	require.Nil(t, gatewayOf(t, api, ctx, "c2"), "another device is untouched")
	require.Nil(t, gatewayOf(t, api, ctx, "gw"), "the gateway itself is behind nothing")

	_, err = api.AssignDeviceGateway(ctx, "c2", "gw")
	require.NoError(t, err)
	page, err := api.DeviceChildren(ctx, "gw", rdb.Pagination{PageNumber: 1, PageSize: 10})
	require.NoError(t, err)
	tokens := []string{}
	for _, d := range page.Results {
		tokens = append(tokens, d.Token)
	}
	require.ElementsMatch(t, []string{"c1", "c2"}, tokens)

	got, err := api.DeviceGateway(ctx, deviceOf(t, api, ctx, "c1"))
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "gw", got.Token)

	// Idempotent: the same assignment again changes nothing and is not an error.
	_, err = api.AssignDeviceGateway(ctx, "c1", "gw")
	require.NoError(t, err)

	cleared, err := api.ClearDeviceGateway(ctx, "c1")
	require.NoError(t, err)
	require.Nil(t, cleared.GatewayId)
	require.Nil(t, gatewayOf(t, api, ctx, "c1"))
	require.NotNil(t, gatewayOf(t, api, ctx, "c2"), "clearing one child leaves its sibling")
	// Clearing a device with no gateway is not an error.
	_, err = api.ClearDeviceGateway(ctx, "c1")
	require.NoError(t, err)

	page, err = api.DeviceChildren(ctx, "gw", rdb.Pagination{PageNumber: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
}

// A child has ONE gateway: assigning it to another moves it in a single write.
func TestAssigningToAnotherGatewayMovesTheChild(t *testing.T) {
	api, ctx := gatewayFixture(t, "gw1", "gw2", "c")
	_, err := api.AssignDeviceGateway(ctx, "c", "gw1")
	require.NoError(t, err)
	_, err = api.AssignDeviceGateway(ctx, "c", "gw2")
	require.NoError(t, err)

	require.Equal(t, deviceOf(t, api, ctx, "gw2").ID, *gatewayOf(t, api, ctx, "c"))
	first, err := api.DeviceChildren(ctx, "gw1", rdb.Pagination{PageNumber: 1, PageSize: 10})
	require.NoError(t, err)
	require.Empty(t, first.Results, "the old gateway no longer fronts the child")
}

// The rules, each refused with a named error and each leaving the stored state as it was.
func TestGatewayAssignmentRulesAreEnforced(t *testing.T) {
	api, ctx := gatewayFixture(t, "gw", "gw-b", "c", "grandchild")
	_, err := api.AssignDeviceGateway(ctx, "c", "gw")
	require.NoError(t, err)

	cases := []struct {
		name       string
		device, gw string
		want       error
	}{
		{"self", "gw", "gw", ErrGatewayIsSelf},
		// Depth 2: a device behind a gateway cannot be a gateway.
		{"gateway is itself behind a gateway", "grandchild", "c", ErrGatewayHasGateway},
		// ...and a gateway cannot be put behind one.
		{"device is itself a gateway", "gw", "gw-b", ErrDeviceHasChildren},
		{"unknown gateway", "grandchild", "no-such-gateway", gorm.ErrRecordNotFound},
		{"unknown device", "no-such-device", "gw", gorm.ErrRecordNotFound},
	}
	for _, c := range cases {
		_, err := api.AssignDeviceGateway(ctx, c.device, c.gw)
		require.Error(t, err, c.name)
		require.True(t, errors.Is(err, c.want), "%s: got %v, want %v", c.name, err, c.want)
	}
	require.Nil(t, gatewayOf(t, api, ctx, "gw"), "a refused assignment wrote nothing")
	require.Nil(t, gatewayOf(t, api, ctx, "grandchild"))
	require.NotNil(t, gatewayOf(t, api, ctx, "c"), "and left the standing assignment as it was")
}

// A device of another tenant is invisible: a gateway cannot be assigned across tenants, and a
// token that exists only in another tenant is not found.
func TestGatewayAssignmentCannotCrossTenants(t *testing.T) {
	api, ctx := gatewayFixture(t, "gw", "c")
	other := core.WithTenant(context.Background(), "other")
	dt := &DeviceType{}
	dt.Token = "dt"
	require.NoError(t, api.RDB.DB(other).Create(dt).Error)
	_, err := api.CreateDevice(other, &DeviceCreateRequest{Token: "foreign-gw", DeviceTypeToken: "dt"})
	require.NoError(t, err)

	_, err = api.AssignDeviceGateway(ctx, "c", "foreign-gw")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = api.AssignDeviceGateway(other, "c", "foreign-gw")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "the other tenant has no device c either")
	require.Nil(t, gatewayOf(t, api, ctx, "c"))

	// And the in-tenant assignment still works, so the refusals above were not a dead harness.
	_, err = api.AssignDeviceGateway(ctx, "c", "gw")
	require.NoError(t, err)
	_, err = api.DeviceChildren(other, "gw", rdb.Pagination{PageNumber: 1, PageSize: 10})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "another tenant cannot list this tenant's gateway")
}

// A device can be created behind a gateway, with the same rules; a refusal creates nothing.
func TestCreatingADeviceBehindAGateway(t *testing.T) {
	api, ctx := gatewayFixture(t, "gw", "c")
	_, err := api.AssignDeviceGateway(ctx, "c", "gw")
	require.NoError(t, err)

	gw := "gw"
	created, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "new", DeviceTypeToken: "dt", GatewayToken: &gw})
	require.NoError(t, err)
	require.Equal(t, deviceOf(t, api, ctx, "gw").ID, *created.GatewayId)
	require.NotNil(t, gatewayOf(t, api, ctx, "new"))

	for name, tc := range map[string]struct {
		token string
		gw    string
		want  error
	}{
		"gateway is behind a gateway": {"n2", "c", ErrGatewayHasGateway},
		"unknown gateway":             {"n3", "nope", gorm.ErrRecordNotFound},
		"itself":                      {"n4", "n4", ErrGatewayIsSelf},
	} {
		g := tc.gw
		_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: tc.token, DeviceTypeToken: "dt", GatewayToken: &g})
		require.ErrorIs(t, err, tc.want, name)
		rows, ferr := api.DevicesByToken(ctx, []string{tc.token})
		require.NoError(t, ferr)
		require.Empty(t, rows, "%s: the refused create left a device behind", name)
	}
}

// A gateway that still has children is not deleted; with its children cleared it is. A child can
// always be deleted. The refusal is named, and it is the same code a foreign-key refusal gets.
func TestDeletingAGatewayWithChildrenIsRefused(t *testing.T) {
	api, ctx := gatewayFixture(t, "gw", "c1", "c2")
	for _, c := range []string{"c1", "c2"} {
		_, err := api.AssignDeviceGateway(ctx, c, "gw")
		require.NoError(t, err)
	}

	deleted, err := api.DeleteDevice(ctx, "gw")
	require.ErrorIs(t, err, ErrGatewayHasChildren)
	require.False(t, deleted)
	require.Contains(t, err.Error(), "2 device(s)", "the refusal says how many children block it")
	require.Len(t, mustDevices(t, api, ctx, "gw"), 1, "the refused delete removed nothing")

	deleted, err = api.DeleteDevice(ctx, "c1")
	require.NoError(t, err)
	require.True(t, deleted, "a child can always be deleted")
	_, err = api.ClearDeviceGateway(ctx, "c2")
	require.NoError(t, err)

	deleted, err = api.DeleteDevice(ctx, "gw")
	require.NoError(t, err)
	require.True(t, deleted, "with no children left the gateway is deleted")
}

func mustDevices(t *testing.T, api *Api, ctx context.Context, token string) []*Device {
	t.Helper()
	rows, err := api.DevicesByToken(ctx, []string{token})
	require.NoError(t, err)
	return rows
}

// Replacing the unit behind a gateway keeps the gateway's identity, so its children stay behind
// it: the swap rotates credentials on the same device row, and the assignment points at the row.
func TestReplacingAGatewayKeepsItsChildren(t *testing.T) {
	api, ctx := gatewayFixture(t, "gw", "c")
	_, err := api.AssignDeviceGateway(ctx, "c", "gw")
	require.NoError(t, err)
	seedCredential(t, api, ctx, "gw", "cred-old", "bearer-old", true, nil)

	_, err = api.ReplaceDevice(ctx, &DeviceReplaceRequest{DeviceToken: "gw"}, "tester", time.Now())
	require.NoError(t, err)

	require.Equal(t, deviceOf(t, api, ctx, "gw").ID, *gatewayOf(t, api, ctx, "c"),
		"the child still names the gateway it was assigned to")
	page, err := api.DeviceChildren(ctx, "gw", rdb.Pagination{PageNumber: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
}

// An assignment change evicts the CHILD's cached rows on every replica, so the resolver reads
// the new answer on the next event rather than at the TTL. The gateway is not evicted (its row
// did not change), and its sessions are not touched: a child moving is not a revocation of the
// gateway. Negative control: both mutations must evict, not just one.
func TestGatewayAssignmentEvictsTheChildNotTheGateway(t *testing.T) {
	api, ctx := gatewayFixture(t, "gw", "c")
	evictor := &captureEvictor{}
	api.CacheEvictor = evictor

	_, err := api.AssignDeviceGateway(ctx, "c", "gw")
	require.NoError(t, err)
	require.Equal(t, []string{"c"}, evictor.deviceTokenEvicts, "assign evicts the child's by-token entry")
	require.Len(t, evictor.credentialEvicts, 1)

	_, err = api.ClearDeviceGateway(ctx, "c")
	require.NoError(t, err)
	require.Equal(t, []string{"c", "c"}, evictor.deviceTokenEvicts, "clear evicts it too")
	require.Len(t, evictor.credentialEvicts, 2)

	// A refused assignment evicts nothing.
	before := len(evictor.deviceTokenEvicts)
	_, err = api.AssignDeviceGateway(ctx, "c", "c")
	require.Error(t, err)
	require.Len(t, evictor.deviceTokenEvicts, before)
}
