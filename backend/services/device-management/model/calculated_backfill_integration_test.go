// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The calculated-metric backfill doors on the real engine: two tenants holding a profile of the
// SAME token, devices that were deleted and a token that was reused, read through the plan door
// (whose snapshots are jsonb here, not text) and walked through the roster in keyset pages.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/... -run BackfillDoorsOnPostgres -v
package model

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBackfillDoorsOnPostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	api.CalculatedMetricsCeiling = &stubCeiling{n: 10}
	stamp := time.Now().UnixNano()
	acme := core.WithTenant(context.Background(), fmt.Sprintf("bfa%d", stamp))
	globex := core.WithTenant(context.Background(), fmt.Sprintf("bfg%d", stamp))

	// Both tenants publish profile "meter" with a different formula and hold devices of it.
	seed := func(ctx context.Context, expr string, devices int) {
		seedType(t, api, ctx, "meter-type", "meter")
		for _, req := range []*MetricDefinitionCreateRequest{
			{Token: "md-x", DeviceProfileToken: "meter", MetricKey: "x", DataType: "DOUBLE"},
			{Token: "md-y", DeviceProfileToken: "meter", MetricKey: "y", DataType: "DOUBLE", Expression: &expr},
		} {
			_, err := api.CreateMetricDefinition(ctx, req)
			require.NoError(t, err)
		}
		_, err := api.PublishDeviceProfile(ctx, "meter", nil, nil, "tester")
		require.NoError(t, err)
		for i := 0; i < devices; i++ {
			md := fmt.Sprintf(`{"gain": %d}`, i)
			_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: fmt.Sprintf("d%02d", i),
				DeviceTypeToken: "meter-type", Metadata: &md})
			require.NoError(t, err)
		}
	}
	seed(acme, "x * device.gain", 7)
	seed(globex, "x + 1.0", 3)

	// The plan door answers each tenant from its own version row.
	plan, err := api.CalculatedMetricPlan(acme, "meter", nil)
	require.NoError(t, err)
	require.Len(t, plan.Calculated, 1)
	assert.Equal(t, "x * device.gain", plan.Calculated[0].Expression)
	assert.Equal(t, []string{"gain"}, plan.ConstantKeys)
	assert.True(t, plan.Calculated[0].NewInVersion)
	assert.NotNil(t, plan.ActiveSince)
	gplan, err := api.CalculatedMetricPlan(globex, "meter", nil)
	require.NoError(t, err)
	assert.Equal(t, "x + 1.0", gplan.Calculated[0].Expression)
	assert.Empty(t, gplan.ConstantKeys)

	// A deleted device leaves the roster; its token, reused, comes back as a NEW row whose
	// createdAt is the new device's, so a worker clamping on it never fills the old one's readings.
	var before Device
	require.NoError(t, api.RDB.DB(acme).Where("token = ?", "d03").First(&before).Error)
	deleted, err := api.DeleteDevice(acme, "d03")
	require.NoError(t, err)
	require.True(t, deleted)
	_, err = api.DeleteDevice(acme, "d05")
	require.NoError(t, err)
	_, err = api.CreateDevice(acme, &DeviceCreateRequest{Token: "d03", DeviceTypeToken: "meter-type"})
	require.NoError(t, err)

	var walked []BackfillRosterEntry
	var after uint64
	for page := 0; ; page++ {
		got, err := api.CalculatedBackfillRoster(acme, "meter", []string{"gain"}, nil, after, 2)
		require.NoError(t, err)
		if page == 0 {
			require.NotNil(t, got.Total)
			assert.Equal(t, int64(6), *got.Total, "7 created, 2 deleted, 1 reused token")
		} else {
			assert.Nil(t, got.Total)
		}
		walked = append(walked, got.Entries...)
		if got.NextCursor == nil {
			break
		}
		require.Greater(t, *got.NextCursor, after)
		after = *got.NextCursor
	}
	require.Len(t, walked, 6)
	seen := map[string]BackfillRosterEntry{}
	for i, e := range walked {
		if i > 0 {
			assert.Greater(t, e.DeviceId, walked[i-1].DeviceId, "device id order")
		}
		_, dup := seen[e.DeviceToken]
		assert.False(t, dup, "duplicate %s", e.DeviceToken)
		seen[e.DeviceToken] = e
	}
	_, gone := seen["d05"]
	assert.False(t, gone, "a deleted device is not on the roster")
	reused, ok := seen["d03"]
	require.True(t, ok)
	assert.NotEqual(t, before.ID, reused.DeviceId, "the reused token is the new row")
	assert.True(t, reused.CreatedAt.After(before.CreatedAt), "createdAt is the new device's")
	assert.Empty(t, reused.Constants, "the new device holds no constant")
	require.Len(t, seen["d06"].Constants, 1)
	require.NotNil(t, seen["d06"].Constants[0].Number)
	assert.Equal(t, 6.0, *seen["d06"].Constants[0].Number)

	// Each tenant walks only its own devices, under the same profile token.
	g, err := api.CalculatedBackfillRoster(globex, "meter", nil, nil, 0, 500)
	require.NoError(t, err)
	assert.Len(t, g.Entries, 3)
	assert.Equal(t, int64(3), *g.Total)
	for _, e := range g.Entries {
		_, inAcme := seen[e.DeviceToken]
		if inAcme {
			assert.NotEqual(t, seen[e.DeviceToken].DeviceId, e.DeviceId, "%s is globex's own row", e.DeviceToken)
		}
	}
	// A token filter is resolved in the caller's tenant: globex's d01 exists, acme's deleted d05
	// does not, for either of them.
	_, err = api.CalculatedBackfillRoster(globex, "meter", nil, []string{"d01"}, 0, 5)
	require.NoError(t, err)
	_, err = api.CalculatedBackfillRoster(globex, "meter", nil, []string{"d05"}, 0, 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "d05")
	_, err = api.CalculatedBackfillRoster(acme, "meter", nil, []string{"d05"}, 0, 5)
	require.Error(t, err, "a deleted device is not a device of the profile")

	// A third tenant with no such profile gets NOT_FOUND from both doors.
	none := core.WithTenant(context.Background(), fmt.Sprintf("bfn%d", stamp))
	_, err = api.CalculatedMetricPlan(none, "meter", nil)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "%v", err)
	_, err = api.CalculatedBackfillRoster(none, "meter", nil, nil, 0, 5)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "%v", err)
}
