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

// nameContainsFixture seeds devices and types whose names and tokens differ, so a match can be
// told to have come from one or the other: "pump-a/b" carry their token in the name, "q-7" is
// named for something else entirely, and "bare-1" has no name at all.
func nameContainsFixture(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api, ctx := newScopingTestApi(t)
	seed := []struct{ token, name string }{
		{"pump-a", "Unit pump-a"},
		{"pump-b", "Unit pump-b"},
		{"fan-1", "Unit fan-1"},
		{"pct-100", "Unit pct-100"},
		{"q-7", "Boiler Feed"},
		{"bare-1", ""},
		{"Cam_Eleven", ""},
		{"CamXEleven", ""},
	}
	for _, s := range seed {
		dt := &DeviceType{}
		dt.Token = s.token + "-type"
		if s.name != "" {
			dt.Name = sql.NullString{String: "Type " + s.name, Valid: true}
		}
		require.NoError(t, api.RDB.DB(ctx).Create(dt).Error)
		req := &DeviceCreateRequest{Token: s.token, DeviceTypeToken: dt.Token}
		if s.name != "" {
			req.Name = strPtr(s.name)
		}
		_, err := api.CreateDevice(ctx, req)
		require.NoError(t, err)
	}
	return api, ctx
}

// allDevices is every seeded device; the fixture's own seed device type adds one to the types.
var allDevices = []string{"CamXEleven", "Cam_Eleven", "bare-1", "fan-1", "pct-100", "pump-a", "pump-b", "q-7"}

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

func typeTokens(t *testing.T, api *Api, ctx context.Context, s *string) []string {
	t.Helper()
	found, err := api.DeviceTypes(ctx, DeviceTypeSearchCriteria{NameContains: s})
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
	find := func(s string) []string {
		return deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr(s)})
	}

	require.Equal(t, []string{"pump-a", "pump-b"}, find("PUMP"), "case-insensitive substring")
	require.Equal(t, []string{"pump-a"}, find("  unit pump-a "), "trimmed")
	require.Equal(t, []string{"q-7"}, find("boiler"), "a name that is not the token")
	require.Equal(t, []string{"bare-1"}, find("bare"), "an unnamed device is found by its token")
	require.Equal(t, []string{"q-7"}, find("Q-7"), "a token whose name says something else")
	require.Equal(t, []string{"Cam_Eleven"}, find("cam_eleven"), "a mixed-case token is found from lower case")
	require.Equal(t, []string{"Cam_Eleven"}, find("cam_e"), "an underscore in the text is that character, not any character")
	require.Empty(t, find("zzz"))

	// Blank is no filter, the unnamed device included (its NULL name never matches a pattern).
	require.Equal(t, allDevices, find("   "))
	require.Equal(t, allDevices, deviceTokens(t, api, ctx, DeviceSearchCriteria{}))

	// Wildcards match themselves: a bare % or _ must not match every row.
	require.Empty(t, find("%"))
	require.Equal(t, []string{"Cam_Eleven"}, find("_"), "an underscore matches only a row that holds one")
	require.Empty(t, find(`\`))
	require.Equal(t, []string{"pct-100"}, find("pct-1"), "a literal hyphen and digits")

	// Composes with the type filter, and the OR inside it does not leak past that AND.
	require.Equal(t, []string{"pump-b"}, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("pump"), DeviceType: strPtr("pump-b-type")}))
	require.Empty(t, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("fan"), DeviceType: strPtr("pump-b-type")}))
	require.Equal(t, []string{"fan-1"}, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("fan"), DeviceType: strPtr("fan-1-type")}))
}

func TestDeviceTypesNameContains(t *testing.T) {
	api, ctx := nameContainsFixture(t)

	require.Equal(t, []string{"pump-a-type", "pump-b-type"}, typeTokens(t, api, ctx, strPtr("Pump")))
	require.Equal(t, []string{"bare-1-type"}, typeTokens(t, api, ctx, strPtr("bare")), "an unnamed type is found by its token")
	require.Equal(t, []string{"q-7-type"}, typeTokens(t, api, ctx, strPtr("boiler")))
	require.Empty(t, typeTokens(t, api, ctx, strPtr("%")))
	require.Len(t, typeTokens(t, api, ctx, strPtr("  ")), 9, "blank is no filter: the eight seeded and the fixture's own")
	require.Len(t, typeTokens(t, api, ctx, nil), 9)
}

// SQLite's LIKE ignores case by default and Postgres's does not, so on the default test database
// the case handling in the query is invisible: every case-folding mutant passes. This turns
// SQLite's LIKE case-sensitive, as Postgres's is, so the folding has to be done by the query.
func TestNameContainsFoldsCaseWhereLikeDoesNot(t *testing.T) {
	api, ctx := nameContainsFixture(t)
	sqlDB, err := api.RDB.Database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, api.RDB.DB(ctx).Exec("PRAGMA case_sensitive_like = ON").Error)

	var n int64
	require.NoError(t, api.RDB.DB(ctx).Raw("SELECT count(*) FROM devices WHERE name LIKE 'unit PUMP%'").Scan(&n).Error)
	require.Zero(t, n, "the pragma must have made LIKE case-sensitive, or this test proves nothing")

	require.Equal(t, []string{"pump-a", "pump-b"}, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("PuMp")}))
	require.Equal(t, []string{"q-7"}, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("BOILER")}))
	require.Equal(t, []string{"bare-1"}, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("BARE")}))
	require.Equal(t, []string{"Cam_Eleven"}, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("cAM_eLEVEN")}))
	require.Equal(t, []string{"CamXEleven", "Cam_Eleven"}, deviceTokens(t, api, ctx, DeviceSearchCriteria{NameContains: strPtr("CAM")}))
	require.Equal(t, []string{"pump-a-type", "pump-b-type"}, typeTokens(t, api, ctx, strPtr("TYPE UNIT PUMP")))
}
