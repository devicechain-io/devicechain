// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"fmt"

	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-notification-management/model"
	gql "github.com/graph-gophers/graphql-go"
)

// nullInt32 adapts a nullable bigint column to an optional GraphQL Int (int32).
func nullInt32(v sql.NullInt64) *int32 {
	if !v.Valid {
		return nil
	}
	n := int32(v.Int64)
	return &n
}

// NotificationPolicyResolver resolves a routing policy and its rules.
type NotificationPolicyResolver struct {
	M model.NotificationPolicy
	S *SchemaResolver
	C context.Context
}

func (r *NotificationPolicyResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *NotificationPolicyResolver) CreatedAt() *string { return util.FormatTime(r.M.CreatedAt) }

func (r *NotificationPolicyResolver) UpdatedAt() *string { return util.FormatTime(r.M.UpdatedAt) }

func (r *NotificationPolicyResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *NotificationPolicyResolver) Token() string { return r.M.Token }

func (r *NotificationPolicyResolver) Name() *string { return util.NullStr(r.M.Name) }

func (r *NotificationPolicyResolver) Description() *string { return util.NullStr(r.M.Description) }

func (r *NotificationPolicyResolver) DeviceTypeToken() *string {
	return util.NullStr(r.M.DeviceTypeToken)
}

func (r *NotificationPolicyResolver) ThrottleSeconds() *int32 { return nullInt32(r.M.ThrottleSeconds) }

func (r *NotificationPolicyResolver) EscalateAfterSeconds() *int32 {
	return nullInt32(r.M.EscalateAfterSeconds)
}

func (r *NotificationPolicyResolver) MaxEscalations() *int32 { return nullInt32(r.M.MaxEscalations) }

func (r *NotificationPolicyResolver) Enabled() bool { return r.M.Enabled }

func (r *NotificationPolicyResolver) Metadata() *string { return util.MetadataStr(r.M.Metadata) }

func (r *NotificationPolicyResolver) Rules() []*NotificationRuleResolver {
	resolvers := make([]*NotificationRuleResolver, 0, len(r.M.Rules))
	for _, rule := range r.M.Rules {
		resolvers = append(resolvers, &NotificationRuleResolver{M: rule, S: r.S, C: r.C})
	}
	return resolvers
}

// NotificationRuleResolver resolves a single routing rule.
type NotificationRuleResolver struct {
	M model.NotificationRule
	S *SchemaResolver
	C context.Context
}

func (r *NotificationRuleResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *NotificationRuleResolver) Severity() string { return r.M.Severity }

func (r *NotificationRuleResolver) Recipients() *string { return util.MetadataStr(r.M.Recipients) }

// Channel resolves the rule's target delivery channel (preloaded with the policy).
func (r *NotificationRuleResolver) Channel() *NotificationChannelResolver {
	if r.M.Channel == nil {
		return nil
	}
	return &NotificationChannelResolver{M: *r.M.Channel, S: r.S, C: r.C}
}

// NotificationPolicySearchResultsResolver resolves a page of policies.
type NotificationPolicySearchResultsResolver struct {
	M model.NotificationPolicySearchResults
	S *SchemaResolver
	C context.Context
}

func (r *NotificationPolicySearchResultsResolver) Results() []*NotificationPolicyResolver {
	resolvers := make([]*NotificationPolicyResolver, 0, len(r.M.Results))
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &NotificationPolicyResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *NotificationPolicySearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
