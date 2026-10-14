// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DisabledDevicePage answers exactly the devices whose disabled-since instant is set, pages by
// id, and sees only the caller's tenant: command-delivery treats it as the authoritative list of
// devices whose queued commands it must hold.
func TestDisabledDevicePageListsOnlyDisabledDevicesOfTheTenant(t *testing.T) {
	api, ctx, _, _, _ := newReconcileDoorApi(t)
	seedType(t, api, ctx, "sensor", "")
	for _, tok := range []string{"d1", "d2", "d3", "d4"} {
		_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: tok, DeviceTypeToken: "sensor"})
		require.NoError(t, err)
	}
	require.NoError(t, api.RDB.DB(ctx).Model(&Device{}).Where("token IN ?", []string{"d2", "d4"}).
		Update("disabled_at", time.Now()).Error)

	// Another tenant has a disabled device of its own, which must never appear below.
	other := core.WithTenant(context.Background(), "other")
	seedType(t, api, other, "sensor", "")
	_, err := api.CreateDevice(other, &DeviceCreateRequest{Token: "d9", DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	require.NoError(t, api.RDB.DB(other).Model(&Device{}).Where("token = ?", "d9").
		Update("disabled_at", time.Now()).Error)

	var all []string
	var after uint64
	for {
		page, err := api.DisabledDevicePage(ctx, after, 1)
		require.NoError(t, err)
		all = append(all, page.Tokens...)
		if page.NextCursor == nil {
			break
		}
		after = *page.NextCursor
	}
	assert.Equal(t, []string{"d2", "d4"}, all, "only this tenant's disabled devices")

	otherPage, err := api.DisabledDevicePage(other, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"d9"}, otherPage.Tokens)

	_, err = api.DisabledDevicePage(ctx, 0, 0)
	assert.Error(t, err, "a page size outside 1..max is refused, not clamped")
}
