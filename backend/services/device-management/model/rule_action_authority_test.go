// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func ruleActionClaims(authorities ...auth.Authority) context.Context {
	grants := []string{string(auth.DeviceWrite)}
	for _, a := range authorities {
		grants = append(grants, string(a))
	}
	return auth.WithClaims(core.WithTenant(context.Background(), "acme"), &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Authorities: grants})
}

func actionDefinition(kind string) string { return fmt.Sprintf(`{"actions":[{"type":%q}]}`, kind) }

func TestRuleActionAuthoringAuthority(t *testing.T) {
	for _, kind := range []string{"raiseAlarm", "sendCommand", "httpCall", "publish"} {
		for _, level := range []string{"device", "command", "connector", "full"} {
			t.Run(kind+"/"+level, func(t *testing.T) {
				api := newPublishEmitTestApi(t)
				ctx := ruleActionClaims()
				if level == "command" {
					ctx = ruleActionClaims(auth.CommandWrite)
				}
				if level == "connector" {
					ctx = ruleActionClaims(auth.ConnectorWrite)
				}
				if level == "full" {
					ctx = ruleActionClaims(auth.CommandWrite, auth.ConnectorWrite)
				}
				seedProfile(t, api, ctx, "p")
				allowed := kind == "raiseAlarm" || level == "full" || (kind == "sendCommand" && level == "command") || ((kind == "httpCall" || kind == "publish") && level == "connector")
				_, err := api.CreateDetectionRule(ctx, &DetectionRuleCreateRequest{Token: "new", DeviceProfileToken: "p", Definition: actionDefinition(kind), Enabled: false})
				if allowed {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, auth.ErrForbidden)
				}
				var count int64
				require.NoError(t, api.RDB.DB(ctx).Model(&DetectionRule{}).Where("token = ?", "new").Count(&count).Error)
				if allowed {
					require.EqualValues(t, 1, count)
				} else {
					require.Zero(t, count)
				}
				// Updating an alarm-only rule checks the folded replacement definition.
				_, err = api.CreateDetectionRule(ctx, &DetectionRuleCreateRequest{Token: "existing", DeviceProfileToken: "p", Definition: actionDefinition("raiseAlarm")})
				require.NoError(t, err)
				_, err = api.UpdateDetectionRule(ctx, "existing", &DetectionRuleUpdateRequest{Definition: dcgraphql.OptionalStringOf(actionDefinition(kind))})
				if allowed {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, auth.ErrForbidden)
				}
				rows, err := api.DetectionRulesByToken(ctx, []string{"existing"})
				require.NoError(t, err)
				want := actionDefinition("raiseAlarm")
				if allowed {
					want = actionDefinition(kind)
				}
				require.JSONEq(t, want, string(rows[0].Definition))
			})
		}
	}
}

func TestRuleActionPartialUpdateChecksEffectiveDefinition(t *testing.T) {
	api := newPublishEmitTestApi(t)
	full := ruleActionClaims(auth.CommandWrite, auth.ConnectorWrite)
	seedProfile(t, api, full, "p")
	_, err := api.CreateDetectionRule(full, &DetectionRuleCreateRequest{Token: "r", DeviceProfileToken: "p", Definition: actionDefinition("sendCommand")})
	require.NoError(t, err)
	_, err = api.UpdateDetectionRule(ruleActionClaims(), "r", &DetectionRuleUpdateRequest{Name: dcgraphql.OptionalStringOf("denied")})
	require.ErrorIs(t, err, auth.ErrForbidden)
	rows, err := api.DetectionRulesByToken(full, []string{"r"})
	require.NoError(t, err)
	require.False(t, rows[0].Name.Valid)
	_, err = api.UpdateDetectionRule(full, "r", &DetectionRuleUpdateRequest{Name: dcgraphql.OptionalStringOf("allowed")})
	require.NoError(t, err)
}

func TestRuleActionPublishAndRollbackAuthority(t *testing.T) {
	for _, kind := range []string{"raiseAlarm", "sendCommand", "httpCall", "publish"} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/enabled=%t", kind, enabled), func(t *testing.T) {
				api := newPublishEmitTestApi(t)
				low := ruleActionClaims()
				full := ruleActionClaims(auth.CommandWrite, auth.ConnectorWrite)
				profile := seedProfileWithRule(t, api, full, "p", "r", enabled)
				// Plant legacy data directly, bypassing the authoring gate.
				require.NoError(t, api.RDB.DB(full).Model(&DetectionRule{}).Where("device_profile_id = ?", profile.ID).Update("definition", datatypes.JSON(actionDefinition(kind))).Error)
				allowed := !enabled || kind == "raiseAlarm"
				_, err := api.PublishDeviceProfile(low, "p", nil, nil, "publisher")
				if allowed {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, auth.ErrForbidden)
				}
				var count int64
				require.NoError(t, api.RDB.DB(full).Model(&DeviceProfileVersion{}).Count(&count).Error)
				if allowed {
					require.EqualValues(t, 1, count)
				} else {
					require.Zero(t, count)
				}
				v, err := api.PublishDeviceProfile(full, "p", nil, nil, "publisher")
				require.NoError(t, err)
				// Move to an alarm-only version so rollback has an observable effect.
				require.NoError(t, api.RDB.DB(full).Model(&DetectionRule{}).Where("device_profile_id = ?", profile.ID).Update("definition", datatypes.JSON(actionDefinition("raiseAlarm"))).Error)
				latest, err := api.PublishDeviceProfile(low, "p", nil, nil, "publisher")
				require.NoError(t, err)
				_, err = api.RollbackDeviceProfile(low, "p", v.Version)
				if allowed {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, auth.ErrForbidden)
				}
				p, err := api.deviceProfileByToken(full, "p")
				require.NoError(t, err)
				want := latest.Version
				if allowed {
					want = v.Version
				}
				require.Equal(t, want, p.ActiveVersion.Int32)
				_, err = api.RollbackDeviceProfile(full, "p", v.Version)
				require.NoError(t, err)
			})
		}
	}
}

func TestRuleActionServiceClaimsAndMissingClaims(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	require.ErrorIs(t, authorizeRuleActions(ctx, []byte(actionDefinition("sendCommand"))), auth.ErrUnauthenticated)
	service := &auth.Claims{TokenType: auth.TokenTypeService, Authorities: []string{string(auth.DeviceWrite)}}
	require.ErrorIs(t, authorizeRuleActions(auth.WithClaims(ctx, service), []byte(actionDefinition("sendCommand"))), auth.ErrForbidden)
	service.Authorities = append(service.Authorities, string(auth.CommandWrite))
	require.NoError(t, authorizeRuleActions(auth.WithClaims(ctx, service), []byte(actionDefinition("sendCommand"))))
	require.NoError(t, authorizeRuleActions(ctx, []byte(actionDefinition("raiseAlarm"))))
}

func TestRuleActionReviewedPublishChecksPublisherNotApprover(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	r.rule.Definition = datatypes.JSON(`{"metric":"temp_c","threshold":25.5,"actions":[{"type":"sendCommand"}]}`)
	author, _ := reviewIdentity("author")
	require.NoError(t, r.api.RDB.DB(author).Save(r.rule).Error)
	// reviewIdentity grants device:write, but no command or connector authority.
	row := approveRigReview(t, r, r.run(t), "reviewer")
	_, err := publishRigReview(r, row)
	require.ErrorIs(t, err, auth.ErrForbidden)
	publisher, person := reviewIdentity("publisher")
	person.Claims.Authorities = append(person.Claims.Authorities, string(auth.CommandWrite))
	publisher = auth.WithClaims(publisher, person.Claims)
	_, err = r.api.PublishDeviceProfileReviewed(publisher, "profile", nil, nil, "publisher", &row.Token, nil)
	require.NoError(t, err)
}

func TestRuleActionReviewedPublishRechecksFrozenSnapshot(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	r.changeRule(t, "author")
	row := approveRigReview(t, r, r.run(t), "reviewer")
	reads := 0
	const callback = "test:actuating_rule_in_rebuilt_snapshot"
	db := r.api.RDB.DB(r.ctx)
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if tx.Error != nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "detection_rules" {
			return
		}
		rules, ok := tx.Statement.Dest.(*[]*DetectionRule)
		if !ok {
			return
		}
		reads++
		if reads == 2 {
			require.Len(t, *rules, 1)
			(*rules)[0].Definition = datatypes.JSON(`{"metric":"temp_c","threshold":25.5,"actions":[{"type":"sendCommand"}]}`)
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })
	_, err := publishRigReview(r, row)
	require.GreaterOrEqual(t, reads, 2, "must reach the in-transaction snapshot rebuild")
	require.ErrorIs(t, err, auth.ErrForbidden)
	profile, err := r.api.deviceProfileByToken(r.ctx, "profile")
	require.NoError(t, err)
	require.EqualValues(t, 1, profile.ActiveVersion.Int32)
}
