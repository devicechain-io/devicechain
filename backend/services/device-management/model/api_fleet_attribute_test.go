// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFleetPlanAndCreateUseCanonicalAttributeIntent(t *testing.T) {
	for _, tc := range []struct {
		valueType, input, canonical string
	}{
		{"BOOLEAN", "1", "true"},
		{"BOOLEAN", "T", "true"},
		{"BOOLEAN", "TRUE", "true"},
		{"DOUBLE", "0x1p3", "8"},
	} {
		t.Run(tc.valueType+"/"+tc.input, func(t *testing.T) {
			armed(t)
			api, ctx := fleetFixture(t, 1)
			req := setReq("canonical", "d1")
			req.ConfigSet = &FleetConfigSetIntent{Key: "k", ValueType: tc.valueType, Value: sptr(tc.input)}
			plan, err := api.PlanFleetOperation(ctx, req)
			require.NoError(t, err)
			intent, err := fleetIntentFrom(plan.Intent)
			require.NoError(t, err)
			require.Equal(t, tc.canonical, *intent.Value)
			require.Equal(t, tc.input, *req.ConfigSet.Value, "normalization does not mutate the caller's input")
			op, err := api.CreateFleetOperation(ctx, req, "creator")
			require.NoError(t, err)
			require.Equal(t, plan.Intent, op.Intent)
			req.ConfigSet.Value = sptr(tc.canonical)
			canonicalPlan, err := api.PlanFleetOperation(ctx, req)
			require.NoError(t, err)
			require.Equal(t, canonicalPlan.RequestDigest, plan.RequestDigest)
			require.Equal(t, canonicalPlan.ContractDigest, plan.ContractDigest)
			replayed, err := api.CreateFleetOperation(ctx, req, "creator")
			require.NoError(t, err, "equivalent canonical intent replays the existing operation")
			require.Equal(t, op.ID, replayed.ID)
		})
	}
}

func TestFleetInvalidAttributeValuesAreRefusedAtPlanAndCreate(t *testing.T) {
	for _, tc := range []struct {
		name, valueType, value string
	}{
		{"boolean", "BOOLEAN", "maybe"},
		{"double", "DOUBLE", "not-a-number"},
		{"non-finite", "DOUBLE", "NaN"},
		{"overflow", "LONG", "12345678901234567890"},
		{"oversize", "STRING", strings.Repeat("x", MaxAttributeValueBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			armed(t)
			api, ctx := fleetFixture(t, 1)
			req := setReq("invalid", "d1")
			req.ConfigSet = &FleetConfigSetIntent{Key: "k", ValueType: tc.valueType, Value: sptr(tc.value)}
			_, err := api.PlanFleetOperation(ctx, req)
			requireFleetCode(t, err, FleetCodeInvalidRequest)
			_, err = api.CreateFleetOperation(ctx, req, "creator")
			requireFleetCode(t, err, FleetCodeInvalidRequest)
			var count int64
			require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestFleetPostStoresCanonicalValuesAndBooleanSelectorMatches(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, tc := range []struct {
			valueType, input, canonical string
		}{
			{"BOOLEAN", "1", "true"},
			{"BOOLEAN", "TRUE", "true"},
			{"DOUBLE", "0x1p3", "8"},
		} {
			name := tc.valueType + "/" + tc.input
			if legacy {
				name += "/legacy"
			}
			t.Run(name, func(t *testing.T) {
				input := tc.input
				if legacy {
					input = tc.canonical
				}
				e := postFixtureWithValueType(t, input, false, tc.valueType)
				if legacy {
					// Model an operation persisted before canonicalization was enforced.
					intent, err := json.Marshal(FleetConfigSetIntent{Key: "k", ValueType: tc.valueType, Value: sptr(tc.input)})
					require.NoError(t, err)
					require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperation{ID: e.op.ID}).Update("intent", string(intent)).Error)
				}
				require.Equal(t, FleetPosted, e.post(t).Outcome)
				a, present := attrOf(t, e)
				require.True(t, present)
				require.Equal(t, tc.canonical, a.Value.String)
				var posted fleetValueImage
				require.NoError(t, json.Unmarshal([]byte(e.reload(t).PostedValue.String), &posted))
				require.Equal(t, tc.canonical, *posted.Value, "the reversal receipt also uses canonical text")
				if tc.valueType == "BOOLEAN" {
					require.Equal(t, []string{e.dev.Token}, browseMatches(t, e.api, e.ctx, `attr["k"] == true`))
				}
			})
		}
	}
}

func TestFleetPostEmitsOneCanonicalAttributeFactAfterCommit(t *testing.T) {
	e := postFixtureWithValueType(t, "0x1p3", false, "DOUBLE")
	cap := &captureAttr{}
	e.api.DeviceAttributePublisher = cap
	require.Equal(t, FleetPosted, e.post(t).Outcome)
	require.Len(t, cap.events, 1)
	a, present := attrOf(t, e)
	require.True(t, present)
	require.Equal(t, "8", a.Value.String)
	ev := cap.events[0]
	require.Equal(t, e.dev.Token, ev.DeviceToken)
	require.Equal(t, "k", ev.AttrKey)
	require.Equal(t, string(AttributeScopeShared), ev.Scope)
	require.Equal(t, float64(8), ev.Value)
	require.False(t, ev.Removed)
	require.True(t, a.LastUpdated.Equal(ev.UpdatedAt), "the fact carries the committed attribute's timestamp")
	require.Equal(t, FleetPostLostRace, e.post(t).Outcome)
	require.Len(t, cap.events, 1, "a duplicate attempt emits no second fact")
}

func TestFleetFailedCommitEmitsNoAttributeFact(t *testing.T) {
	e := postFixtureWithValueType(t, "8", false, "DOUBLE")
	cap := &captureAttr{}
	e.api.DeviceAttributePublisher = cap
	failed := errors.New("commit failed")
	attributeBeforeCommitHook = func(*gorm.DB) error { return failed }
	t.Cleanup(func() { attributeBeforeCommitHook = nil })
	_, err := e.api.PostFleetLine(e.ctx, e.op.ID, e.line.ID, e.lease)
	require.ErrorIs(t, err, failed)
	require.Empty(t, cap.events)
	_, present := attrOf(t, e)
	require.False(t, present)
}

func TestFleetUnsetEmitsRemovedAttributeFact(t *testing.T) {
	e := postFixtureWithValueType(t, "", true, "DOUBLE")
	_, err := e.api.SetEntityAttribute(e.ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: e.dev.Token,
		Scope: "SHARED", AttrKey: "k", ValueType: "DOUBLE", Value: sptr("8")})
	require.NoError(t, err)
	cap := &captureAttr{}
	e.api.DeviceAttributePublisher = cap
	require.Equal(t, FleetPosted, e.post(t).Outcome)
	require.Len(t, cap.events, 1)
	require.True(t, cap.events[0].Removed)
	require.Equal(t, e.dev.Token, cap.events[0].DeviceToken)
	require.Equal(t, "k", cap.events[0].AttrKey)
	require.Equal(t, string(AttributeScopeShared), cap.events[0].Scope)
	require.False(t, cap.events[0].UpdatedAt.IsZero())
}

func TestFleetReversalToAbsentEmitsRemovedAttributeFact(t *testing.T) {
	f := reversalFixture(t, nil, false, 1)
	cap := &captureAttr{}
	f.api.DeviceAttributePublisher = cap
	_, results := f.reverseAndPost(t, f.ctx, revReq("orig", "undo"))
	require.Equal(t, FleetPosted, results[1].Outcome)
	require.Nil(t, f.k(t, f.devs[0]))
	require.Len(t, cap.events, 1)
	require.True(t, cap.events[0].Removed)
	require.Equal(t, f.devs[0].Token, cap.events[0].DeviceToken)
	require.Equal(t, "k", cap.events[0].AttrKey)
	require.Equal(t, string(AttributeScopeShared), cap.events[0].Scope)
	require.False(t, cap.events[0].UpdatedAt.IsZero())
}

func TestFleetReversalCanonicalizesALegacyBeforeImage(t *testing.T) {
	e := postFixtureWithValueType(t, "false", false, "BOOLEAN")
	_, err := e.api.SetEntityAttribute(e.ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: e.dev.Token,
		Scope: "SHARED", AttrKey: "k", ValueType: "BOOLEAN", Value: sptr("true")})
	require.NoError(t, err)
	// A pre-fix writer left this noncanonical spelling in the original's before-image.
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(&EntityAttribute{}).
		Where("entity_type = 'device' AND entity_id = ? AND scope = 'SHARED' AND attr_key = 'k'", e.dev.ID).
		Update("value", "TRUE").Error)
	require.Equal(t, FleetPosted, e.post(t).Outcome)
	f := &reversalEnv{api: e.api, ctx: e.ctx, devs: []*Device{e.dev}}
	f.finish(t, e.op)
	rev, results := f.reverseAndPost(t, e.ctx, revReq("p1", "undo"))
	require.Equal(t, FleetPosted, results[1].Outcome)
	a, present := attrOf(t, e)
	require.True(t, present)
	require.Equal(t, "true", a.Value.String)
	var posted fleetValueImage
	require.NoError(t, json.Unmarshal([]byte(f.line(t, rev, 1).PostedValue.String), &posted))
	require.Equal(t, "true", *posted.Value)
	require.Equal(t, []string{e.dev.Token}, browseMatches(t, e.api, e.ctx, `attr["k"] == true`))
}
