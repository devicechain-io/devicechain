// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func TestRuleDoorValidation(t *testing.T) {
	door, err := ruleSaveDoor(nil)
	require.NoError(t, err)
	require.Equal(t, AuthoredVia("API"), door)
	for _, value := range []AuthoredVia{"FORM", "CANVAS", "DESCRIBE", "PRODUCT_AS_CODE", "API", "UNKNOWN"} {
		door, err := ruleSaveDoor(&value)
		require.NoError(t, err)
		require.Equal(t, value, door)
		var decoded AuthoredVia
		require.NoError(t, decoded.UnmarshalGraphQL(string(value)))
		require.Equal(t, value, decoded)
	}
	for _, value := range []AuthoredVia{"AGENT_PROPOSAL", "other", "form", ""} {
		_, err := ruleSaveDoor(&value)
		require.Error(t, err)
		var decoded AuthoredVia
		require.Error(t, decoded.UnmarshalGraphQL(string(value)))
	}
	var decoded AuthoredVia
	require.Error(t, decoded.UnmarshalGraphQL(7))
}

func TestRuleProvenanceStoredHistory(t *testing.T) {
	for _, raw := range []datatypes.JSON{nil, datatypes.JSON(`null`), datatypes.JSON(`[]`)} {
		values, err := AuthoredViaValues(raw)
		require.NoError(t, err)
		require.Equal(t, []AuthoredVia{"UNKNOWN"}, values)
	}
	for _, raw := range []datatypes.JSON{datatypes.JSON(`{}`), datatypes.JSON(`[`), datatypes.JSON(`["AGENT_PROPOSAL"]`)} {
		_, err := AuthoredViaValues(raw)
		require.Error(t, err)
	}
	// Old stored keys are absent, rather than a newly marshalled zero field.
	snap, err := parseProfileSnapshot(datatypes.JSON(`{"rules":[{"Token":"legacy","Definition":{},"Enabled":false}]}`))
	require.NoError(t, err)
	raw, err := snapshotAuthoredVia(snap)
	require.NoError(t, err)
	require.JSONEq(t, `["UNKNOWN"]`, string(raw))
	raw, err = mergeAuthoredVia(nil, ruleDoorJSON("FORM"), ruleDoorJSON("DESCRIBE"), ruleDoorJSON("FORM"))
	require.NoError(t, err)
	require.Equal(t, `["DESCRIBE","FORM","UNKNOWN"]`, string(raw))
	// The version records all rules' doors, including a disabled Describe rule.
	raw, err = snapshotAuthoredVia(&ProfileSnapshot{Rules: []*DetectionRule{
		{AuthoredVia: ruleDoorJSON("FORM"), Enabled: true},
		{AuthoredVia: ruleDoorJSON("DESCRIBE"), Enabled: false},
	}})
	require.NoError(t, err)
	require.JSONEq(t, `["DESCRIBE","FORM"]`, string(raw))
	// No rules cannot claim that they came through any known door.
	raw, err = snapshotAuthoredVia(&ProfileSnapshot{})
	require.NoError(t, err)
	require.JSONEq(t, `["UNKNOWN"]`, string(raw))
}

// A competing save lands after the initial read, before this editor writes. The
// merge must use the database's current history, not that initial stale object.
func TestRuleProvenanceMergeUsesCurrentRow(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedProfileWithRule(t, api, ctx, "profile", "rule", false)
	db := api.RDB.Database
	injected := false
	const callback = "test:rule-provenance-newer-history"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if injected || tx.Statement.Table != "detection_rules" {
			return
		}
		injected = true
		require.NoError(t, api.RDB.DB(ctx).Model(&DetectionRule{}).Where("token = ?", "rule").Update("authored_via", ruleDoorJSON("DESCRIBE")).Error)
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	door := AuthoredVia("FORM")
	row, err := api.UpdateDetectionRule(ctx, "rule", &DetectionRuleUpdateRequest{AuthoredVia: OptionalAuthoredVia{Set: true, Value: &door}})
	require.NoError(t, err)
	require.True(t, injected)
	require.JSONEq(t, `["DESCRIBE","FORM"]`, string(row.AuthoredVia))
}

type provenanceValidator func(context.Context, []RuleToValidate) ([]DetectionRuleValidationFailure, error)

func (f provenanceValidator) ValidateDetectionRules(ctx context.Context, rules []RuleToValidate) ([]DetectionRuleValidationFailure, error) {
	return f(ctx, rules)
}

func TestRuleProvenancePublishUsesFrozenRules(t *testing.T) {
	api := newPublishEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedProfileWithRule(t, api, ctx, "profile", "rule", true)
	require.NoError(t, api.RDB.DB(ctx).Model(&DetectionRule{}).Where("token = ?", "rule").Update("authored_via", ruleDoorJSON("FORM")).Error)
	api.DetectionRuleValidator = provenanceValidator(func(ctx context.Context, _ []RuleToValidate) ([]DetectionRuleValidationFailure, error) {
		// Validation is outside the publish transaction. A new draft save here
		// must not be attributed to the already-built snapshot being validated.
		door := AuthoredVia("DESCRIBE")
		_, err := api.UpdateDetectionRule(ctx, "rule", &DetectionRuleUpdateRequest{AuthoredVia: OptionalAuthoredVia{Set: true, Value: &door}})
		return nil, err
	})
	version, err := api.PublishDeviceProfile(ctx, "profile", nil, nil, "publisher")
	require.NoError(t, err)
	require.JSONEq(t, `["FORM"]`, string(version.AuthoredVia))
	snap, err := parseProfileSnapshot(version.Snapshot)
	require.NoError(t, err)
	require.JSONEq(t, `["FORM"]`, string(snap.Rules[0].AuthoredVia))
	rows, err := api.DetectionRulesByToken(ctx, []string{"rule"})
	require.NoError(t, err)
	require.JSONEq(t, `["DESCRIBE","FORM"]`, string(rows[0].AuthoredVia))
	// Later saves change the draft only, never the provenance of this version.
	door := AuthoredVia("PRODUCT_AS_CODE")
	_, err = api.UpdateDetectionRule(ctx, "rule", &DetectionRuleUpdateRequest{AuthoredVia: OptionalAuthoredVia{Set: true, Value: &door}})
	require.NoError(t, err)
	versions, err := api.DeviceProfileVersions(ctx, "profile", nil)
	require.NoError(t, err)
	require.JSONEq(t, `["FORM"]`, string(versions[0].AuthoredVia))
}

func TestOptionalAuthoredViaStates(t *testing.T) {
	var absent OptionalAuthoredVia
	require.False(t, absent.Set)
	var null OptionalAuthoredVia
	require.NoError(t, null.UnmarshalGraphQL(nil))
	require.True(t, null.Set)
	require.Nil(t, null.Value)
	var value OptionalAuthoredVia
	require.NoError(t, value.UnmarshalGraphQL("DESCRIBE"))
	require.True(t, value.Set)
	require.Equal(t, AuthoredVia("DESCRIBE"), *value.Value)
	require.Error(t, value.UnmarshalGraphQL("AGENT_PROPOSAL"))
}
