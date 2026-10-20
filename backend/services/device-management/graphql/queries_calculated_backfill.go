// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strconv"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// The calculated-metric backfill doors (model/api_calculated_backfill.go): what event-management's
// backfill worker reads over a service token to learn which formulas a published version runs and
// which devices to run them for. Read-only, tenant-scoped by the rows, and gated on Authorize, which
// refuses every customer-scoped caller: the plan and the roster are tenant-wide material.

// CalculatedMetricPlan resolves one published version's calculated metrics.
func (r *SchemaResolver) CalculatedMetricPlan(ctx context.Context, args struct {
	ProfileToken string
	Version      *int32
}) (*CalculatedMetricPlanResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	plan, err := r.GetApi(ctx).CalculatedMetricPlan(ctx, args.ProfileToken, args.Version)
	if err != nil {
		return nil, err
	}
	return &CalculatedMetricPlanResolver{M: *plan}, nil
}

// CalculatedBackfillRoster resolves one page of the backfill roster.
func (r *SchemaResolver) CalculatedBackfillRoster(ctx context.Context, args struct {
	ProfileToken string
	ConstantKeys []string
	DeviceTokens *[]string
	AfterId      *string
	Limit        int32
}) (*CalculatedBackfillRosterPageResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	afterId, err := optionalCursor(args.AfterId)
	if err != nil {
		return nil, err
	}
	var tokens []string
	if args.DeviceTokens != nil {
		tokens = *args.DeviceTokens
	}
	page, err := r.GetApi(ctx).CalculatedBackfillRoster(ctx, args.ProfileToken, args.ConstantKeys, tokens, afterId,
		int(args.Limit))
	if err != nil {
		return nil, err
	}
	return &CalculatedBackfillRosterPageResolver{M: *page}, nil
}

// CalculatedMetricPlanResolver exposes one version's plan.
type CalculatedMetricPlanResolver struct {
	M model.CalculatedMetricPlan
}

func (r *CalculatedMetricPlanResolver) VersionToken() string { return r.M.VersionToken }
func (r *CalculatedMetricPlanResolver) Version() int32       { return r.M.Version }
func (r *CalculatedMetricPlanResolver) ActiveSince() *string {
	if r.M.ActiveSince == nil {
		return nil
	}
	s := reconcileTime(*r.M.ActiveSince)
	return &s
}
func (r *CalculatedMetricPlanResolver) ConstantKeys() []string { return r.M.ConstantKeys }

func (r *CalculatedMetricPlanResolver) Measured() []*CalculatedMetricPlanInputResolver {
	out := make([]*CalculatedMetricPlanInputResolver, 0, len(r.M.Measured))
	for i := range r.M.Measured {
		out = append(out, &CalculatedMetricPlanInputResolver{M: r.M.Measured[i]})
	}
	return out
}

func (r *CalculatedMetricPlanResolver) Calculated() []*CalculatedMetricPlanMetricResolver {
	out := make([]*CalculatedMetricPlanMetricResolver, 0, len(r.M.Calculated))
	for i := range r.M.Calculated {
		out = append(out, &CalculatedMetricPlanMetricResolver{M: r.M.Calculated[i]})
	}
	return out
}

// CalculatedMetricPlanInputResolver exposes one measured input.
type CalculatedMetricPlanInputResolver struct {
	M model.CalculatedPlanInput
}

func (r *CalculatedMetricPlanInputResolver) Key() string      { return r.M.Key }
func (r *CalculatedMetricPlanInputResolver) DataType() string { return r.M.DataType }

// CalculatedMetricPlanMetricResolver exposes one calculated metric.
type CalculatedMetricPlanMetricResolver struct {
	M model.CalculatedPlanMetric
}

func (r *CalculatedMetricPlanMetricResolver) Key() string         { return r.M.Key }
func (r *CalculatedMetricPlanMetricResolver) Expression() string  { return r.M.Expression }
func (r *CalculatedMetricPlanMetricResolver) DataType() string    { return r.M.DataType }
func (r *CalculatedMetricPlanMetricResolver) MinValue() *float64  { return r.M.MinValue }
func (r *CalculatedMetricPlanMetricResolver) MaxValue() *float64  { return r.M.MaxValue }
func (r *CalculatedMetricPlanMetricResolver) Unit() *string       { return r.M.Unit }
func (r *CalculatedMetricPlanMetricResolver) Classifier() float64 { return float64(r.M.Classifier) }
func (r *CalculatedMetricPlanMetricResolver) NewInVersion() bool  { return r.M.NewInVersion }
func (r *CalculatedMetricPlanMetricResolver) ChangedSinceEarlier() bool {
	return r.M.ChangedSinceEarlier
}

func (r *CalculatedMetricPlanMetricResolver) NameSharedWithOtherProfiles() bool {
	return r.M.NameSharedWithOtherProfiles
}

// Inputs, MeasuredInputs and Constants are null for a metric that cannot run in the version.
func (r *CalculatedMetricPlanMetricResolver) Inputs() *[]string { return nilOrList(r.M.Inputs) }
func (r *CalculatedMetricPlanMetricResolver) MeasuredInputs() *[]string {
	return nilOrList(r.M.MeasuredInputs)
}
func (r *CalculatedMetricPlanMetricResolver) Constants() *[]string { return nilOrList(r.M.Constants) }

func nilOrList(v []string) *[]string {
	if v == nil {
		return nil
	}
	return &v
}

// CalculatedBackfillRosterPageResolver exposes one page of the roster.
type CalculatedBackfillRosterPageResolver struct {
	M model.BackfillRosterPage
}

func (r *CalculatedBackfillRosterPageResolver) NextCursor() *string {
	return formatCursor(r.M.NextCursor)
}
func (r *CalculatedBackfillRosterPageResolver) Total() *float64 {
	if r.M.Total == nil {
		return nil
	}
	t := float64(*r.M.Total)
	return &t
}

func (r *CalculatedBackfillRosterPageResolver) Entries() []*CalculatedBackfillRosterEntryResolver {
	out := make([]*CalculatedBackfillRosterEntryResolver, 0, len(r.M.Entries))
	for i := range r.M.Entries {
		out = append(out, &CalculatedBackfillRosterEntryResolver{M: r.M.Entries[i]})
	}
	return out
}

// CalculatedBackfillRosterEntryResolver exposes one device of the roster.
type CalculatedBackfillRosterEntryResolver struct {
	M model.BackfillRosterEntry
}

func (r *CalculatedBackfillRosterEntryResolver) DeviceId() string {
	return strconv.FormatUint(uint64(r.M.DeviceId), 10)
}
func (r *CalculatedBackfillRosterEntryResolver) DeviceToken() string { return r.M.DeviceToken }
func (r *CalculatedBackfillRosterEntryResolver) CreatedAt() string {
	return reconcileTime(r.M.CreatedAt)
}
func (r *CalculatedBackfillRosterEntryResolver) Constants() []*DeviceConstantResolver {
	out := make([]*DeviceConstantResolver, 0, len(r.M.Constants))
	for i := range r.M.Constants {
		out = append(out, &DeviceConstantResolver{M: r.M.Constants[i]})
	}
	return out
}
