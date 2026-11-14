// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

func TestRuleCeilingPublishAndRollback(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	cap := &stubCeiling{n: 1}
	api.RulesCeiling = cap
	pub := &capturePublishedRules{}
	api.DetectionRulesPublishedPublisher = pub
	a := seedProfileWithRule(t, api, ctx, "a", "ra", true)
	b := seedProfileWithRule(t, api, ctx, "b", "rb", true)
	seedProfileWithRule(t, api, ctx, "draft-only", "unused", true)
	// Another tenant's active rule never consumes this tenant's budget.
	other := core.WithTenant(context.Background(), "other")
	seedProfileWithRule(t, api, other, "a", "ra", true)
	_, err := api.PublishDeviceProfile(other, "a", nil, nil, "test")
	require.NoError(t, err)
	_, err = api.PublishDeviceProfile(ctx, "a", nil, nil, "test")
	require.NoError(t, err)
	// Re-publishing replaces the active version; history and disabled rules cost no slots.
	seedProfileWithRule(t, api, ctx, "disabled", "off", false)
	_, err = api.PublishDeviceProfile(ctx, "disabled", nil, nil, "test")
	require.NoError(t, err)
	_, err = api.PublishDeviceProfile(ctx, "a", nil, nil, "test")
	require.NoError(t, err)
	before := len(pub.events)
	_, err = api.PublishDeviceProfile(ctx, "b", nil, nil, "test")
	require.ErrorContains(t, err, "rulesPerTenant")
	require.Len(t, pub.events, before)
	var count int64
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceProfileVersion{}).Where("device_profile_id = ?", b.ID).Count(&count).Error)
	require.Zero(t, count, "refused publishes leave no version")
	// Disable a's rule and publish, freeing the slot for b.
	require.NoError(t, api.RDB.DB(ctx).Model(&DetectionRule{}).Where("device_profile_id = ?", a.ID).Update("enabled", false).Error)
	_, err = api.PublishDeviceProfile(ctx, "a", nil, nil, "test")
	require.NoError(t, err)
	_, err = api.PublishDeviceProfile(ctx, "b", nil, nil, "test")
	require.NoError(t, err)
	_, err = api.RollbackDeviceProfile(ctx, "a", 1)
	require.ErrorContains(t, err, "rulesPerTenant")
	current, err := api.deviceProfileByToken(ctx, "a")
	require.NoError(t, err)
	require.EqualValues(t, 3, current.ActiveVersion.Int32)
	// A tier downgrade permits reductions even while the tenant stays over budget.
	cap.n = 2
	_, err = api.RollbackDeviceProfile(ctx, "a", 1)
	require.NoError(t, err)
	cap.n = 0
	require.NoError(t, api.RDB.DB(ctx).Model(&DetectionRule{}).Where("device_profile_id = ?", b.ID).Update("enabled", false).Error)
	_, err = api.PublishDeviceProfile(ctx, "b", nil, nil, "test")
	require.NoError(t, err)
	_, err = api.RollbackDeviceProfile(ctx, "b", 1)
	require.ErrorContains(t, err, "rulesPerTenant")
}

func TestRuleCeilingFailsClosed(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedProfileWithRule(t, api, ctx, "a", "ra", true)
	defaultCeiling, err := api.rulesCeiling(ctx)
	require.NoError(t, err)
	require.Equal(t, 500, defaultCeiling)
	_, err = api.PublishDeviceProfile(ctx, "a", nil, nil, "test")
	require.NoError(t, err, "nil resolver retains a finite default budget")
	api.RulesCeiling = &stubCeiling{err: errors.New("unreachable")}
	_, err = api.PublishDeviceProfile(ctx, "a", nil, nil, "test")
	require.ErrorContains(t, err, "unreachable")
	_, err = api.RollbackDeviceProfile(ctx, "a", 1)
	require.ErrorContains(t, err, "unreachable")
}
