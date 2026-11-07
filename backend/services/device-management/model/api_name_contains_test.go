// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func nameContainsFixture(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api, ctx := newScopingTestApi(t)
	for _, tok := range []string{"pump-a", "pump-b", "fan-1", "pct-100"} {
		dt := &DeviceType{}
		dt.Token = tok + "-type"
		dt.Name = sql.NullString{String: "Type " + tok, Valid: true}
		require.NoError(t, api.RDB.DB(ctx).Create(dt).Error)
		_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: tok, DeviceTypeToken: dt.Token, Name: strPtr("Unit " + tok)})
		require.NoError(t, err)
	}
	return api, ctx
}

func deviceTokens(t *testing.T, api *Api, ctx context.Context, c DeviceSearchCriteria) []string {
	t.Helper()
	c.PageNumber, c.PageSize = 1, 100
	found, err := api.Devices(ctx, c)
	require.NoError(t, err)
	out := []string{}
	for _, d := range found.Results {
		out = append(out, d.Token)
	}
	sort.Strings(out)
	return out
}

func TestDevicesNameContains(t *testing.T) {
	api, ctx := nameContainsFixture(t)

	require.Equal(t, []string{"pump-a", "pump-b"}, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("PUMP")}), "case-insensitive substring")
	require.Equal(t, []string{"pump-a"}, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("  unit pump-a ")}), "trimmed")
	require.Empty(t, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("zzz")}))
	require.Len(t, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("   ")}), 4, "blank is no filter")
	require.Len(t, deviceTokens(t, api, ctx, DeviceSearchCriteria{}), 4)
	// Wildcards match themselves: a bare % or _ must not match every row.
	require.Empty(t, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("%")}))
	require.Empty(t, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("_")}))
	require.Empty(t, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr(`\`)}))
	// Composes with the type filter.
	require.Equal(t, []string{"pump-b"}, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("pump"), DeviceType: strPtr("pump-b-type")}))
	require.Empty(t, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("fan"), DeviceType: strPtr("pump-b-type")}))
}

func TestDeviceTypesNameContains(t *testing.T) {
	api, ctx := nameContainsFixture(t)
	list := func(s *string) []string {
		found, err := api.DeviceTypes(ctx, DeviceTypeSearchCriteria{NameContains: s})
		require.NoError(t, err)
		out := []string{}
		for _, d := range found.Results {
			out = append(out, d.Token)
		}
		sort.Strings(out)
		return out
	}
	require.Equal(t, []string{"pump-a-type", "pump-b-type"}, list(strPtr("Pump")))
	require.Empty(t, list(strPtr("%")))
	require.Len(t, list(nil), 5, "the four seeded here and the fixture's own")
}
