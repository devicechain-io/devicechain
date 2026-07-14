// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// capturePublishedRules records the detection-rules-published events emitted, so a test can
// assert PublishDeviceProfile propagated the frozen rule set (ADR-051 slice 4b-3).
type capturePublishedRules struct {
	events []*DetectionRulesPublishedEvent
}

func (c *capturePublishedRules) PublishDetectionRulesPublished(_ context.Context, e *DetectionRulesPublishedEvent) {
	c.events = append(c.events, e)
}

func newPublishEmitTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	// PublishDeviceProfile builds a snapshot from every definition list, then writes a version.
	if err := db.AutoMigrate(&DeviceProfile{}, &DeviceProfileVersion{}, &MetricDefinition{},
		&CommandDefinition{}, &DetectionRule{}, &DeviceType{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

func seedProfileWithRule(t *testing.T, api *Api, ctx context.Context, profileToken, ruleToken string, enabled bool) *DeviceProfile {
	t.Helper()
	p := &DeviceProfile{}
	p.Token = profileToken
	if err := api.RDB.DB(ctx).Create(p).Error; err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	dr := &DetectionRule{DeviceProfileId: p.ID, Definition: datatypes.JSON([]byte(`{"name":"n","type":"threshold"}`)), Enabled: enabled}
	dr.Token = ruleToken
	if err := api.RDB.DB(ctx).Create(dr).Error; err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	return p
}

// PublishDeviceProfile emits a detection-rules-published fact keyed on the new version's
// immutable "{profileToken}@{version}" token, carrying only the ENABLED rules.
func TestPublishDeviceProfile_EmitsEnabledRules(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	capture := &capturePublishedRules{}
	api.DetectionRulesPublishedPublisher = capture

	seedProfileWithRule(t, api, ctx, "prof", "hot", true)
	// A second, DISABLED rule on the same profile must be omitted from the fact.
	disabled := &DetectionRule{DeviceProfileId: 1, Definition: datatypes.JSON([]byte(`{"name":"d","type":"threshold"}`)), Enabled: false}
	disabled.Token = "parked"
	if err := api.RDB.DB(ctx).Create(disabled).Error; err != nil {
		t.Fatalf("seed disabled rule: %v", err)
	}

	version, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	assert.NoError(t, err)
	assert.Equal(t, int32(1), version.Version)

	if assert.Len(t, capture.events, 1, "a successful publish emits exactly one fact") {
		ev := capture.events[0]
		assert.Equal(t, "prof@1", ev.ProfileVersionToken)
		if assert.Len(t, ev.Rules, 1, "only the enabled rule propagates") {
			assert.Equal(t, "hot", ev.Rules[0].Token)
			assert.JSONEq(t, `{"name":"n","type":"threshold"}`, ev.Rules[0].Definition)
		}
	}
}

// A rule's group scope (ADR-062 S4) survives the snapshot freeze and rides the published-rule
// fact to event-processing: the scope columns are NOT json:"-", so buildProfileSnapshot's
// marshal carries them into the frozen version, and enabledSnapshotRules projects them.
func TestPublishDeviceProfile_PropagatesRuleScope(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	capture := &capturePublishedRules{}
	api.DetectionRulesPublishedPublisher = capture

	p := &DeviceProfile{}
	p.Token = "prof"
	if err := api.RDB.DB(ctx).Create(p).Error; err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	scoped := &DetectionRule{
		DeviceProfileId: p.ID, Definition: datatypes.JSON([]byte(`{"name":"n","type":"threshold"}`)),
		Enabled: true, EntityGroupToken: strptr("arid-areas"), EntityGroupVersion: i32ptr(7),
	}
	scoped.Token = "hot"
	if err := api.RDB.DB(ctx).Create(scoped).Error; err != nil {
		t.Fatalf("seed scoped rule: %v", err)
	}

	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if assert.Len(t, capture.events, 1) && assert.Len(t, capture.events[0].Rules, 1) {
		got := capture.events[0].Rules[0]
		assert.Equal(t, "arid-areas", got.EntityGroupToken, "scope token propagates in the fact")
		assert.Equal(t, int32(7), got.EntityGroupVersion, "scope version propagates in the fact")
	}
}

// The version token tracks the monotonic version number across successive publishes.
func TestPublishDeviceProfile_EmitsVersionedToken(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	capture := &capturePublishedRules{}
	api.DetectionRulesPublishedPublisher = capture

	seedProfileWithRule(t, api, ctx, "prof", "hot", true)
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	if assert.Len(t, capture.events, 2) {
		assert.Equal(t, "prof@1", capture.events[0].ProfileVersionToken)
		assert.Equal(t, "prof@2", capture.events[1].ProfileVersionToken)
	}
}

// With no publisher wired, publish still succeeds — emission is a nil-safe no-op.
func TestPublishDeviceProfile_NoPublisherIsNoOp(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedProfileWithRule(t, api, ctx, "prof", "hot", true)
	_, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	assert.NoError(t, err)
}
