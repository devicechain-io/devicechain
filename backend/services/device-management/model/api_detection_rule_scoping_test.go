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
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The ADR-062 S4 rule-side glue: a DetectionRule pins an optional {group}@{version} scope, and
// the S2 membership read-model for that group@v is enrolled while any LIVE (published, enabled)
// rule references it. Enrollment follows PUBLISHED state — reconciled when a profile's active
// version changes (publish / rollback) and on profile delete — NOT the mutable draft. These
// tests exercise that lifecycle plus the reference count, the delete-guard, and validation.

const ruleDef = `{"type":"threshold","metric":"temp","op":">","value":30}`

// newRuleScopeTestApi builds an api with every table the publish/rollback/delete enrollment
// paths touch (profiles + versions + the definition lists buildProfileSnapshot reads + the
// groups/versions/read-model + the scope-ref table + device/attribute tables for backfill).
func newRuleScopeTestApi(t *testing.T) (*Api, context.Context) {
	t.Helper()
	// Shared-cache named in-memory DB: publish + attribute writes open transactions while other
	// reads run on the pool connection, so all connections must see the same tables.
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceCredential{}, &Area{}, &AreaType{},
		&DeviceProfile{}, &DeviceProfileVersion{}, &MetricDefinition{}, &CommandDefinition{},
		&DetectionRule{}, &DetectionRuleScopeRef{}, &EntityAttribute{},
		&EntityGroup{}, &EntityGroupVersion{}, &EntityGroupMembership{}, &EntityGroupFacetRef{},
		&EntityRelationship{}, &Alarm{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api := NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "sensor"}); err != nil {
		t.Fatalf("seed device type: %v", err)
	}
	return api, ctx
}

// seedProfile creates a device profile a rule can attach to.
func seedProfile(t *testing.T, api *Api, ctx context.Context, token string) {
	t.Helper()
	if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: token}); err != nil {
		t.Fatalf("seed profile %s: %v", token, err)
	}
}

// scopeReq builds a rule create request optionally pinned to group@version.
func scopeReq(token, profile string, enabled bool, groupToken *string, version *int32) *DetectionRuleCreateRequest {
	return &DetectionRuleCreateRequest{
		Token:              token,
		DeviceProfileToken: profile,
		Definition:         ruleDef,
		Enabled:            enabled,
		EntityGroupToken:   groupToken,
		EntityGroupVersion: version,
	}
}

func i32ptr(v int32) *int32 { return &v }

// publishProfile publishes a profile and returns the new version number.
func publishProfile(t *testing.T, api *Api, ctx context.Context, token string) int32 {
	t.Helper()
	v, err := api.PublishDeviceProfile(ctx, token, nil, nil, "tester")
	require.NoError(t, err)
	return v.Version
}

// countFacetRefs returns how many reverse-index rows exist for group@v (a proxy for "the
// read-model is enrolled").
func countFacetRefs(t *testing.T, api *Api, ctx context.Context, groupId uint, version int32) int64 {
	t.Helper()
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroupFacetRef{}).
		Where("group_id = ? AND selector_version = ?", groupId, version).Count(&n).Error)
	return n
}

// Enrollment happens at PUBLISH, not at draft save: a scoped rule enrolls its group@v only
// once the profile is published, and the group's members backfill into the read-model.
func TestRuleScope_PublishEnrollsGroupVersion(t *testing.T) {
	api, ctx := newRuleScopeTestApi(t)
	seedProfile(t, api, ctx, "p1")
	d := seedDevice(t, api, ctx, "d1")
	setSharedClimate(t, api, ctx, "d1", "arid")
	g, v := publishAridGroup(t, api, ctx, "arid-areas")

	_, err := api.CreateDetectionRule(ctx, scopeReq("r1", "p1", true, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	// A DRAFT scoped rule does not enroll — enrollment is published-state-driven.
	assert.EqualValues(t, 0, countFacetRefs(t, api, ctx, g.ID, v), "draft save does not enroll")

	publishProfile(t, api, ctx, "p1")
	assert.EqualValues(t, 1, countFacetRefs(t, api, ctx, g.ID, v), "publish enrolls group@v")
	assert.True(t, isMemberOf(t, api, ctx, "device", d, g.ID, v), "the arid device is backfilled as a member")
}

// A disabled rule is inert: publishing a profile whose only scoped rule is disabled enrolls
// nothing.
func TestRuleScope_DisabledRuleNotEnrolledAtPublish(t *testing.T) {
	api, ctx := newRuleScopeTestApi(t)
	seedProfile(t, api, ctx, "p1")
	g, v := publishAridGroup(t, api, ctx, "arid-areas")

	_, err := api.CreateDetectionRule(ctx, scopeReq("r1", "p1", false, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	publishProfile(t, api, ctx, "p1")
	assert.EqualValues(t, 0, countFacetRefs(t, api, ctx, g.ID, v), "a disabled scoped rule enrolls nothing")
}

// The load-bearing lifecycle fix (Fable HIGH-1): disabling a rule in the DRAFT without
// republishing must NOT tear the read-model out from under the still-live published rule.
// Only the next publish (with the rule disabled) GCs it.
func TestRuleScope_DisableDraftKeepsEnrollmentUntilRepublish(t *testing.T) {
	api, ctx := newRuleScopeTestApi(t)
	seedProfile(t, api, ctx, "p1")
	g, v := publishAridGroup(t, api, ctx, "arid-areas")

	_, err := api.CreateDetectionRule(ctx, scopeReq("r1", "p1", true, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	publishProfile(t, api, ctx, "p1")
	require.EqualValues(t, 1, countFacetRefs(t, api, ctx, g.ID, v), "published scoped rule enrolled")

	// Disable the DRAFT rule without republishing: the live v1 still references G@v.
	_, err = api.UpdateDetectionRule(ctx, "r1", scopeReq("r1", "p1", false, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	assert.EqualValues(t, 1, countFacetRefs(t, api, ctx, g.ID, v),
		"a draft disable must NOT deregister the still-live published rule's enrollment")

	// Republish (rule now disabled in the active version) → GC.
	publishProfile(t, api, ctx, "p1")
	assert.EqualValues(t, 0, countFacetRefs(t, api, ctx, g.ID, v), "republishing the disabled rule GCs enrollment")
}

// The rollback resurrection fix (Fable HIGH-1 scenario 2): rolling back to a version whose
// scoped rule the current active version had dropped must RE-enroll that group@v.
func TestRuleScope_RollbackReEnrolls(t *testing.T) {
	api, ctx := newRuleScopeTestApi(t)
	seedProfile(t, api, ctx, "p1")
	g, v := publishAridGroup(t, api, ctx, "arid-areas")

	_, err := api.CreateDetectionRule(ctx, scopeReq("r1", "p1", true, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	publishProfile(t, api, ctx, "p1") // v1: scoped → enrolled
	require.EqualValues(t, 1, countFacetRefs(t, api, ctx, g.ID, v), "v1 enrolled")

	// Un-scope the rule and publish v2 → GC.
	_, err = api.UpdateDetectionRule(ctx, "r1", scopeReq("r1", "p1", true, nil, nil))
	require.NoError(t, err)
	publishProfile(t, api, ctx, "p1")
	require.EqualValues(t, 0, countFacetRefs(t, api, ctx, g.ID, v), "v2 (unscoped) GC'd enrollment")

	// Rollback to v1 (scoped) → the scoped rule is live again, so it must re-enroll.
	_, err = api.RollbackDeviceProfile(ctx, "p1", 1)
	require.NoError(t, err)
	assert.EqualValues(t, 1, countFacetRefs(t, api, ctx, g.ID, v), "rollback to the scoped version re-enrolls")
}

// The profile-delete fix (Fable HIGH-2): deleting a profile whose published rule was the last
// reference to a group@v GCs the enrollment (no orphan rows).
func TestRuleScope_ProfileDeleteGCsEnrollment(t *testing.T) {
	api, ctx := newRuleScopeTestApi(t)
	seedProfile(t, api, ctx, "p1")
	g, v := publishAridGroup(t, api, ctx, "arid-areas")

	_, err := api.CreateDetectionRule(ctx, scopeReq("r1", "p1", true, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	publishProfile(t, api, ctx, "p1")
	require.EqualValues(t, 1, countFacetRefs(t, api, ctx, g.ID, v), "enrolled")

	deleted, err := api.DeleteDeviceProfile(ctx, "p1")
	require.NoError(t, err)
	require.True(t, deleted)
	assert.EqualValues(t, 0, countFacetRefs(t, api, ctx, g.ID, v), "profile delete GC's its last-reference enrollment")
}

// Cross-profile reference counting: a group@v two profiles reference stays enrolled until the
// LAST profile un-references it (the refcount is global, not per-profile).
func TestRuleScope_CrossProfileReferenceCounting(t *testing.T) {
	api, ctx := newRuleScopeTestApi(t)
	seedProfile(t, api, ctx, "p1")
	seedProfile(t, api, ctx, "p2")
	g, v := publishAridGroup(t, api, ctx, "arid-areas")

	_, err := api.CreateDetectionRule(ctx, scopeReq("r1", "p1", true, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	publishProfile(t, api, ctx, "p1")
	_, err = api.CreateDetectionRule(ctx, scopeReq("r2", "p2", true, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	publishProfile(t, api, ctx, "p2")
	require.EqualValues(t, 1, countFacetRefs(t, api, ctx, g.ID, v), "both profiles reference the one enrollment")

	// Remove p1's rule and republish p1 → p2 still references, enrollment survives.
	_, err = api.DeleteDetectionRule(ctx, "r1")
	require.NoError(t, err)
	publishProfile(t, api, ctx, "p1")
	assert.EqualValues(t, 1, countFacetRefs(t, api, ctx, g.ID, v), "enrollment survives while p2 references it")

	// Remove p2's rule and republish p2 → last reference gone, GC.
	_, err = api.DeleteDetectionRule(ctx, "r2")
	require.NoError(t, err)
	publishProfile(t, api, ctx, "p2")
	assert.EqualValues(t, 0, countFacetRefs(t, api, ctx, g.ID, v), "GC'd when the last profile un-references it")
}

// The delete-guard blocks deleting a group referenced by a LIVE published rule, and by a
// DRAFT scoped rule (which would fail its next publish otherwise); once un-referenced the
// group deletes.
func TestRuleScope_DeleteGuard(t *testing.T) {
	api, ctx := newRuleScopeTestApi(t)
	seedProfile(t, api, ctx, "p1")
	_, v := publishAridGroup(t, api, ctx, "arid-areas")

	// A DRAFT scoped rule (never published) still blocks group deletion.
	_, err := api.CreateDetectionRule(ctx, scopeReq("r1", "p1", true, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	_, err = api.DeleteEntityGroup(ctx, "arid-areas")
	assert.ErrorIs(t, err, ErrEntityInUse, "a group pinned by a draft rule is protected")

	// Publish → now a LIVE published rule references it; still blocked.
	publishProfile(t, api, ctx, "p1")
	_, err = api.DeleteEntityGroup(ctx, "arid-areas")
	assert.ErrorIs(t, err, ErrEntityInUse, "a group pinned by a published rule is protected")

	// Remove the rule and republish so neither a draft nor a published reference remains.
	_, err = api.DeleteDetectionRule(ctx, "r1")
	require.NoError(t, err)
	publishProfile(t, api, ctx, "p1")

	deleted, err := api.DeleteEntityGroup(ctx, "arid-areas")
	require.NoError(t, err)
	assert.True(t, deleted, "the group deletes once no rule references it")
}

// A rule's group scope (ADR-062 S4) survives the snapshot freeze and rides the published-rule
// fact to event-processing: the scope columns are NOT json:"-", so buildProfileSnapshot's
// marshal carries them into the frozen version, and enabledSnapshotRules projects them.
func TestRuleScope_PropagatesInPublishedFact(t *testing.T) {
	api, ctx := newRuleScopeTestApi(t)
	capture := &capturePublishedRules{}
	api.DetectionRulesPublishedPublisher = capture

	seedProfile(t, api, ctx, "p1")
	_, gv := publishAridGroup(t, api, ctx, "arid-areas")
	_, err := api.CreateDetectionRule(ctx, scopeReq("hot", "p1", true, strptr("arid-areas"), i32ptr(gv)))
	require.NoError(t, err)

	publishProfile(t, api, ctx, "p1")
	if assert.Len(t, capture.events, 1) && assert.Len(t, capture.events[0].Rules, 1) {
		got := capture.events[0].Rules[0]
		assert.Equal(t, "arid-areas", got.EntityGroupToken, "scope token propagates in the fact")
		assert.Equal(t, gv, got.EntityGroupVersion, "scope version propagates in the fact")
	}
}

// Scope validation fails closed on invalid pins (draft-save, before any publish).
func TestRuleScope_ValidationRejectsBadScopes(t *testing.T) {
	api, ctx := newRuleScopeTestApi(t)
	seedProfile(t, api, ctx, "p1")
	_, v := publishAridGroup(t, api, ctx, "arid-areas")
	staticMode := string(MembershipStatic)
	_, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
		Token: "static-grp", MemberType: "device", MembershipMode: &staticMode,
	})
	require.NoError(t, err)

	cases := []struct {
		name    string
		token   *string
		version *int32
	}{
		{"token without version", strptr("arid-areas"), nil},
		{"version without token", nil, i32ptr(v)},
		{"unknown group", strptr("ghost"), i32ptr(1)},
		{"unknown version", strptr("arid-areas"), i32ptr(99)},
		{"static group", strptr("static-grp"), i32ptr(1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := api.CreateDetectionRule(ctx, scopeReq("bad", "p1", true, tc.token, tc.version))
			assert.Error(t, err, "invalid scope must be rejected")
		})
	}

	_, err = api.CreateDetectionRule(ctx, scopeReq("ok", "p1", true, strptr("arid-areas"), i32ptr(v)))
	assert.NoError(t, err, "a valid pin is accepted")
}
