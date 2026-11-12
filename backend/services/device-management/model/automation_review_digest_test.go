// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func reviewSnapshot() *ProfileSnapshot {
	return &ProfileSnapshot{
		Rules: []*DetectionRule{{TokenReference: rdb.TokenReference{Token: "r1"}, Enabled: true, Definition: datatypes.JSON(`{"metric":"temp_c","threshold":26}`)}},
		Metrics: []*MetricDefinition{{MetricKey: "temp_c", DataType: "DOUBLE", Expression: sql.NullString{String: "raw * 2.0", Valid: true}},
			{MetricKey: "raw", DataType: "DOUBLE"}, {MetricKey: "unread", DataType: "DOUBLE"}},
		Commands: []*CommandDefinition{{CommandKey: "boost"}},
	}
}

func reviewSnapshotBytes(t *testing.T, s *ProfileSnapshot) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return b
}

func TestSnapshotDigestNormalizesStorageOnly(t *testing.T) {
	a, err := SnapshotDigest([]byte(`{"rules":[{"Definition":{"n":1e2},"TenantId":"a","ID":5,"UpdatedAt":"old"}]}`))
	require.NoError(t, err)
	b, err := SnapshotDigest([]byte(` { "commands": [], "metrics":null, "rules":[{"UpdatedAt":"new","ID":90,"TenantId":"b","Definition":{"n":100.00}}] } `))
	require.NoError(t, err)
	require.Equal(t, a, b)
	require.Len(t, a, 71)
	_, err = SnapshotDigest([]byte(`{"metrics":{}}`))
	require.Error(t, err)
	_, err = SnapshotDigest([]byte(`{} {}`))
	require.Error(t, err)
	base := reviewSnapshot()
	raw := reviewSnapshotBytes(t, base)
	want, err := SnapshotDigest(raw)
	require.NoError(t, err)
	var generic map[string]any
	require.NoError(t, json.Unmarshal(raw, &generic))
	roundtrip, err := json.MarshalIndent(generic, "", "  ")
	require.NoError(t, err)
	got, err := SnapshotDigest(roundtrip)
	require.NoError(t, err)
	require.Equal(t, want, got)
	base.Rules[0].UpdatedAt = time.Now()
	got, err = SnapshotDigest(reviewSnapshotBytes(t, base))
	require.NoError(t, err)
	require.Equal(t, want, got)
	for _, tc := range []struct {
		name string
		edit func(*ProfileSnapshot)
	}{
		{"definition", func(s *ProfileSnapshot) { s.Rules[0].Definition = datatypes.JSON(`{"threshold":25.5}`) }},
		{"enabled", func(s *ProfileSnapshot) { s.Rules[0].Enabled = false }},
		{"scope", func(s *ProfileSnapshot) {
			token, version := "group", int32(1)
			s.Rules[0].EntityGroupToken, s.Rules[0].EntityGroupVersion = &token, &version
		}},
		{"name", func(s *ProfileSnapshot) { s.Rules[0].Name = sql.NullString{String: "renamed", Valid: true} }},
		{"expression", func(s *ProfileSnapshot) { s.Metrics[0].Expression.String = "raw * 3.0" }},
		{"command", func(s *ProfileSnapshot) { s.Commands[0].Impact = "DISRUPTIVE" }},
		{"decoder", func(s *ProfileSnapshot) { s.Decoder = json.RawMessage(`{"language":"javascript"}`) }},
		{"order", func(s *ProfileSnapshot) { s.Metrics[0], s.Metrics[1] = s.Metrics[1], s.Metrics[0] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := reviewSnapshot()
			tc.edit(s)
			got, err := SnapshotDigest(reviewSnapshotBytes(t, s))
			require.NoError(t, err)
			require.NotEqual(t, want, got)
		})
	}
}

func TestReviewClassification(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		edit       func(*ProfileSnapshot)
		changes    []ProfileChange
		reasons    []string
	}{
		{"one rule", ReviewKindRuleChange, func(s *ProfileSnapshot) { s.Rules[0].Enabled = false },
			[]ProfileChange{{Area: "RULE", Key: "r1", Change: "MODIFIED", Automation: true}}, []string{}},
		{"two rules", ReviewKindUnreviewable, func(s *ProfileSnapshot) {
			s.Rules[0].Enabled = false
			s.Rules = append(s.Rules, &DetectionRule{TokenReference: rdb.TokenReference{Token: "r2"}, Definition: datatypes.JSON(`{}`)})
		},
			[]ProfileChange{{Area: "RULE", Key: "r1", Change: "MODIFIED", Automation: true}, {Area: "RULE", Key: "r2", Change: "ADDED", Automation: true}}, []string{"rules r1 and r2 both change"}},
		{"read metric", ReviewKindUnreviewable, func(s *ProfileSnapshot) { s.Metrics[0].Expression.String = "raw * 3.0" },
			[]ProfileChange{{Area: "METRIC", Key: "temp_c", Change: "MODIFIED", Automation: true}}, []string{"metric temp_c, which a rule reads, changes"}},
		{"transitive metric", ReviewKindUnreviewable, func(s *ProfileSnapshot) { s.Metrics[1].DataType = "INT" },
			[]ProfileChange{{Area: "METRIC", Key: "raw", Change: "MODIFIED", Automation: true}}, []string{"metric raw, which a rule reads, changes"}},
		{"unread metric", ReviewKindNoAutomationChange, func(s *ProfileSnapshot) { s.Metrics[2].DataType = "INT" },
			[]ProfileChange{{Area: "METRIC", Key: "unread", Change: "MODIFIED"}}, []string{}},
		{"ride along", ReviewKindRuleChange, func(s *ProfileSnapshot) { s.Rules[0].Enabled = false; s.Metrics[2].DataType = "INT" },
			[]ProfileChange{{Area: "METRIC", Key: "unread", Change: "MODIFIED"}, {Area: "RULE", Key: "r1", Change: "MODIFIED", Automation: true}}, []string{}},
		{"decoder", ReviewKindNoAutomationChange, func(s *ProfileSnapshot) { s.Decoder = json.RawMessage(`{"v":1}`) },
			[]ProfileChange{{Area: "DECODER", Change: "ADDED"}}, []string{}},
		{"description", ReviewKindNoAutomationChange, func(s *ProfileSnapshot) { s.Rules[0].Description = sql.NullString{String: "help", Valid: true} },
			[]ProfileChange{{Area: "RULE", Key: "r1", Change: "MODIFIED"}}, []string{}},
		{"added", ReviewKindRuleChange, func(s *ProfileSnapshot) {
			s.Rules = append(s.Rules, &DetectionRule{TokenReference: rdb.TokenReference{Token: "r2"}, Definition: datatypes.JSON(`{}`)})
		},
			[]ProfileChange{{Area: "RULE", Key: "r2", Change: "ADDED", Automation: true}}, []string{}},
		{"removed", ReviewKindRuleChange, func(s *ProfileSnapshot) { s.Rules = nil },
			[]ProfileChange{{Area: "RULE", Key: "r1", Change: "REMOVED", Automation: true}}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, draft := reviewSnapshot(), reviewSnapshot()
			tc.edit(draft)
			got, err := classifyAutomationReview(base, draft)
			require.NoError(t, err)
			require.Equal(t, tc.kind, got.Kind)
			require.Equal(t, tc.changes, got.Changes)
			require.Equal(t, tc.reasons, got.Reasons)
			if tc.kind == ReviewKindRuleChange {
				if tc.name == "added" {
					require.Equal(t, "r2", got.RuleToken)
					require.Nil(t, got.Base)
				} else {
					require.Equal(t, "r1", got.RuleToken)
				}
				if tc.name == "removed" {
					require.Nil(t, got.Draft)
				}
			}
		})
	}
}

func TestConstantMetricsFailClosedAndCloseAcrossVersions(t *testing.T) {
	base := &ProfileSnapshot{Metrics: []*MetricDefinition{
		{MetricKey: "raw", DataType: "DOUBLE"},
		{MetricKey: "scaled", DataType: "DOUBLE", Expression: sql.NullString{String: "raw * device.gain", Valid: true}},
		{MetricKey: "next", DataType: "DOUBLE", Expression: sql.NullString{String: "scaled + 1.0", Valid: true}},
		{MetricKey: "broken", DataType: "DOUBLE", Expression: sql.NullString{String: "!!!", Valid: true}},
		{MetricKey: "plain", DataType: "DOUBLE", Expression: sql.NullString{String: "raw + 2.0", Valid: true}},
	}}
	draft := &ProfileSnapshot{Metrics: []*MetricDefinition{
		{MetricKey: "next", DataType: "DOUBLE"},
		{MetricKey: "later", DataType: "DOUBLE", Expression: sql.NullString{String: "next * 2.0", Valid: true}},
	}}
	require.Equal(t, []string{"broken", "later", "next", "scaled"}, constantMetrics(base, draft))
}

// This value pins the approval binding independently of the implementation's encoder.
func TestReviewDigestBinding(t *testing.T) {
	token := "r1"
	r := &AutomationReview{TenantScoped: rdb.TenantScoped{TenantId: "acme"}, Kind: ReviewKindRuleChange, RuleToken: &token,
		DraftDigest: "sha256:draft", BaselineActivationId: 7, BaselineDigest: "sha256:base", ScopeDigest: "sha256:scope",
		StreamBounds:       datatypes.JSON(`[{"role":"INCIDENT","stream":"resolved","firstSeq":"12","lastSeq":"19"}]`),
		ExpectationsDigest: "sha256:expect", ResultDigest: "sha256:result", InputDigest: "sha256:input", Outcome: ReviewOutcomePassed,
		ProfileChanges: datatypes.JSON(`[{"area":"RULE","key":"r1","change":"MODIFIED","automation":true}]`)}
	got, err := reviewDigestOf(r, "profile")
	require.NoError(t, err)
	require.Equal(t, "sha256:4aef3536c1b032b49e0379ca35460a22476f95636028e61be3c86079ac75ddc7", got)
	r.BaselineActivationId++
	changed, err := reviewDigestOf(r, "profile")
	require.NoError(t, err)
	require.NotEqual(t, got, changed)
}
