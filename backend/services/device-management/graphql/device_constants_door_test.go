// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type constantsArgs = struct {
	ProfileToken string
	Keys         []string
	AfterId      *string
	Limit        int32
}

// The constants door hands out what devices carry in their metadata, so it takes the authority
// every other device read takes and answers only the caller's tenant.
func TestDeviceConstantsDoorIsAuthorizedAndTenantScoped(t *testing.T) {
	api := reconcileDoorApi(t)
	ctx := reconcileCtx(api, "acme")
	md := `{"calibration": 1.25, "site": "north"}`
	_, err := api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: "d2", DeviceTypeToken: "sensor", Metadata: &md})
	require.NoError(t, err)

	args := constantsArgs{ProfileToken: "prof", Keys: []string{"calibration"}, Limit: 10}
	r := &SchemaResolver{}

	_, err = r.DeviceConstantsPage(ctx, args)
	assert.Error(t, err, "no claims, no answer")

	page, err := r.DeviceConstantsPage(withAuthorities(ctx, auth.DeviceRead), args)
	require.NoError(t, err)
	require.Len(t, page.Entries(), 1)
	entry := page.Entries()[0]
	assert.Equal(t, "d2", entry.DeviceToken())
	require.Len(t, entry.Constants(), 1)
	assert.Equal(t, "calibration", entry.Constants()[0].Key())
	require.NotNil(t, entry.Constants()[0].Number())
	assert.Equal(t, 1.25, *entry.Constants()[0].Number())
	assert.Nil(t, page.NextCursor())

	_, err = r.DeviceConstantsPage(withAuthorities(reconcileCtx(api, "other"), auth.DeviceRead), args)
	assert.Error(t, err, "another tenant cannot even name the profile")

	bad := "12abc"
	args.AfterId = &bad
	_, err = r.DeviceConstantsPage(withAuthorities(ctx, auth.DeviceRead), args)
	assert.Error(t, err, "a malformed cursor is refused")
}
