// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

const RulesPerTenantField = "rulesPerTenant"

// RulesCeilingResolver reads the tier ceiling at publish time without a cache.
// A nil ceiling means the tier has not opted into enforcement.
// Lookup failures reach the caller: a fallback could exceed a lower tier's limit.
type RulesCeilingResolver struct {
	fetch func(context.Context, string) (*int, error)
}

func NewRulesCeilingResolver(client *svcclient.Client, url string) *RulesCeilingResolver {
	return &RulesCeilingResolver{fetch: func(ctx context.Context, tenant string) (*int, error) {
		var out rulesCeilingResponse
		if err := client.Query(ctx, url, tenant, rulesCeilingQuery, nil, &out); err != nil {
			// Older user-management schemas cannot expose an explicit ceiling.
			var gqlErr *svcclient.GraphQLError
			if errors.As(err, &gqlErr) && unsupportedRulesCeiling(gqlErr.Messages) {
				return nil, nil
			}
			return nil, err
		}
		v := out.TenantGovernance.RulesPerTenant
		if v == nil {
			return nil, nil
		}
		if *v < 0 {
			return nil, fmt.Errorf("invalid rulesPerTenant ceiling: %d", *v)
		}
		n := int(*v)
		return &n, nil
	}}
}
func (r *RulesCeilingResolver) Resolve(ctx context.Context, tenant string) (*int, error) {
	if tenant == "" {
		return nil, fmt.Errorf("cannot resolve rulesPerTenant without a tenant")
	}
	return r.fetch(ctx, tenant)
}

const rulesCeilingQuery = "query { tenantGovernance { " + RulesPerTenantField + " } }"

type rulesCeilingResponse struct {
	TenantGovernance struct {
		RulesPerTenant *int32 `json:"rulesPerTenant"`
	} `json:"tenantGovernance"`
}

// Only schema validation errors for the fields this query adds are compatible.
// Runtime, permission and mixed errors must still fail closed for an increase.
func unsupportedRulesCeiling(messages []string) bool {
	if len(messages) == 0 {
		return false
	}
	for _, message := range messages {
		m := strings.ToLower(message)
		if !(strings.Contains(m, "cannot query field") || strings.Contains(m, "unknown field")) ||
			!(strings.Contains(m, "rulespertenant") || strings.Contains(m, "tenantgovernance")) {
			return false
		}
	}
	return true
}
