// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// JSONB rewrites whitespace and object order. Exercise the deployed migration
// chain and a real server: sqlite cannot prove the approval binding survives it.
func TestAutomationReviewPostgresEvidenceAndPublication(t *testing.T) {
	mgr := newPostgresRdbManager(t)
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	for _, table := range []string{
		"automation_reviews", "device_profile_activations", "device_profile_versions",
		"detection_rule_scope_refs", "detection_rules", "command_definitions", "metric_definitions", "device_profiles",
	} {
		require.NoError(t, sys.Exec(fmt.Sprintf(`TRUNCATE TABLE "device-management".%q CASCADE`, table)).Error, "truncate %s", table)
	}
	api := NewApi(mgr)
	before, _ := reviewIdentity("before-baseline")
	profile := seedProfileWithRule(t, api, before, "profile", "r1", true)
	var rule DetectionRule
	require.NoError(t, api.RDB.DB(before).Where("token = ?", "r1").First(&rule).Error)
	rule.Definition = datatypes.JSON(`{"metric":"temp_c","threshold":26}`)
	require.NoError(t, api.RDB.DB(before).Save(&rule).Error)
	baseline, err := api.PublishDeviceProfile(before, "profile", nil, nil, "before-baseline")
	require.NoError(t, err)

	editor, _ := reviewIdentity("rule-editor")
	rule.Definition = datatypes.JSON(`{"metric":"temp_c","threshold":25.5}`)
	require.NoError(t, api.RDB.DB(editor).Save(&rule).Error)
	// Keep whitespace, exponent spelling and a trailing newline: TEXT must keep
	// all bytes, independently of the JSONB evidence used in the review digest.
	result := "{\n  \"outcome\":\"PASSED\", \"value\":1e2, \"label\":\"é\"\n}\n"
	comparer := &fakeRuleComparer{response: &CompareResponse{Ok: true, Outcome: ReviewOutcomePassed,
		Result: result, ResultDigest: reviewHash([]byte(result)), InputDigest: "sha256:inputs", EngineBuild: "pg-test",
		Windows: []ReviewWindowResult{
			{Role: "INCIDENT", Stream: "resolved", FirstSeq: "18446744073709551614", LastSeq: "18446744073709551615"},
			{Role: "NORMAL", Stream: "resolved", FirstSeq: "20", LastSeq: "25"},
		}}}
	api.RuleComparer = comparer
	creator, person := reviewIdentity("review-creator")
	row, err := api.RunAutomationReview(creator, person, reviewRunInput())
	require.NoError(t, err)
	require.Equal(t, ReviewKindRuleChange, row.Kind)
	require.Equal(t, baseline.Version, row.BaselineVersion)
	require.Len(t, comparer.requests, 1)

	readBack, err := api.AutomationReviewByToken(creator, row.Token)
	require.NoError(t, err)
	require.Equal(t, row.ReviewDigest, readBack.ReviewDigest)
	digest, err := reviewDigestOf(readBack, profile.Token)
	require.NoError(t, err)
	require.Equal(t, readBack.ReviewDigest, digest, "JSONB streamBounds/profileChanges/scope round trip must not move the binding")
	require.JSONEq(t, string(row.StreamBounds), string(readBack.StreamBounds))
	require.JSONEq(t, string(row.ProfileChanges), string(readBack.ProfileChanges))
	require.JSONEq(t, string(row.Scope), string(readBack.Scope))
	require.Equal(t, result, readBack.Result)
	require.Equal(t, comparer.response.ResultDigest, readBack.ResultDigest)
	require.Equal(t, reviewHash([]byte(readBack.Result)), readBack.ResultDigest)
	var authors []string
	require.NoError(t, json.Unmarshal(readBack.DraftAuthors, &authors))
	require.Equal(t, []string{"rule-editor"}, authors, "pre-baseline edits must not exclude an otherwise independent approver")

	third, approver := reviewIdentity("third-person")
	approved, err := api.ApproveAutomationReview(third, approver, readBack.Token, readBack.ReviewDigest)
	require.NoError(t, err)
	require.Equal(t, ReviewStateApproved, approved.State)
	var approvals []ReviewApproval
	require.NoError(t, json.Unmarshal(approved.Approvals, &approvals))
	require.Len(t, approvals, 1)
	require.Equal(t, "third-person", approvals[0].Sub)

	published, err := api.PublishDeviceProfile(third, profile.Token, nil, nil, approver.Principal)
	require.NoError(t, err)
	var storedVersion DeviceProfileVersion
	require.NoError(t, api.RDB.DB(third).First(&storedVersion, published.ID).Error)
	require.Equal(t, baseline.Version+1, storedVersion.Version)
	snapshotDigest, err := SnapshotDigest(storedVersion.Snapshot)
	require.NoError(t, err)
	require.Equal(t, readBack.DraftDigest, snapshotDigest)

	table, err := journalTable(api.RDB.DB(creator), &AutomationReview{})
	require.NoError(t, err)
	for _, want := range []struct{ operation, actor string }{{"create", "review-creator"}, {"update", "third-person"}} {
		var events []rdb.AuditEvent
		require.NoError(t, api.RDB.DB(creator).Where("table_name = ? AND entity_pk = ? AND operation = ?",
			table, strconv.FormatUint(uint64(readBack.ID), 10), want.operation).Order("id DESC").Find(&events).Error)
		require.Len(t, events, 1)
		require.Equal(t, want.actor, events[0].Actor)
		require.Equal(t, "acme", events[0].TenantId)
		require.Equal(t, rdb.AuditCategoryMutation, events[0].Category)
		require.EqualValues(t, 1, events[0].RowsAffected)
	}
}
