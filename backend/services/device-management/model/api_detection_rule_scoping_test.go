// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ADR-062 S4a rule-side glue: a DetectionRule pins an optional {group}@{version}
// scope; saving a scoped-and-enabled rule enrolls that group@v for membership maintenance,
// and the last enabled reference dropping (delete / disable / un-scope) GCs it. These tests
// exercise the reconcile + reference-count + delete-guard + fact-threading behaviors.

const ruleDef = `{"type":"threshold","metric":"temp","op":">","value":30}`

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

// countFacetRefs returns how many reverse-index rows exist for group@v (a proxy for "the
// read-model is enrolled").
func countFacetRefs(t *testing.T, api *Api, ctx context.Context, groupId uint, version int32) int64 {
	t.Helper()
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroupFacetRef{}).
		Where("group_id = ? AND selector_version = ?", groupId, version).Count(&n).Error)
	return n
}

// A rule scoped to a published dynamic group@v enrolls it (backfills the read-model + reverse
// index) on save, and its members become visible via MembershipsForEntity.
func TestRuleScope_SaveEnrollsGroupVersion(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	seedProfile(t, api, ctx, "p1")
	d := seedDevice(t, api, ctx, "d1")
	setSharedClimate(t, api, ctx, "d1", "arid")
	g, v := publishAridGroup(t, api, ctx, "arid-areas")

	// Nothing enrolled before the rule references it.
	assert.EqualValues(t, 0, countFacetRefs(t, api, ctx, g.ID, v), "no enrollment before a rule references the group")

	_, err := api.CreateDetectionRule(ctx, scopeReq("r1", "p1", true, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)

	assert.EqualValues(t, 1, countFacetRefs(t, api, ctx, g.ID, v), "saving a scoped rule enrolls group@v")
	assert.True(t, isMemberOf(t, api, ctx, "device", d, g.ID, v), "the arid device is backfilled as a member")
}

// A disabled scoped rule does NOT enroll (it is inert until enabled) but still pins the
// group so the delete-guard blocks removing it.
func TestRuleScope_DisabledDoesNotEnrollButPins(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	seedProfile(t, api, ctx, "p1")
	g, v := publishAridGroup(t, api, ctx, "arid-areas")

	_, err := api.CreateDetectionRule(ctx, scopeReq("r1", "p1", false, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	assert.EqualValues(t, 0, countFacetRefs(t, api, ctx, g.ID, v), "a disabled rule does not enroll the read-model")

	// The delete-guard still blocks: a disabled rule can be re-enabled, so its pin holds.
	_, err = api.DeleteEntityGroup(ctx, "arid-areas")
	assert.ErrorIs(t, err, ErrEntityInUse, "a group pinned by a disabled rule cannot be deleted")
}

// Enabling a parked scoped rule enrolls the group; disabling the last enabled rule GCs it.
func TestRuleScope_EnableDisableTogglesEnrollment(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	seedProfile(t, api, ctx, "p1")
	g, v := publishAridGroup(t, api, ctx, "arid-areas")

	_, err := api.CreateDetectionRule(ctx, scopeReq("r1", "p1", false, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	assert.EqualValues(t, 0, countFacetRefs(t, api, ctx, g.ID, v), "disabled: not enrolled")

	// Enable → enroll.
	_, err = api.UpdateDetectionRule(ctx, "r1", scopeReq("r1", "p1", true, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	assert.EqualValues(t, 1, countFacetRefs(t, api, ctx, g.ID, v), "enabling enrolls group@v")

	// Disable → GC.
	_, err = api.UpdateDetectionRule(ctx, "r1", scopeReq("r1", "p1", false, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	assert.EqualValues(t, 0, countFacetRefs(t, api, ctx, g.ID, v), "disabling the last enabled rule GCs the read-model")
}

// Two enabled rules referencing the same group@v: deleting one keeps the enrollment; deleting
// the second GCs it (reference counting).
func TestRuleScope_ReferenceCountingAcrossRules(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	seedProfile(t, api, ctx, "p1")
	g, v := publishAridGroup(t, api, ctx, "arid-areas")

	_, err := api.CreateDetectionRule(ctx, scopeReq("r1", "p1", true, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	_, err = api.CreateDetectionRule(ctx, scopeReq("r2", "p1", true, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	assert.EqualValues(t, 1, countFacetRefs(t, api, ctx, g.ID, v), "both rules reference the one enrollment")

	// Delete r1 — r2 still references, so the enrollment stays.
	_, err = api.DeleteDetectionRule(ctx, "r1")
	require.NoError(t, err)
	assert.EqualValues(t, 1, countFacetRefs(t, api, ctx, g.ID, v), "enrollment survives while another enabled rule references it")

	// Delete r2 — the last reference drops, GC the enrollment.
	_, err = api.DeleteDetectionRule(ctx, "r2")
	require.NoError(t, err)
	assert.EqualValues(t, 0, countFacetRefs(t, api, ctx, g.ID, v), "enrollment GC'd when the last enabled rule is deleted")
}

// Changing a rule's scope from group@v1 to a different group GCs the vacated enrollment and
// enrolls the adopted one.
func TestRuleScope_ChangeScopeMovesEnrollment(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	seedProfile(t, api, ctx, "p1")
	gA, vA := publishAridGroup(t, api, ctx, "arid-areas")
	// A second dynamic group on a different facet.
	gB := createDynamicGroup(t, api, ctx, "humid-areas", `attr["climate"] == "humid"`)
	vbv, err := api.PublishEntityGroup(ctx, "humid-areas", nil, nil, "tester")
	require.NoError(t, err)
	vB := vbv.Version

	_, err = api.CreateDetectionRule(ctx, scopeReq("r1", "p1", true, strptr("arid-areas"), i32ptr(vA)))
	require.NoError(t, err)
	assert.EqualValues(t, 1, countFacetRefs(t, api, ctx, gA.ID, vA), "arid enrolled")
	assert.EqualValues(t, 0, countFacetRefs(t, api, ctx, gB.ID, vB), "humid not yet enrolled")

	// Re-scope r1 to the humid group.
	_, err = api.UpdateDetectionRule(ctx, "r1", scopeReq("r1", "p1", true, strptr("humid-areas"), i32ptr(vB)))
	require.NoError(t, err)
	assert.EqualValues(t, 0, countFacetRefs(t, api, ctx, gA.ID, vA), "vacated arid enrollment GC'd")
	assert.EqualValues(t, 1, countFacetRefs(t, api, ctx, gB.ID, vB), "adopted humid enrollment created")
}

// Un-scoping a rule (clearing its group) GCs the enrollment.
func TestRuleScope_UnscopeGCsEnrollment(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	seedProfile(t, api, ctx, "p1")
	g, v := publishAridGroup(t, api, ctx, "arid-areas")

	_, err := api.CreateDetectionRule(ctx, scopeReq("r1", "p1", true, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)
	assert.EqualValues(t, 1, countFacetRefs(t, api, ctx, g.ID, v), "scoped rule enrolled")

	// Update the rule to unscoped (nil scope).
	_, err = api.UpdateDetectionRule(ctx, "r1", scopeReq("r1", "p1", true, nil, nil))
	require.NoError(t, err)
	assert.EqualValues(t, 0, countFacetRefs(t, api, ctx, g.ID, v), "un-scoping GCs the enrollment")

	// The rule's columns are cleared.
	rules, err := api.DetectionRulesByToken(ctx, []string{"r1"})
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Nil(t, rules[0].EntityGroupToken, "scope token cleared")
	assert.Nil(t, rules[0].EntityGroupVersion, "scope version cleared")
}

// The delete-guard: a group referenced by any rule cannot be deleted; once the rule is
// removed the group deletes.
func TestRuleScope_DeleteGuardBlocksThenAllows(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	seedProfile(t, api, ctx, "p1")
	_, v := publishAridGroup(t, api, ctx, "arid-areas")

	_, err := api.CreateDetectionRule(ctx, scopeReq("r1", "p1", true, strptr("arid-areas"), i32ptr(v)))
	require.NoError(t, err)

	_, err = api.DeleteEntityGroup(ctx, "arid-areas")
	assert.ErrorIs(t, err, ErrEntityInUse, "a referenced group is protected")

	_, err = api.DeleteDetectionRule(ctx, "r1")
	require.NoError(t, err)

	deleted, err := api.DeleteEntityGroup(ctx, "arid-areas")
	require.NoError(t, err)
	assert.True(t, deleted, "the group deletes once no rule references it")
}

// Scope validation fails closed on invalid pins.
func TestRuleScope_ValidationRejectsBadScopes(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	seedProfile(t, api, ctx, "p1")
	_, v := publishAridGroup(t, api, ctx, "arid-areas")
	// A static group (not scopeable).
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

	// The valid pin is accepted.
	_, err = api.CreateDetectionRule(ctx, scopeReq("ok", "p1", true, strptr("arid-areas"), i32ptr(v)))
	assert.NoError(t, err, "a valid pin is accepted")
}
