// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestKeyedDefinitionDeleteRetainsOriginalPredicate(t *testing.T) {
	for _, byToken := range []bool{false, true} {
		t.Run(map[bool]string{false: "profile", true: "token"}[byToken], func(t *testing.T) {
			r := newAutomationReviewRig(t, true)
			other, err := r.api.CreateDeviceProfile(r.ctx, &DeviceProfileCreateRequest{Token: "other"})
			require.NoError(t, err)
			db := r.api.RDB.DB(r.ctx)
			table, err := journalTable(db, &DetectionRule{})
			require.NoError(t, err)
			changed := false
			// SQLite omits FOR UPDATE. Change the selected row in the same
			// transaction just before deletion to exercise the predicate backstop.
			require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("test:move-selected-definition", func(tx *gorm.DB) {
				if changed || tx.Statement.Table != table {
					return
				}
				changed = true
				column, value := "device_profile_id", any(other.ID)
				if byToken {
					column, value = "token", "renamed-rule"
				}
				tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Model(&DetectionRule{}).
					Where("id = ?", r.rule.ID).UpdateColumn(column, value).Error)
			}))
			if byToken {
				deleted, err := r.api.DeleteDetectionRule(r.ctx, r.rule.Token)
				require.NoError(t, err)
				require.False(t, deleted, "a renamed definition no longer matches the requested token")
			} else {
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					return deleteProfileChildrenByKey[DetectionRule](tx, r.rule.DeviceProfileId)
				}))
			}
			require.True(t, changed, "the selected row must change before deletion")
			var retained DetectionRule
			require.NoError(t, db.First(&retained, r.rule.ID).Error, "a definition outside the original predicate must survive")
			if byToken {
				require.Equal(t, "renamed-rule", retained.Token)
			} else {
				require.Equal(t, other.ID, retained.DeviceProfileId)
			}
		})
	}
}

func TestDeleterOnAnotherProfileCanApprove(t *testing.T) {
	testDeleterOnAnotherProfileCanApprove(t, newAutomationReviewRig(t, true))
}

func testDeleterOnAnotherProfileCanApprove(t *testing.T, r *automationReviewRig) {
	t.Helper()
	seedProfileWithRule(t, r.api, r.ctx, "other", "other-rule", true)
	bob, person := reviewIdentity("bob")
	deleted, err := r.api.DeleteDetectionRule(bob, "other-rule")
	require.NoError(t, err)
	require.True(t, deleted)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	r.changeRule(t, "alice")
	row := r.run(t)
	approved, err := r.api.ApproveAutomationReview(bob, person, row.Token, row.ReviewDigest)
	require.NoError(t, err, "a deleter on another profile must not receive SELF_APPROVAL_REFUSED")
	require.Equal(t, ReviewStateApproved, approved.State)
	require.JSONEq(t, `["alice","before"]`, string(row.DraftAuthors))
}

func TestDeleterOnReviewedProfileCannotApprove(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	seedProfileWithRule(t, r.api, r.ctx, "other", "other-rule", true)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	bob, person := reviewIdentity("bob")
	deleted, err := r.api.DeleteDetectionRule(bob, r.rule.Token)
	require.NoError(t, err)
	require.True(t, deleted)
	later, _ := reviewIdentity("later-deleter")
	deleted, err = r.api.DeleteDetectionRule(later, "other-rule")
	require.NoError(t, err)
	require.True(t, deleted)
	row := r.run(t)
	_, err = r.api.ApproveAutomationReview(bob, person, row.Token, row.ReviewDigest)
	reviewErrorCode(t, err, "SELF_APPROVAL_REFUSED")
	require.JSONEq(t, `["before","bob"]`, string(row.DraftAuthors))
}

func TestLegacyReviewDeleteAuthors(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	seedProfileWithRule(t, r.api, r.ctx, "other", "old-other", true)
	dave, _ := reviewIdentity("dave")
	require.NoError(t, r.api.RDB.DB(dave).Where("token = ?", "old-other").Delete(&DetectionRule{}).Error)
	// This keyed write is the floor, even though it occurs after publication.
	r.changeRule(t, "alice")
	seedProfileWithRule(t, r.api, r.ctx, "third", "unkeyed-update", true)
	updater, _ := reviewIdentity("unrelated-updater")
	require.NoError(t, r.api.RDB.DB(updater).Model(&DetectionRule{}).Where("token = ?", "unkeyed-update").Update("enabled", false).Error)
	carol, person := reviewIdentity("carol")
	// Historical deletes and not-yet-upgraded pods use this condition-only statement.
	require.NoError(t, r.api.RDB.DB(carol).Where("token = ?", r.rule.Token).Delete(&DetectionRule{}).Error)
	// Prove the old-pod statement leaves a soft-deleted row and an unkeyed
	// journal event, rather than relying on a synthetic audit fixture.
	db := r.api.RDB.DB(carol)
	table, err := journalTable(db, &DetectionRule{})
	require.NoError(t, err)
	var events []rdb.AuditEvent
	require.NoError(t, db.Where("table_name = ? AND operation = ? AND actor = ?", table, "delete", "carol").Find(&events).Error)
	require.Len(t, events, 1)
	require.Equal(t, rdb.AuditCategoryMutation, events[0].Category)
	require.Empty(t, events[0].EntityPK)
	var removedRule DetectionRule
	require.NoError(t, db.Unscoped().First(&removedRule, r.rule.ID).Error)
	require.True(t, removedRule.DeletedAt.Valid)
	row := r.run(t)
	require.JSONEq(t, `["alice","before","carol"]`, string(row.DraftAuthors))
	_, err = r.api.ApproveAutomationReview(carol, person, row.Token, row.ReviewDigest)
	reviewErrorCode(t, err, "SELF_APPROVAL_REFUSED")
}

func TestMovedReviewRuleDoesNotTriggerLegacyFallback(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	seedProfileWithRule(t, r.api, r.ctx, "other", "other-rule", true)
	alice, _ := reviewIdentity("alice")
	target := "other"
	_, err := r.api.UpdateDetectionRule(alice, r.rule.Token, &DetectionRuleUpdateRequest{
		DeviceProfileToken: dcgraphql.OptionalString{Set: true, Value: &target},
	})
	require.NoError(t, err)
	bob, _ := reviewIdentity("bob")
	require.NoError(t, r.api.RDB.DB(bob).Where("token = ?", "other-rule").Delete(&DetectionRule{}).Error)
	row := r.run(t)
	require.JSONEq(t, `["alice","before"]`, string(row.DraftAuthors))
}

// Every product writer of these four tables must retain a key. Review attribution
// deliberately ignores unkeyed updates, so this watcher guards that assumption.
func TestProfileAuthoringJournalAlwaysKeyed(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	api, ctx := r.api, r.ctx
	_, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "watched"})
	require.NoError(t, err)
	name := "edited"
	named := dcgraphql.OptionalString{Set: true, Value: &name}
	_, err = api.UpdateDeviceProfile(ctx, "watched", &DeviceProfileUpdateRequest{Name: named, Location: OptionalLocationDeclaration{Set: true, Value: &LocationDeclaration{}}})
	require.NoError(t, err)
	_, err = api.SetDeviceProfileConfigurationDeclaration(ctx, "watched", []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	require.NoError(t, err)
	for _, token := range []string{"individual", "cascade"} {
		metric, err := api.CreateMetricDefinition(ctx, &MetricDefinitionCreateRequest{Token: token + "-metric", DeviceProfileToken: "watched", MetricKey: token, DataType: "DOUBLE"})
		require.NoError(t, err)
		_, err = api.UpdateMetricDefinition(ctx, metric.Token, &MetricDefinitionUpdateRequest{Name: named})
		require.NoError(t, err)
		command, err := api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{Token: token + "-command", DeviceProfileToken: "watched", CommandKey: token})
		require.NoError(t, err)
		_, err = api.UpdateCommandDefinition(ctx, command.Token, &CommandDefinitionUpdateRequest{Name: named})
		require.NoError(t, err)
		rule, err := api.CreateDetectionRule(ctx, &DetectionRuleCreateRequest{Token: token + "-rule", DeviceProfileToken: "watched", Definition: `{}`, Enabled: false})
		require.NoError(t, err)
		_, err = api.UpdateDetectionRule(ctx, rule.Token, &DetectionRuleUpdateRequest{Name: named})
		require.NoError(t, err)
		if token == "individual" {
			for _, del := range []struct {
				token string
				run   func(string) (bool, error)
			}{
				{metric.Token, func(token string) (bool, error) { return api.DeleteMetricDefinition(ctx, token) }},
				{command.Token, func(token string) (bool, error) { return api.DeleteCommandDefinition(ctx, token) }},
				{rule.Token, func(token string) (bool, error) { return api.DeleteDetectionRule(ctx, token) }},
			} {
				deleted, err := del.run(del.token)
				require.NoError(t, err)
				require.True(t, deleted)
				deleted, err = del.run(del.token)
				require.NoError(t, err)
				require.False(t, deleted)
			}
		}
	}
	_, err = api.SetDeviceProfileDecoder(ctx, "watched", []byte(`{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"telemetry","outputs":[{"key":"cascade","expr":"1.0"}]}],"vectors":[{"name":"one","input":{"frameHex":"01"},"expect":{"cascade":1}}]}`))
	require.NoError(t, err)
	_, err = api.PublishDeviceProfile(ctx, "watched", nil, nil, "creator")
	require.NoError(t, err)
	deleted, err := api.DeleteDeviceProfile(ctx, "watched")
	require.NoError(t, err)
	require.True(t, deleted)
	for _, want := range []struct {
		model  any
		labels []string
	}{
		{&MetricDefinition{}, []string{"individual-metric", "cascade-metric"}},
		{&CommandDefinition{}, []string{"individual-command", "cascade-command"}},
		{&DetectionRule{}, []string{"individual-rule", "cascade-rule"}},
		{&DeviceProfile{}, []string{"watched"}},
	} {
		table, err := journalTable(api.RDB.DB(ctx), want.model)
		require.NoError(t, err)
		var unkeyed int64
		require.NoError(t, api.RDB.DB(ctx).Model(&rdb.AuditEvent{}).Where("table_name = ? AND entity_pk = ?", table, "").Count(&unkeyed).Error)
		require.Zero(t, unkeyed, "all writes to %s must be attributable", table)
		var events []rdb.AuditEvent
		require.NoError(t, api.RDB.DB(ctx).Where("table_name = ? AND operation = ?", table, "delete").Find(&events).Error)
		require.Len(t, events, len(want.labels))
		var labels []string
		for _, event := range events {
			require.NotEmpty(t, event.EntityPK)
			require.EqualValues(t, 1, event.RowsAffected)
			labels = append(labels, event.EntityLabel)
		}
		require.ElementsMatch(t, want.labels, labels)
	}
}

func TestLegacyReviewDeleteFloorAcrossRemovedRows(t *testing.T) {
	for _, missingHistory := range []bool{false, true} {
		t.Run(map[bool]string{false: "minimum", true: "missing-history"}[missingHistory], func(t *testing.T) {
			r := newAutomationReviewRig(t, true)
			db := r.api.RDB.DB(r.ctx)
			table, err := journalTable(db, &DetectionRule{})
			require.NoError(t, err)
			if missingHistory {
				// An imported baseline can predate the journal entirely.
				require.NoError(t, db.Where("table_name = ?", table).Delete(&rdb.AuditEvent{}).Error)
			}
			carol, _ := reviewIdentity("carol")
			require.NoError(t, r.api.RDB.DB(carol).Where("token = ?", r.rule.Token).Delete(&DetectionRule{}).Error)
			second := &DetectionRule{DeviceProfileId: r.profile.ID, TokenReference: rdb.TokenReference{Token: "second"}, Definition: []byte(`{}`)}
			require.NoError(t, db.Create(second).Error)
			require.NoError(t, db.Where("token = ?", second.Token).Delete(&DetectionRule{}).Error)
			authors, err := r.api.draftAuthors(r.ctx, r.profile, 1,
				&ProfileSnapshot{Rules: []*DetectionRule{r.rule, second}}, &ProfileSnapshot{})
			require.NoError(t, err)
			require.Contains(t, authors, "carol", "the later row's last write cannot replace the earlier floor")
		})
	}
}
