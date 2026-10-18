// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The constants door lists, for the devices of ONE profile, what their metadata holds under the
// requested names, read by the resolver's own reader.
func TestDeviceConstantsPage(t *testing.T) {
	api, ctx, _, _, _ := newReconcileDoorApi(t)
	seedType(t, api, ctx, "meter", "meter-profile")
	seedType(t, api, ctx, "other", "other-profile")
	md := func(s string) *string { return &s }
	for _, d := range []struct {
		tok, typ string
		md       *string
	}{
		{"d1", "meter", md(`{"calibration": 1.5, "site": "n"}`)},
		{"d2", "meter", nil},
		{"d3", "meter", md(`{"calibration": "1.5"}`)},
		{"d4", "other", md(`{"calibration": 9}`)},
		{"d5", "meter", md(`{"site": "s"}`)},
		{"d6", "meter", md(`{"calibration": 2, "gain": 3}`)},
	} {
		_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: d.tok, DeviceTypeToken: d.typ, Metadata: d.md})
		require.NoError(t, err)
	}

	walk := func(limit int, keys ...string) []DeviceConstantsEntry {
		var out []DeviceConstantsEntry
		var after uint64
		for {
			page, err := api.DeviceConstantsPage(ctx, "meter-profile", keys, after, limit)
			require.NoError(t, err)
			out = append(out, page.Entries...)
			if page.NextCursor == nil {
				return out
			}
			after = *page.NextCursor
		}
	}
	f := func(v float64) *float64 { return &v }
	want := []struct {
		device string
		consts []DeviceConstant
	}{
		{"d1", []DeviceConstant{{Key: "calibration", Number: f(1.5)}}},
		{"d3", []DeviceConstant{{Key: "calibration", Number: nil}}}, // present, not a number
		{"d6", []DeviceConstant{{Key: "calibration", Number: f(2)}}},
	}
	for _, limit := range []int{1, 2, 100} {
		got := walk(limit, "calibration")
		require.Len(t, got, len(want), "limit %d", limit)
		for i, w := range want {
			assert.Equal(t, w.device, got[i].DeviceToken, "limit %d", limit)
			assert.Equal(t, w.consts, got[i].Constants, "limit %d", limit)
		}
	}
	// A device is not listed for a name it does not hold, and another profile's device never is.
	for _, e := range walk(100, "gain") {
		assert.Equal(t, "d6", e.DeviceToken)
	}
}

func TestDeviceConstantsPageRefusals(t *testing.T) {
	api, ctx, _, _, _ := newReconcileDoorApi(t)
	seedType(t, api, ctx, "meter", "meter-profile")
	for _, n := range []int{0, -1, MaxDeviceConstantsPageSize + 1} {
		_, err := api.DeviceConstantsPage(ctx, "meter-profile", []string{"k"}, 0, n)
		assert.Error(t, err, "limit %d", n)
	}
	// 33 names, each one a well-formed name, so the COUNT is what is refused (33 empty names
	// would be refused for being empty, whatever the count bound said).
	tooMany := make([]string, calcfield.MaxProfileConstants+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("k%d", i)
	}
	for _, keys := range [][]string{nil, {}, {""}, {strings.Repeat("k", calcfield.MaxConstantKeyBytes+1)}, tooMany} {
		_, err := api.DeviceConstantsPage(ctx, "meter-profile", keys, 0, 10)
		assert.Error(t, err, "keys %v", keys)
	}
	_, err := api.DeviceConstantsPage(ctx, "meter-profile", tooMany[:calcfield.MaxProfileConstants], 0, 10)
	assert.NoError(t, err, "the bound itself is admitted")
	_, err = api.DeviceConstantsPage(ctx, "meter-profile", []string{strings.Repeat("k", calcfield.MaxConstantKeyBytes)}, 0, 10)
	assert.NoError(t, err, "a name at the byte bound is admitted")
	_, err = api.DeviceConstantsPage(ctx, "no-such-profile", []string{"k"}, 0, 10)
	assert.Error(t, err)
}
