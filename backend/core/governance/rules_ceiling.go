// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

const RulesPerTenantField = "rulesPerTenant"

// DefaultRulesPerTenant matches the existing DETECT runtime rule budget.
const DefaultRulesPerTenant = 500

// RulesCeilingResolver reads the tier ceiling at publish time without a cache.
// Lookup failures reach the caller: a fallback could exceed a lower tier's limit.
type RulesCeilingResolver struct {
	fetch func(context.Context, string) (int, error)
}

func NewRulesCeilingResolver(client *svcclient.Client, url string) *RulesCeilingResolver {
	return &RulesCeilingResolver{fetch: func(ctx context.Context, tenant string) (int, error) {
		var out rulesCeilingResponse
		if err := client.Query(ctx, url, tenant, rulesCeilingQuery, nil, &out); err != nil {
			return 0, err
		}
		v := out.TenantGovernance.RulesPerTenant
		if v == nil {
			return DefaultRulesPerTenant, nil
		}
		if *v < 0 {
			return 0, fmt.Errorf("invalid rulesPerTenant ceiling: %d", *v)
		}
		return int(*v), nil
	}}
}
func (r *RulesCeilingResolver) Resolve(ctx context.Context, tenant string) (int, error) {
	if tenant == "" {
		return 0, fmt.Errorf("cannot resolve rulesPerTenant without a tenant")
	}
	return r.fetch(ctx, tenant)
}

const rulesCeilingQuery = "query { tenantGovernance { " + RulesPerTenantField + " } }"

type rulesCeilingResponse struct {
	TenantGovernance struct {
		RulesPerTenant *int32 `json:"rulesPerTenant"`
	} `json:"tenantGovernance"`
}
