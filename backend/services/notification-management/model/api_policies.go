// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/sqlnull"
	"gorm.io/gorm"
)

// CreateNotificationPolicy creates a policy and its rule set atomically. Each
// rule's channel is resolved by token; an unknown channel token fails the whole
// write (no partial policy is left behind).
func (api *Api) CreateNotificationPolicy(ctx context.Context,
	request *NotificationPolicyCreateRequest) (*NotificationPolicy, error) {
	if err := validateJSONObject(request.Metadata, "metadata"); err != nil {
		return nil, err
	}
	if err := validateDeviceTypeScoping(request.DeviceTypeToken); err != nil {
		return nil, err
	}

	// Converted out here, beside the validation it duplicates, rather than inside the
	// transaction. validateJSONObject above is strictly stronger — it requires an OBJECT,
	// not merely valid JSON — so this can never be the thing that fails; opening a
	// transaction to discover that would be work for an outcome already decided.
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}

	var created *NotificationPolicy
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		policy := &NotificationPolicy{
			TokenReference: rdb.TokenReference{Token: request.Token},
			NamedEntity: rdb.NamedEntity{
				Name:        rdb.NullStrOf(request.Name),
				Description: rdb.NullStrOf(request.Description),
			},
			MetadataEntity:       rdb.MetadataEntity{Metadata: metadataJSON},
			DeviceTypeToken:      rdb.NullStrOf(request.DeviceTypeToken),
			ThrottleSeconds:      sqlnull.Int64FromInt32(request.ThrottleSeconds),
			EscalateAfterSeconds: sqlnull.Int64FromInt32(request.EscalateAfterSeconds),
			MaxEscalations:       sqlnull.Int64FromInt32(request.MaxEscalations),
			Enabled:              request.Enabled,
		}
		if err := tx.Create(policy).Error; err != nil {
			return err
		}
		rules, err := api.buildRules(tx, policy.ID, request.Rules)
		if err != nil {
			return err
		}
		if len(rules) > 0 {
			// Omit the Channel association: the rules carry a resolved Channel
			// pointer for the response, but the channel row already exists and must
			// not be re-saved by GORM's belongs-to upsert.
			if err := tx.Omit("Channel").Create(&rules).Error; err != nil {
				return err
			}
		}
		policy.Rules = derefRules(rules)
		created = policy
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// UpdateNotificationPolicy applies a PARTIAL update to the policy named by the token
// argument: a field the request omits is left alone, an explicit null clears it, and a
// value sets it. The input carries no token, so a policy's identity cannot move.
//
// 🔴 AN ABSENT `rules` LEAVES THE RULE SET EXACTLY AS IT IS — rows, ids and all. This is
// the substantive change. The previous shape ran the delete-and-reinsert unconditionally,
// so ANY edit — a name, a throttle, a metadata key — destroyed every rule and recreated
// it, and an edit that simply did not mention rules emptied the policy and returned
// success. For the service that carries alarms to humans, that is an alerting outage
// spelled as a metadata edit.
//
// 🔴 EVERYTHING THAT CAN REFUSE RESOLVES BEFORE ANYTHING IS WRITTEN. Malformed metadata,
// a cleared `enabled`, an unknown channel token or a mistyped severity inside a rule all
// fail the WHOLE update. The rule-set half is inside the transaction with the header
// write, so a rule that buildRules refuses rolls the header back with it.
//
// When expectedUpdatedAt is non-nil it is an optimistic-concurrency precondition, the
// same one updateDashboard, updateConnector and updateAiProvider take and enforced by the
// same core code (rdb.RefuseIfMoved, rdb.UpdateIfUnmoved): the update is refused with
// ErrConflict, and nothing is written, if the policy's updated_at is no longer the one
// the caller names. It matters most here because a `rules` list replaces the whole rule
// set, so two operators saving at once would otherwise silently lose one set. Without it
// the last write wins, as it always has.
//
// Every guarded save writes the header row and so moves updated_at — a rules-only edit
// too, which is what makes a rule change stale every other editor's copy. That includes
// an update that names nothing: this is the connectors and AI-providers shape, not the
// dashboard one, where an empty update writes nothing at all.
//
// The response is always the policy as re-read after the commit, so its updatedAt is the
// STORED one and can be sent straight back as the next precondition. The in-memory value
// gorm leaves behind is not: PostgreSQL keeps microseconds of the nanoseconds it was sent.
func (api *Api) UpdateNotificationPolicy(ctx context.Context, token string,
	request *NotificationPolicyUpdateRequest, expectedUpdatedAt *string) (*NotificationPolicy, error) {
	matches, err := api.NotificationPoliciesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	policy := matches[0]

	enabled, err := request.Enabled.ApplyToRequired("enabled", policy.Enabled)
	if err != nil {
		return nil, err
	}
	metadata := request.Metadata.ApplyTo(dcgraphql.MetadataStr(policy.Metadata))
	if err := validateJSONObject(metadata, "metadata"); err != nil {
		return nil, err
	}
	// Converted out here, beside the validation it duplicates, rather than inside the
	// transaction. validateJSONObject above is strictly stronger — it requires an OBJECT,
	// not merely valid JSON — so this can never be the thing that fails; opening a
	// transaction to discover that would be work for an outcome already decided.
	metadataJSON, err := rdb.JSONInputOf("metadata", metadata)
	if err != nil {
		return nil, err
	}

	requestedRules, replaceRules := request.Rules.Requested()

	// The rule checks that need nothing but the request, run before the precondition so a
	// malformed request is refused as malformed whoever else is writing — reporting it as
	// stale would send the caller off to reload and retry a request that can never succeed.
	// This covers the VALUE checks only. The unknown-channel check needs the transaction,
	// so it stays in buildRules, and a stale request naming an unknown channel is refused
	// as stale; the retry then reports the channel. buildRules repeats these two checks:
	// it is the one choke point create and update share, and this is only an early-out.
	if replaceRules {
		for _, rr := range requestedRules {
			if err := validateSeverity(rr.Severity); err != nil {
				return nil, err
			}
			if err := validateStringArray(rr.Recipients, "recipients"); err != nil {
				return nil, err
			}
		}
	}

	if err := rdb.RefuseIfMoved(policy.UpdatedAt, expectedUpdatedAt, ErrConflict); err != nil {
		return nil, err
	}
	// Captured before the transaction: the header write below overwrites UpdatedAt in
	// memory, and the guard must name the version that was READ.
	readAt := policy.UpdatedAt

	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		policy.Name = request.Name.ApplyToNullString(policy.Name)
		policy.Description = request.Description.ApplyToNullString(policy.Description)
		policy.Metadata = metadataJSON
		policy.ThrottleSeconds = request.ThrottleSeconds.ApplyToNullInt64(policy.ThrottleSeconds)
		policy.EscalateAfterSeconds = request.EscalateAfterSeconds.ApplyToNullInt64(policy.EscalateAfterSeconds)
		policy.MaxEscalations = request.MaxEscalations.ApplyToNullInt64(policy.MaxEscalations)
		policy.Enabled = enabled
		if expectedUpdatedAt == nil {
			// No precondition: the unconditional write, last write wins.
			if err := rdb.AdvancingFrom(tx, readAt).Omit("Rules").Save(policy).Error; err != nil {
				return err
			}
		} else {
			// The FIRST statement in the transaction, before the rule set is touched: a
			// refusal returns before the delete below, and the rollback covers the rest.
			//
			// The map names every header column this update can change, so it is never
			// empty, and each value is the folded one — an omitted field writes back what
			// was read, a cleared one writes NULL, and false and 0 are written rather than
			// skipped as a struct update would.
			if err := rdb.UpdateIfUnmoved(tx, policy, readAt, map[string]any{
				"name":                   policy.Name,
				"description":            policy.Description,
				"metadata":               policy.Metadata,
				"throttle_seconds":       policy.ThrottleSeconds,
				"escalate_after_seconds": policy.EscalateAfterSeconds,
				"max_escalations":        policy.MaxEscalations,
				"enabled":                policy.Enabled,
			}, ErrConflict); err != nil {
				return err
			}
		}
		if !replaceRules {
			// The caller said nothing about rules, so the stored rows stay as they are.
			return nil
		}
		// Replace the rule set: drop the old rows, insert the new ones.
		if err := tx.Unscoped().Where("policy_id = ?", policy.ID).Delete(&NotificationRule{}).Error; err != nil {
			return err
		}
		rules, err := api.buildRules(tx, policy.ID, requestedRules)
		if err != nil {
			return err
		}
		if len(rules) > 0 {
			// Omit the Channel association: the rules carry a resolved Channel
			// pointer for the response, but the channel row already exists and must
			// not be re-saved by GORM's belongs-to upsert.
			if err := tx.Omit("Channel").Create(&rules).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Re-read by the token ARGUMENT, the only thing that names the row: the response must
	// carry the stored updatedAt (see above) and the stored rule set.
	reloaded, err := api.NotificationPoliciesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(reloaded) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return reloaded[0], nil
}

// buildRules resolves each rule request to a NotificationRule owned by policyId,
// resolving the channel token to its id (fails closed on an unknown channel) and
// validating the severity against the alarm tier vocabulary and recipients as a JSON
// string array. It reads through tx, which already carries the tenant-scoped context,
// so the channel lookup is tenant-isolated.
//
// It is the single choke point for rule validation because BOTH policy create and
// policy update build their rules here — an update that NAMES a rule set replaces the
// stored one wholesale, so a check placed on the create path alone would let an edit
// reintroduce exactly what the create refused. Making `rules` optional did not add a
// second path: an absent rule set builds nothing rather than building it somewhere else.
func (api *Api) buildRules(tx *gorm.DB, policyId uint,
	requests []*NotificationRuleCreateRequest) ([]*NotificationRule, error) {
	rules := make([]*NotificationRule, 0, len(requests))
	for _, rr := range requests {
		if err := validateSeverity(rr.Severity); err != nil {
			return nil, err
		}
		if err := validateStringArray(rr.Recipients, "recipients"); err != nil {
			return nil, err
		}
		channel := &NotificationChannel{}
		if err := tx.Where("token = ?", rr.ChannelToken).First(channel).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil, fmt.Errorf("rule references unknown channel token %q", rr.ChannelToken)
			}
			return nil, err
		}
		recipientsJSON, err := rdb.JSONInputOf("recipients", rr.Recipients)
		if err != nil {
			return nil, err
		}
		rules = append(rules, &NotificationRule{
			PolicyId:  policyId,
			Severity:  rr.Severity,
			ChannelId: channel.ID,
			// Carry the resolved channel so the create response renders it without a
			// reload (reads preload it; the update response is a reload); the Create
			// call Omits the association so this pointer never re-saves the channel row.
			Channel:    channel,
			Recipients: recipientsJSON,
		})
	}
	return rules, nil
}

// NotificationPoliciesById loads policies (with rules) by numeric id.
func (api *Api) NotificationPoliciesById(ctx context.Context, ids []uint) ([]*NotificationPolicy, error) {
	return rdb.FindByIds[NotificationPolicy](api.RDB.DB(ctx).Preload("Rules").Preload("Rules.Channel"), ids)
}

// NotificationPoliciesByToken loads policies (with rules) by token.
func (api *Api) NotificationPoliciesByToken(ctx context.Context, tokens []string) ([]*NotificationPolicy, error) {
	found := make([]*NotificationPolicy, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx).Preload("Rules").Preload("Rules.Channel"), &found, "token", tokens); err != nil {
		return nil, err
	}
	return found, nil
}

// EnabledNotificationPolicies loads every enabled policy (with rules + channels) for
// the caller tenant. It is the dispatcher's read path (N.C): unpaginated because the
// dispatcher must weigh all of a tenant's policies against each alarm, and a tenant's
// policy set is small operator-authored configuration, not device-scale data.
func (api *Api) EnabledNotificationPolicies(ctx context.Context) ([]*NotificationPolicy, error) {
	found := make([]*NotificationPolicy, 0)
	result := api.RDB.DB(ctx).Preload("Rules").Preload("Rules.Channel").
		Where("enabled = ?", true).Find(&found)
	return found, result.Error
}

// NotificationPolicies searches policies (with rules) by criteria.
func (api *Api) NotificationPolicies(ctx context.Context,
	criteria NotificationPolicySearchCriteria) (*NotificationPolicySearchResults, error) {
	results := make([]NotificationPolicy, 0)
	db, pag := api.RDB.ListOf(ctx, &NotificationPolicy{}, func(result *gorm.DB) *gorm.DB {
		if criteria.DeviceTypeToken != nil {
			result = result.Where("device_type_token = ?", *criteria.DeviceTypeToken)
		}
		if criteria.Enabled != nil {
			result = result.Where("enabled = ?", *criteria.Enabled)
		}
		return result.Preload("Rules").Preload("Rules.Channel")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &NotificationPolicySearchResults{Results: results, Pagination: pag}, nil
}

// DeleteNotificationPolicy hard-deletes a policy and its rules atomically.
func (api *Api) DeleteNotificationPolicy(ctx context.Context, token string) (bool, error) {
	matches, err := api.NotificationPoliciesByToken(ctx, []string{token})
	if err != nil {
		return false, err
	}
	if len(matches) == 0 {
		return false, nil
	}
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("policy_id = ?", matches[0].ID).Delete(&NotificationRule{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Where("token = ?", token).Delete(&NotificationPolicy{}).Error
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// countRulesForChannel counts routing rules that reference the channel id. It is
// tenant-scoped by the query callback, so it only sees the caller's own rules.
func (api *Api) countRulesForChannel(ctx context.Context, channelId uint) (int64, error) {
	var n int64
	err := api.RDB.DB(ctx).Model(&NotificationRule{}).Where("channel_id = ?", channelId).Count(&n).Error
	return n, err
}

// derefRules flattens a slice of rule pointers into values for the returned
// aggregate (GraphQL resolvers read NotificationPolicy.Rules by value).
func derefRules(rules []*NotificationRule) []NotificationRule {
	out := make([]NotificationRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, *r)
	}
	return out
}
