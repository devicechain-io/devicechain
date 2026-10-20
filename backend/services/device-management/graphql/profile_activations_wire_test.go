// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DeviceProfile.activations takes the authority every catalogue read takes, pages newest first
// by keyset, refuses a malformed cursor, and answers only the caller's tenant.
func TestProfileActivationsField(t *testing.T) {
	api := reconcileDoorApi(t) // publishes "prof" once
	ctx := reconcileCtx(api, "acme")
	_, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	_, err = api.RollbackDeviceProfile(ctx, "prof", 1)
	require.NoError(t, err)

	field := func(c *DeviceProfileResolver, first *int32, after *string) (*DeviceProfileActivationPageResolver, error) {
		return c.Activations(struct {
			First *int32
			After *string
		}{first, after})
	}
	mk := func(tenant string, authorities ...auth.Authority) *DeviceProfileResolver {
		c := reconcileCtx(api, tenant)
		if len(authorities) > 0 {
			c = withAuthorities(c, authorities...)
		}
		return &DeviceProfileResolver{M: model.DeviceProfile{}, S: &SchemaResolver{}, C: c}
	}
	setToken := func(r *DeviceProfileResolver) *DeviceProfileResolver { r.M.Token = "prof"; return r }

	_, err = field(setToken(mk("acme")), nil, nil)
	assert.Error(t, err, "no claims is refused")

	r := setToken(mk("acme", auth.DeviceRead))
	one := int32(2)
	page, err := field(r, &one, nil)
	require.NoError(t, err)
	require.Len(t, page.Entries(), 2)
	assert.Equal(t, "ROLLBACK", page.Entries()[0].Kind())
	assert.EqualValues(t, 1, page.Entries()[0].Version())
	require.NotNil(t, page.NextCursor())

	page2, err := field(r, &one, page.NextCursor())
	require.NoError(t, err)
	require.Len(t, page2.Entries(), 1)
	assert.Equal(t, "PUBLISH", page2.Entries()[0].Kind())
	assert.Nil(t, page2.NextCursor())

	bad := "12abc"
	_, err = field(r, nil, &bad)
	assert.Error(t, err, "a malformed cursor is refused, not read as start-over")
	zero := int32(0)
	_, err = field(r, &zero, nil)
	assert.Error(t, err)

	_, err = field(setToken(mk("other", auth.DeviceRead)), nil, nil)
	assert.Error(t, err, "another tenant has no such profile")
}

// activatedAt is documented as UTC, so an instant held in another zone is rendered in UTC, not
// with its offset.
func TestProfileActivationInstantIsRenderedInUTC(t *testing.T) {
	zone := time.FixedZone("UTC+5", 5*60*60)
	at := time.Date(2026, 10, 20, 17, 30, 0, 123456000, zone)
	r := &DeviceProfileActivationResolver{M: model.DeviceProfileActivation{ActivatedAt: at}}
	assert.Equal(t, "2026-10-20T12:30:00.123456Z", r.ActivatedAt())
}
