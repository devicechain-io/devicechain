// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The preview's constants client reads device-management's REAL constants door, in-process: a
// field-name drift between the two services fails here instead of previewing every device as
// holding no constants in production.
func constantsClientOverDM(t *testing.T, w *dmWorld, calls *int) *deviceConstantsClient {
	return &deviceConstantsClient{exec: func(ctx context.Context, svc factService, tenant, query string,
		vars map[string]any, out any) error {
		*calls++
		resp := dmSchema().Exec(schemaDmContext(w.api, tenant), query, "", wireVariables(t, vars))
		if len(resp.Errors) > 0 {
			return resp.Errors[0]
		}
		return json.Unmarshal(resp.Data, out)
	}}
}

func TestTheConstantsClientReadsDeviceManagementsDoor(t *testing.T) {
	w := newDmWorld(t)
	w.profile("meter", nil)
	w.deviceType("meter-type", "meter")
	for i := 1; i <= 5; i++ {
		md := fmt.Sprintf(`{"calibration": %d.5, "site": "n"}`, i)
		if i == 4 {
			md = `{"calibration": "four"}`
		}
		if i == 5 {
			md = `{"site": "none-here"}`
		}
		_, err := w.api.CreateDevice(w.ctx, &dmmodel.DeviceCreateRequest{Token: fmt.Sprintf("d%d", i),
			DeviceTypeToken: "meter-type", Metadata: &md})
		require.NoError(t, err)
	}

	var calls int
	c := constantsClientOverDM(t, w, &calls)
	byDevice, truncated, err := c.DeviceConstants(context.Background(), "acme", "meter", []string{"calibration"}, 10_000)
	require.NoError(t, err)
	assert.False(t, truncated)
	assert.Equal(t, 1, calls)
	require.Len(t, byDevice, 4, "d5 holds none of the names and is not listed")
	for _, tok := range []string{"d1", "d2", "d3"} {
		require.Len(t, byDevice[tok], 1, tok)
		v, ok := byDevice[tok][0].Number()
		require.True(t, ok, tok)
		assert.Equal(t, map[string]float64{"d1": 1.5, "d2": 2.5, "d3": 3.5}[tok], v)
	}
	_, usable := byDevice["d4"][0].Number()
	assert.False(t, usable, "a string is present and not a number")
}

func TestTheConstantsClientStopsAtTheDeviceCap(t *testing.T) {
	w := newDmWorld(t)
	w.profile("meter", nil)
	w.deviceType("meter-type", "meter")
	md := `{"calibration": 1}`
	for i := 1; i <= 3; i++ {
		_, err := w.api.CreateDevice(w.ctx, &dmmodel.DeviceCreateRequest{Token: fmt.Sprintf("d%d", i),
			DeviceTypeToken: "meter-type", Metadata: &md})
		require.NoError(t, err)
	}
	var calls int
	c := constantsClientOverDM(t, w, &calls)
	// A cap of one page: the first page is read (3 devices < 200, the walk ends), so it is not truncated.
	_, truncated, err := c.DeviceConstants(context.Background(), "acme", "meter", []string{"calibration"}, deviceConstantsPageSize)
	require.NoError(t, err)
	assert.False(t, truncated)
	// A cap of zero reads nothing and says the profile may hold more.
	got, truncated, err := c.DeviceConstants(context.Background(), "acme", "meter", []string{"calibration"}, 0)
	require.NoError(t, err)
	assert.True(t, truncated)
	assert.Empty(t, got)
}

func TestTheConstantsClientFailsWholeNeverPartial(t *testing.T) {
	var calls int
	c := &deviceConstantsClient{exec: func(context.Context, factService, string, string, map[string]any, any) error {
		calls++
		return errors.New("boom")
	}}
	got, _, err := c.DeviceConstants(context.Background(), "acme", "meter", []string{"k"}, 1000)
	require.Error(t, err)
	assert.Nil(t, got)

	skew := &deviceConstantsClient{exec: func(context.Context, factService, string, string, map[string]any, any) error {
		return errors.New(`Cannot query field "deviceConstantsPage" on type "Query"`)
	}}
	_, _, err = skew.DeviceConstants(context.Background(), "acme", "meter", []string{"k"}, 1000)
	require.Error(t, err)
	assert.ErrorIs(t, err, errFactsSkew)
}
