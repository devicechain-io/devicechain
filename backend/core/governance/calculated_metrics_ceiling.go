// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// The calculated-metrics ceiling (ADR-082 decision 8, packaged per ADR-065): how many
// calculated metrics one device profile may publish.
//
// A calculated metric is computed on every reading of every device of the profile and
// stored as one more measurement row, rollup group and latest-state row, so its cost is
// storage rows on the busiest tables in the platform. That is why the count is sold per
// tier rather than left to the author.
const (
	// CalculatedMetricsPerProfileField is the field device-management selects off
	// tenantGovernance AND the tier config key an operator sets, declared once here for
	// the reason GeoFenceCeilingField gives: the two must be the same string by
	// construction.
	CalculatedMetricsPerProfileField = "calculatedMetricsPerProfile"

	// DefaultCalculatedMetricsPerProfile is what a tenant whose tier declares nothing may
	// publish: ZERO, so the feature is off on every default tier. It stays off until the
	// resolver-cost measurement on a production-shaped cluster has passed, and an operator
	// turns it on for a tier by setting the key. Zero is a real setting here, not a
	// missing one: it is the platform's answer until that measurement says otherwise.
	DefaultCalculatedMetricsPerProfile = 0

	// MaxCalculatedMetricsPerProfile is the most any tier may grant. Each one adds a
	// measurement row per reading, so a profile at the maximum writes up to 32 extra rows
	// for every reading its devices send.
	MaxCalculatedMetricsPerProfile = 32

	// CalculatedMetricsPerProfileBecause is the reason a tier value above the maximum is
	// refused, for the operator who was refused.
	CalculatedMetricsPerProfileBecause = "each calculated metric stores one more measurement row " +
		"for every reading of every device of the profile"
)

// CalculatedMetricsCeilingResolver reads a tenant's calculated-metrics ceiling from
// user-management's tenantGovernance.
//
// 🔴 IT DOES NOT CACHE AND IT RETURNS AN ERROR. It is read when a profile that carries a
// calculated metric is published, which is rare, and what a wrong answer produces there is
// durable: a published version that every reading is then computed against. A tier may sit
// at zero, so answering an unreachable user-management with any number but zero would raise
// the ceiling of exactly the tenant nobody could read. The caller refuses the publish.
type CalculatedMetricsCeilingResolver struct {
	fetch func(ctx context.Context, tenant string) (int, error)
}

// NewCalculatedMetricsCeilingResolver wires the resolver to user-management at umURL over
// the given service-token client, which needs tenant:read.
func NewCalculatedMetricsCeilingResolver(client *svcclient.Client, umURL string) *CalculatedMetricsCeilingResolver {
	return &CalculatedMetricsCeilingResolver{fetch: func(ctx context.Context, tenant string) (int, error) {
		var out calculatedMetricsCeilingResponse
		if err := client.Query(ctx, umURL, tenant, calculatedMetricsCeilingQuery, nil, &out); err != nil {
			return 0, err
		}
		return resolveCalculatedMetricsCeiling(out.TenantGovernance.CalculatedMetricsPerProfile), nil
	}}
}

// Resolve answers how many calculated metrics one of the tenant's profiles may publish.
func (r *CalculatedMetricsCeilingResolver) Resolve(ctx context.Context, tenant string) (int, error) {
	if tenant == "" {
		return 0, fmt.Errorf("governance: cannot resolve the calculated-metrics ceiling without a tenant")
	}
	return r.fetch(ctx, tenant)
}

// calculatedMetricsCeilingQuery selects the one field, built from the constant so a
// rename cannot leave the query selecting the old name.
const calculatedMetricsCeilingQuery = "query { tenantGovernance { " + CalculatedMetricsPerProfileField + " } }"

// calculatedMetricsCeilingResponse is the wire shape of that query. The json tag must be a
// literal; TestCalculatedMetricsCeilingQueryAndResponseAgree decodes a body built from the
// constant, so a tag that drifts from it fails instead of silently decoding to nil.
type calculatedMetricsCeilingResponse struct {
	TenantGovernance struct {
		CalculatedMetricsPerProfile *int32 `json:"calculatedMetricsPerProfile"`
	} `json:"tenantGovernance"`
}

// resolveCalculatedMetricsCeiling folds the wire value onto the platform default. Null
// (neither the tenant nor its tier declares one) and a negative value resolve to the
// default; a value above the maximum is clamped to it, for the version-skew reason
// resolveGeoFenceCap gives.
func resolveCalculatedMetricsCeiling(v *int32) int {
	if v == nil || *v < 0 {
		return DefaultCalculatedMetricsPerProfile
	}
	if int(*v) > MaxCalculatedMetricsPerProfile {
		return MaxCalculatedMetricsPerProfile
	}
	return int(*v)
}
