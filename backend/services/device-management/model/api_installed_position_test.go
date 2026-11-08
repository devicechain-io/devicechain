// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/require"
)

// The range check refuses before the database is touched. The Api here has NO store at all: a
// check that reached the database would dereference nil, so passing proves the refusal came first.
func TestAnInvalidInstalledPositionIsRefusedBeforeTheDatabase(t *testing.T) {
	api := &Api{}
	ctx := context.Background()
	for _, c := range []struct {
		name string
		in   InstalledPositionInput
	}{
		{"latitude 91", InstalledPositionInput{Latitude: 91}},
		{"latitude -90.0001", InstalledPositionInput{Latitude: -90.0001}},
		{"longitude 180.5", InstalledPositionInput{Longitude: 180.5}},
		{"latitude NaN", InstalledPositionInput{Latitude: math.NaN()}},
		{"longitude Inf", InstalledPositionInput{Longitude: math.Inf(1)}},
		{"elevation NaN", InstalledPositionInput{Elevation: f64(math.NaN())}},
		{"negative accuracy", InstalledPositionInput{Accuracy: f64(-1)}},
		{"unknown source", InstalledPositionInput{Source: strp("GUESS")}},
	} {
		_, err := api.SetInstalledPosition(ctx, "DEVICE", "d1", c.in)
		require.Error(t, err, c.name)
		require.True(t, errors.Is(err, ErrInvalidInstalledPosition), "%s: %v", c.name, err)
	}
	_, err := api.SetInstalledPosition(ctx, "AREA", "d1", InstalledPositionInput{})
	require.True(t, errors.Is(err, ErrInvalidInstalledPosition), "an unknown family: %v", err)

	// The boundaries themselves are valid.
	for _, in := range []InstalledPositionInput{
		{Latitude: 90, Longitude: 180}, {Latitude: -90, Longitude: -180}, {Latitude: 0, Longitude: 0},
	} {
		_, err := in.validate()
		require.NoError(t, err)
	}
}

func installedImportCtx(authorities ...auth.Authority) context.Context {
	strs := make([]string, 0, len(authorities))
	for _, a := range authorities {
		strs = append(strs, string(a))
	}
	return auth.WithClaims(partialUpdateCtx(), &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Authorities: strs})
}

func installedRow(token string, lat, lon *float64) *DeviceImportRow {
	return &DeviceImportRow{Token: token, DeviceTypeToken: "sensor", InstalledLatitude: lat, InstalledLongitude: lon}
}

// Import accepts optional installed-position columns, with the range check as a deterministic
// PER-ROW rejection: a bad row is INVALID, the rows beside it are created.
func TestImportStoresInstalledPositionsAndRejectsBadRowsOneByOne(t *testing.T) {
	api, _ := newImportApi(t)
	ctx := installedImportCtx(auth.DeviceWrite, auth.LocationInstalledRead)
	rows := []*DeviceImportRow{
		installedRow("lamp-1", f64(40.7128), f64(-74.006)),
		installedRow("lamp-2", f64(91), f64(0)),                                  // out of range
		installedRow("lamp-3", f64(10), nil),                                     // half a coordinate
		installedRow("lamp-4", nil, nil),                                         // states no position
		{Token: "lamp-5", DeviceTypeToken: "sensor", InstalledElevation: f64(3)}, // elevation alone
		installedRow("lamp-6", f64(-90), f64(180)),
	}
	res := mustImport(t, api, ctx, rows, DeviceImportModeNone, false)
	require.Equal(t, []string{DeviceImportCreated, DeviceImportInvalid, DeviceImportInvalid, DeviceImportCreated,
		DeviceImportInvalid, DeviceImportCreated}, outcomes(res))

	got, err := api.DevicesByToken(ctx, []string{"lamp-1", "lamp-4", "lamp-2"})
	require.NoError(t, err)
	byToken := map[string]*Device{}
	for _, d := range got {
		byToken[d.Token] = d
	}
	require.Len(t, byToken, 2, "the out-of-range row must not have created a device")
	require.InDelta(t, 40.7128, byToken["lamp-1"].InstalledLat.Float64, 1e-9)
	require.Equal(t, InstalledSourceImport, byToken["lamp-1"].InstalledSource.String)
	require.True(t, byToken["lamp-1"].InstalledUpdatedAt.Valid)
	require.False(t, byToken["lamp-4"].InstalledLat.Valid, "a row that states no position stores none")
}

func TestImportReRunComparesTheInstalledPositionOnlyForACallerWhoMayReadIt(t *testing.T) {
	api, _ := newImportApi(t)
	holder := installedImportCtx(auth.DeviceWrite, auth.LocationInstalledRead)
	writer := installedImportCtx(auth.DeviceWrite)
	row := func(lat float64) []*DeviceImportRow {
		return []*DeviceImportRow{installedRow("lamp-1", f64(lat), f64(-74.006))}
	}

	require.Equal(t, int32(1), mustImport(t, api, holder, row(40.7128), DeviceImportModeNone, false).Created)
	// Same file, same position: EXISTS (an exactly-rounded comparison, not a float equality).
	require.Equal(t, int32(1), mustImport(t, api, holder, row(40.7128), DeviceImportModeNone, false).Exists)
	// A different position is a CONFLICT, and nothing is updated.
	diff := mustImport(t, api, holder, row(41), DeviceImportModeNone, false)
	require.Equal(t, int32(1), diff.Conflict, outcomes(diff))
	d, _ := api.DevicesByToken(holder, []string{"lamp-1"})
	require.InDelta(t, 40.7128, d[0].InstalledLat.Float64, 1e-9)

	// 🔴 A writer who may not read positions gets the SAME answer whether its guess is right or
	// wrong, so a re-run is no oracle for a coordinate.
	right := mustImport(t, api, writer, row(40.7128), DeviceImportModeNone, false)
	wrong := mustImport(t, api, writer, row(41), DeviceImportModeNone, false)
	require.Equal(t, outcomes(right), outcomes(wrong))
	require.Equal(t, int32(1), right.Conflict)
	// A row that states no position is unaffected for everyone.
	none := mustImport(t, api, writer, []*DeviceImportRow{{Token: "lamp-1", DeviceTypeToken: "sensor"}}, DeviceImportModeNone, false)
	require.Equal(t, int32(1), none.Exists)
}
