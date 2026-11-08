// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
)

//go:embed admin_schema.graphql
var AdminSchemaContent string

// UsageReader is what the admin plane needs from the API: one tenant's days, zero-filled.
type UsageReader interface {
	TenantUsage(ctx context.Context, tenantId string, from, to time.Time) ([]model.TenantUsageDay, error)
}

// AdminResolver is the root resolver of the event-management ADMIN plane: the instance-scoped,
// read-only per-tenant usage record. It is served on its own /admin/graphql endpoint, which
// validates IDENTITY-tier tokens and runs in the system context across tenants.
//
// The endpoint is the boundary that matters; the tenant:read check below is defence in depth.
// tenant:read is a system-tier authority, so a tenant access token cannot satisfy it even
// carrying "*".
type AdminResolver struct{}

func adminUsageFrom(ctx context.Context) UsageReader {
	return ctx.Value(gqlcore.ContextApiKey).(UsageReader)
}

const usageDayLayout = "2006-01-02"

// parseUsageDay reads a YYYY-MM-DD day, naming the argument when it is not one.
func parseUsageDay(arg, v string) (time.Time, error) {
	t, err := time.ParseInLocation(usageDayLayout, v, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be a day written YYYY-MM-DD", arg)
	}
	return t, nil
}

// TenantUsage returns one tenant's usage for each UTC day in [from, to].
func (r *AdminResolver) TenantUsage(ctx context.Context, args struct {
	Tenant string
	From   string
	To     string
}) ([]*TenantUsageDayResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantRead); err != nil {
		return nil, err
	}
	from, err := parseUsageDay("from", args.From)
	if err != nil {
		return nil, err
	}
	to, err := parseUsageDay("to", args.To)
	if err != nil {
		return nil, err
	}
	days, err := adminUsageFrom(ctx).TenantUsage(ctx, args.Tenant, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]*TenantUsageDayResolver, 0, len(days))
	for _, d := range days {
		out = append(out, &TenantUsageDayResolver{d: d})
	}
	return out, nil
}

// TenantUsageDayResolver resolves the TenantUsageDay type.
type TenantUsageDayResolver struct{ d model.TenantUsageDay }

func (t *TenantUsageDayResolver) Tenant() string          { return t.d.TenantId }
func (t *TenantUsageDayResolver) Day() string             { return t.d.Day.Format(usageDayLayout) }
func (t *TenantUsageDayResolver) EventsIngested() float64 { return float64(t.d.EventsIngested) }
func (t *TenantUsageDayResolver) DevicesActive() int32    { return int32(t.d.DevicesActive) }
func (t *TenantUsageDayResolver) DevicesPeak() int32      { return int32(t.d.DevicesPeak) }
func (t *TenantUsageDayResolver) DevicesAvg() float64     { return t.d.DevicesAvg() }
func (t *TenantUsageDayResolver) Complete() bool          { return t.d.Complete }
