// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// asEarlierRelease makes g@v's registration what a release older than the create-enrollment
// marker wrote: the same refs and members, no marker.
func asEarlierRelease(t *testing.T, api *Api, ctx context.Context, g *EntityGroup, v int32) {
	t.Helper()
	res := api.RDB.DB(ctx).Unscoped().Where("group_id = ? AND selector_version = ? AND facet_key = ?", g.ID, v, createEnrollFacetKey).
		Delete(&EntityGroupFacetRef{})
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected, "precondition: this release's registration wrote the marker")
}

func hasMarker(t *testing.T, api *Api, ctx context.Context, g *EntityGroup, v int32) bool {
	t.Helper()
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroupFacetRef{}).
		Where("group_id = ? AND selector_version = ? AND facet_key = ?", g.ID, v, createEnrollFacetKey).Count(&n).Error)
	return n > 0
}

// TestAGroupRegisteredBeforeTheUpgradeEnrollsAtCreate: a group@version registered by an earlier
// release has no create-enrollment marker, so a device created after the upgrade is not enrolled
// (the premise, shown first). One healing pass gives the registration its marker, enrolls the
// device created in the meantime (and announces its new owners: the group is an access scope),
// and from then on a new device is enrolled when it is created. A second pass changes nothing.
func TestAGroupRegisteredBeforeTheUpgradeEnrollsAtCreate(t *testing.T) {
	for _, family := range []string{"device", "area"} {
		t.Run(family, func(t *testing.T) {
			api, ctx := newScopingTestApi(t)
			g, v := registerCreateGroup(t, api, ctx, family, `!("retired" in attr)`, family == "device")
			asEarlierRelease(t, api, ctx, g, v)
			create := func(token string) uint {
				t.Helper()
				if family == "device" {
					d, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: token, DeviceTypeToken: "sensor"})
					require.NoError(t, err)
					return d.ID
				}
				a, err := api.CreateArea(ctx, &AreaCreateRequest{Token: token, AreaTypeToken: "region"})
				require.NoError(t, err)
				return a.ID
			}

			missed := create("created-before-the-heal")
			require.False(t, isMemberOf(t, api, ctx, family, missed, g.ID, v),
				"premise: without the marker a new entity is not enrolled at create")

			healer := NewCreateEnrollMarkerHealer(api)
			n, err := healer.Pass(context.Background())
			require.NoError(t, err)
			require.Equal(t, 1, n)
			require.True(t, hasMarker(t, api, ctx, g, v))
			assertCreateMembership(t, api, ctx, family, missed, g, v)

			assertCreateMembership(t, api, ctx, family, create("created-after-the-heal"), g, v)

			n, err = healer.Pass(context.Background())
			require.NoError(t, err)
			require.Zero(t, n, "a healed registration is not rebuilt again")
		})
	}
}

// TestTheMarkerHealLeavesOtherRegistrationsAlone: a selector that does not match an entity with
// no attributes needs no marker and gets none (and is judged once), a deregistered version is not
// registered again, and an instance with no groups has nothing to do.
func TestTheMarkerHealLeavesOtherRegistrationsAlone(t *testing.T) {
	for _, scenario := range []string{"positive-selector", "deregistered", "no-groups"} {
		t.Run(scenario, func(t *testing.T) {
			api, ctx := newScopingTestApi(t)
			var g *EntityGroup
			var v int32
			switch scenario {
			case "positive-selector":
				g, v = registerCreateGroup(t, api, ctx, "device", `"retired" in attr`, false)
				require.False(t, hasMarker(t, api, ctx, g, v), "precondition")
			case "deregistered":
				g, v = registerCreateGroup(t, api, ctx, "device", `!("retired" in attr)`, false)
				asEarlierRelease(t, api, ctx, g, v)
				require.NoError(t, api.DeregisterGroupVersionForScoping(ctx, g.Token, v))
			}
			healer := NewCreateEnrollMarkerHealer(api)
			for i := 0; i < 2; i++ {
				n, err := healer.Pass(context.Background())
				require.NoError(t, err)
				require.Zero(t, n)
			}
			if g != nil {
				require.False(t, hasMarker(t, api, ctx, g, v))
				var refs int64
				require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroupFacetRef{}).Where("group_id = ?", g.ID).Count(&refs).Error)
				if scenario == "deregistered" {
					require.Zero(t, refs, "a deregistered version must not be registered again")
				}
			}
			if scenario == "positive-selector" {
				require.True(t, healer.isSettled(enrollMarkerKey{g.ID, v}))
			}
			d, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "fresh", DeviceTypeToken: "sensor"})
			require.NoError(t, err)
			ms, err := api.MembershipsForEntity(ctx, "device", d.ID)
			require.NoError(t, err)
			require.Empty(t, ms)
		})
	}
}

// TestTheMarkerHealRebuildsWhenTheSelectorNamesTheReservedKey: a selector that names the reserved
// key itself writes a ref of the marker's shape through Keys(), in this release and in an earlier
// one alike, so that row cannot show the version was registered with the marker. A version whose
// selector also matches an entity with no attributes is rebuilt once, enrolling what an earlier
// release's creates left out, and is then left alone.
func TestTheMarkerHealRebuildsWhenTheSelectorNamesTheReservedKey(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	g, v := registerCreateGroup(t, api, ctx, "device", `!("empty" in attr)`, false)
	require.True(t, hasMarker(t, api, ctx, g, v), "precondition: the selector's own ref has the marker's key")
	d, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "created-by-an-earlier-release", DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	// An earlier release did not enroll at create.
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Where("entity_type = ? AND entity_id = ?", "device", d.ID).
		Delete(&EntityGroupMembership{}).Error)
	require.False(t, isMemberOf(t, api, ctx, "device", d.ID, g.ID, v), "premise")

	healer := NewCreateEnrollMarkerHealer(api)
	n, err := healer.Pass(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.True(t, isMemberOf(t, api, ctx, "device", d.ID, g.ID, v))
	n, err = healer.Pass(context.Background())
	require.NoError(t, err)
	require.Zero(t, n, "rebuilt once per process, then settled")
}
