// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func TestRuleCeilingPublishAndRollback(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	cap := &stubRulesCeiling{n: intCeiling(1)}
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
	requireRuleLimit(t, err)
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
	requireRuleLimit(t, err)
	current, err := api.deviceProfileByToken(ctx, "a")
	require.NoError(t, err)
	require.EqualValues(t, 3, current.ActiveVersion.Int32)
	// A tier downgrade permits reductions even while the tenant stays over budget.
	cap.n = intCeiling(2)
	_, err = api.RollbackDeviceProfile(ctx, "a", 1)
	require.NoError(t, err)
	cap.n = intCeiling(0)
	require.NoError(t, api.RDB.DB(ctx).Model(&DetectionRule{}).Where("device_profile_id = ?", b.ID).Update("enabled", false).Error)
	_, err = api.PublishDeviceProfile(ctx, "b", nil, nil, "test")
	require.NoError(t, err)
	_, err = api.RollbackDeviceProfile(ctx, "b", 1)
	requireRuleLimit(t, err)
}

func intCeiling(n int) *int { return &n }

type stubRulesCeiling struct {
	n     *int
	err   error
	calls int
}

func (s *stubRulesCeiling) Resolve(context.Context, string) (*int, error) {
	s.calls++
	return s.n, s.err
}
func requireRuleLimit(t *testing.T, err error) {
	t.Helper()
	refusal, ok := limit.As(err)
	require.True(t, ok, "expected a typed limit refusal, got %v", err)
	require.Equal(t, "LIMIT_EXCEEDED", refusal.Extensions()["code"])
}

func TestRuleCeilingUnset(t *testing.T) {
	for _, resolver := range []RulesCeilingResolver{nil, &stubRulesCeiling{}} {
		api := newPublishEmitTestApi(t)
		ctx := core.WithTenant(context.Background(), "acme")
		api.RulesCeiling = resolver
		ceiling, err := api.rulesCeiling(ctx)
		require.NoError(t, err)
		require.Nil(t, ceiling)
		a := seedProfileWithRule(t, api, ctx, "a", "ra", true)
		// Existing tenants must be able to activate more than the reporting default.
		for i := 0; i < 500; i++ {
			rule := &DetectionRule{DeviceProfileId: a.ID, Enabled: true, Definition: datatypes.JSON([]byte(`{"name":"n","type":"threshold"}`))}
			rule.Token = fmt.Sprintf("r%d", i)
			require.NoError(t, api.RDB.DB(ctx).Create(rule).Error)
		}
		_, err = api.PublishDeviceProfile(ctx, "a", nil, nil, "test")
		require.NoError(t, err)
	}
}

func TestRuleCeilingFailsClosedOnlyForIncreases(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	a := seedProfileWithRule(t, api, ctx, "a", "ra", true)
	_, err := api.PublishDeviceProfile(ctx, "a", nil, nil, "test")
	require.NoError(t, err)
	// Version 2 has no rules; version 1 has one.
	require.NoError(t, api.RDB.DB(ctx).Model(&DetectionRule{}).Where("device_profile_id = ?", a.ID).Update("enabled", false).Error)
	_, err = api.PublishDeviceProfile(ctx, "a", nil, nil, "test")
	require.NoError(t, err)
	_, err = api.RollbackDeviceProfile(ctx, "a", 1)
	require.NoError(t, err)
	failed := errors.New("unreachable")
	cap := &stubRulesCeiling{err: failed}
	api.RulesCeiling = cap
	// Equal count publish/rollback, decreasing rollback/publish, and rule-free changes.
	require.NoError(t, api.RDB.DB(ctx).Model(&DetectionRule{}).Where("device_profile_id = ?", a.ID).Update("enabled", true).Error)
	_, err = api.PublishDeviceProfile(ctx, "a", nil, nil, "test")
	require.NoError(t, err)
	_, err = api.RollbackDeviceProfile(ctx, "a", 1)
	require.NoError(t, err)
	_, err = api.RollbackDeviceProfile(ctx, "a", 2)
	require.NoError(t, err)
	// Restore one rule without a ceiling so the failing resolver also sees a decreasing publish.
	api.RulesCeiling = nil
	_, err = api.RollbackDeviceProfile(ctx, "a", 1)
	require.NoError(t, err)
	api.RulesCeiling = cap
	require.NoError(t, api.RDB.DB(ctx).Model(&DetectionRule{}).Where("device_profile_id = ?", a.ID).Update("enabled", false).Error)
	_, err = api.PublishDeviceProfile(ctx, "a", nil, nil, "test")
	require.NoError(t, err)
	_, err = api.RollbackDeviceProfile(ctx, "a", 2)
	require.NoError(t, err)
	_, err = api.PublishDeviceProfile(ctx, "a", nil, nil, "test")
	require.NoError(t, err)
	seedProfileWithRule(t, api, ctx, "empty", "off", false)
	_, err = api.PublishDeviceProfile(ctx, "empty", nil, nil, "test")
	require.NoError(t, err)
	require.Zero(t, cap.calls)
	// Both activation paths must fail closed when they would add a rule.
	_, err = api.RollbackDeviceProfile(ctx, "a", 1)
	require.ErrorIs(t, err, failed)
	require.NoError(t, api.RDB.DB(ctx).Model(&DetectionRule{}).Where("device_profile_id = ?", a.ID).Update("enabled", true).Error)
	_, err = api.PublishDeviceProfile(ctx, "a", nil, nil, "test")
	require.ErrorIs(t, err, failed)
	require.Equal(t, 2, cap.calls)
}
