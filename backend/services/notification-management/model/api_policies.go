// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

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

	var created *NotificationPolicy
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		policy := &NotificationPolicy{
			TokenReference: rdb.TokenReference{Token: request.Token},
			NamedEntity: rdb.NamedEntity{
				Name:        rdb.NullStrOf(request.Name),
				Description: rdb.NullStrOf(request.Description),
			},
			MetadataEntity:  rdb.MetadataEntity{Metadata: rdb.MetadataStrOf(request.Metadata)},
			DeviceTypeToken: rdb.NullStrOf(request.DeviceTypeToken),
			ThrottleSeconds: nullInt64OfInt32(request.ThrottleSeconds),
			Enabled:         request.Enabled,
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

// UpdateNotificationPolicy updates a policy header and replaces its rule set
// atomically.
func (api *Api) UpdateNotificationPolicy(ctx context.Context, token string,
	request *NotificationPolicyCreateRequest) (*NotificationPolicy, error) {
	if err := validateJSONObject(request.Metadata, "metadata"); err != nil {
		return nil, err
	}
	matches, err := api.NotificationPoliciesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	var updated *NotificationPolicy
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		policy := matches[0]
		policy.Token = request.Token
		policy.Name = rdb.NullStrOf(request.Name)
		policy.Description = rdb.NullStrOf(request.Description)
		policy.Metadata = rdb.MetadataStrOf(request.Metadata)
		policy.DeviceTypeToken = rdb.NullStrOf(request.DeviceTypeToken)
		policy.ThrottleSeconds = nullInt64OfInt32(request.ThrottleSeconds)
		policy.Enabled = request.Enabled
		if err := tx.Omit("Rules").Save(policy).Error; err != nil {
			return err
		}
		// Replace the rule set: drop the old rows, insert the new ones.
		if err := tx.Unscoped().Where("policy_id = ?", policy.ID).Delete(&NotificationRule{}).Error; err != nil {
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
// validating recipients as a JSON string array. It reads through tx, which already
// carries the tenant-scoped context, so the channel lookup is tenant-isolated.
func (api *Api) buildRules(tx *gorm.DB, policyId uint,
	requests []*NotificationRuleCreateRequest) ([]*NotificationRule, error) {
	rules := make([]*NotificationRule, 0, len(requests))
	for _, rr := range requests {
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
		rules = append(rules, &NotificationRule{
			PolicyId:  policyId,
			Severity:  rr.Severity,
			ChannelId: channel.ID,
			// Carry the resolved channel so the create/update response renders it
			// without a reload (reads preload it); the Create call Omits the
			// association so this pointer never re-saves the channel row.
			Channel:    channel,
			Recipients: rdb.MetadataStrOf(rr.Recipients),
		})
	}
	return rules, nil
}

// NotificationPoliciesById loads policies (with rules) by numeric id.
func (api *Api) NotificationPoliciesById(ctx context.Context, ids []uint) ([]*NotificationPolicy, error) {
	found := make([]*NotificationPolicy, 0)
	result := api.RDB.DB(ctx).Preload("Rules").Preload("Rules.Channel").Find(&found, ids)
	return found, result.Error
}

// NotificationPoliciesByToken loads policies (with rules) by token.
func (api *Api) NotificationPoliciesByToken(ctx context.Context, tokens []string) ([]*NotificationPolicy, error) {
	found := make([]*NotificationPolicy, 0)
	result := api.RDB.DB(ctx).Preload("Rules").Preload("Rules.Channel").Find(&found, "token in ?", tokens)
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
