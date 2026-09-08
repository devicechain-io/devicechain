// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
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
			ThrottleSeconds:      nullInt64OfInt32(request.ThrottleSeconds),
			EscalateAfterSeconds: nullInt64OfInt32(request.EscalateAfterSeconds),
			MaxEscalations:       nullInt64OfInt32(request.MaxEscalations),
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
func (api *Api) UpdateNotificationPolicy(ctx context.Context, token string,
	request *NotificationPolicyUpdateRequest) (*NotificationPolicy, error) {
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

	var updated *NotificationPolicy
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		policy.Name = rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(policy.Name)))
		policy.Description = rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(policy.Description)))
		policy.Metadata = metadataJSON
		policy.ThrottleSeconds = nullInt64OfInt32(
			request.ThrottleSeconds.ApplyTo(int32OfNullInt64(policy.ThrottleSeconds)))
		policy.EscalateAfterSeconds = nullInt64OfInt32(
			request.EscalateAfterSeconds.ApplyTo(int32OfNullInt64(policy.EscalateAfterSeconds)))
		policy.MaxEscalations = nullInt64OfInt32(
			request.MaxEscalations.ApplyTo(int32OfNullInt64(policy.MaxEscalations)))
		policy.Enabled = enabled
		if err := tx.Omit("Rules").Save(policy).Error; err != nil {
			return err
		}
		if !replaceRules {
			// The caller said nothing about rules, so the stored rows stay as they are.
			// policy.Rules already holds them: NotificationPoliciesByToken preloaded them
			// above, so the response renders the rule set the policy still has rather
			// than an empty one.
			updated = policy
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
		policy.Rules = derefRules(rules)
		updated = policy
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
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
			// Carry the resolved channel so the create/update response renders it
			// without a reload (reads preload it); the Create call Omits the
			// association so this pointer never re-saves the channel row.
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
	result := api.RDB.DB(ctx).Preload("Rules").Preload("Rules.Channel").Find(&found, "token in ?", tokens)
	return found, result.Error
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
