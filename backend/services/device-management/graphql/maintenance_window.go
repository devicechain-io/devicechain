// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// ------------------------------
// Maintenance window resolver
// ------------------------------

type MaintenanceWindowResolver struct {
	M model.MaintenanceWindow
	S *SchemaResolver
	C context.Context
}

func (r *MaintenanceWindowResolver) Id() gql.ID { return gql.ID(fmt.Sprint(r.M.ID)) }

func (r *MaintenanceWindowResolver) CreatedAt() *string { return util.FormatTime(r.M.CreatedAt) }

func (r *MaintenanceWindowResolver) UpdatedAt() *string { return util.FormatTime(r.M.UpdatedAt) }

func (r *MaintenanceWindowResolver) DeletedAt() *string { return util.FormatTime(r.M.DeletedAt.Time) }

func (r *MaintenanceWindowResolver) Token() string { return r.M.Token }

func (r *MaintenanceWindowResolver) Name() *string { return util.NullStr(r.M.Name) }

func (r *MaintenanceWindowResolver) Description() *string { return util.NullStr(r.M.Description) }

func (r *MaintenanceWindowResolver) Timezone() string { return r.M.Timezone }

func (r *MaintenanceWindowResolver) Slots() string { return string(r.M.Slots) }

func (r *MaintenanceWindowResolver) Metadata() *string { return util.MetadataStr(r.M.Metadata) }

type MaintenanceWindowSearchResultsResolver struct {
	M model.MaintenanceWindowSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *MaintenanceWindowSearchResultsResolver) Results() []*MaintenanceWindowResolver {
	resolvers := make([]*MaintenanceWindowResolver, 0, len(r.M.Results))
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &MaintenanceWindowResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *MaintenanceWindowSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

// ---------
// Queries
// ---------

// Find maintenance windows by unique id.
func (r *SchemaResolver) MaintenanceWindowsById(ctx context.Context, args struct {
	Ids []string
}) ([]*MaintenanceWindowResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	ids, err := util.AsUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).MaintenanceWindowsById(ctx, ids)
	if err != nil {
		return nil, err
	}
	return r.windowResolvers(ctx, found), nil
}

// Find maintenance windows by unique token.
func (r *SchemaResolver) MaintenanceWindowsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*MaintenanceWindowResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).MaintenanceWindowsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}
	return r.windowResolvers(ctx, found), nil
}

func (r *SchemaResolver) windowResolvers(ctx context.Context, found []*model.MaintenanceWindow) []*MaintenanceWindowResolver {
	result := make([]*MaintenanceWindowResolver, 0, len(found))
	for _, w := range found {
		result = append(result, &MaintenanceWindowResolver{M: *w, S: r, C: ctx})
	}
	return result
}

// List maintenance windows that match the given criteria.
func (r *SchemaResolver) MaintenanceWindows(ctx context.Context, args struct {
	Criteria model.MaintenanceWindowSearchCriteria
}) (*MaintenanceWindowSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).MaintenanceWindows(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &MaintenanceWindowSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

// MaintenanceWindowOpeningResolver exposes when an operation under a window may start.
type MaintenanceWindowOpeningResolver struct {
	M model.WindowOpening
}

func (r *MaintenanceWindowOpeningResolver) Found() bool { return r.M.Found }

func (r *MaintenanceWindowOpeningResolver) OpenNow() bool { return r.M.OpenNow }

func (r *MaintenanceWindowOpeningResolver) DispatchAfter() string {
	return r.M.DispatchAfter.UTC().Format(time.RFC3339)
}

func (r *MaintenanceWindowOpeningResolver) Closes() string {
	return r.M.Closes.UTC().Format(time.RFC3339)
}

// MaintenanceWindowOpening is the one entry point other areas call to evaluate a window.
// Gated on device:read, like the reads around it.
func (r *SchemaResolver) MaintenanceWindowOpening(ctx context.Context, args struct {
	Token string
	At    *string
}) (*MaintenanceWindowOpeningResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	at := time.Now()
	if args.At != nil {
		parsed, err := time.Parse(time.RFC3339, *args.At)
		if err != nil {
			return nil, fmt.Errorf("at %q is not an RFC 3339 timestamp", *args.At)
		}
		at = parsed
	}
	opening, err := r.GetApi(ctx).MaintenanceWindowOpening(ctx, args.Token, at)
	if err != nil {
		return nil, err
	}
	return &MaintenanceWindowOpeningResolver{M: *opening}, nil
}

// CommandDispatchHoldResolver exposes one command that must wait for its window.
type CommandDispatchHoldResolver struct {
	M model.CommandDispatchHold
}

func (r *CommandDispatchHoldResolver) DeviceToken() string { return r.M.DeviceToken }

func (r *CommandDispatchHoldResolver) WindowToken() string { return r.M.WindowToken }

func (r *CommandDispatchHoldResolver) DispatchAfter() string {
	return r.M.DispatchAfter.UTC().Format(time.RFC3339)
}

func (r *CommandDispatchHoldResolver) DispatchUntil() string {
	return r.M.DispatchUntil.UTC().Format(time.RFC3339)
}

// CommandDispatchScheduleResolver exposes the window half of an enqueue decision.
type CommandDispatchScheduleResolver struct {
	holds     []*model.CommandDispatchHold
	refusals  []*model.BatchEnqueueRefusal
	ungated   []string
	undefined []string
	impacts   []*model.CommandDispatchImpact
}

// Impacts lists, for each device whose type defines the command, the impact it carries.
func (r *CommandDispatchScheduleResolver) Impacts() []*CommandDispatchImpactResolver {
	out := make([]*CommandDispatchImpactResolver, 0, len(r.impacts))
	for _, i := range r.impacts {
		out = append(out, &CommandDispatchImpactResolver{M: *i})
	}
	return out
}

// CommandDispatchImpactResolver exposes one device's command impact.
type CommandDispatchImpactResolver struct{ M model.CommandDispatchImpact }

func (r *CommandDispatchImpactResolver) DeviceToken() string { return r.M.DeviceToken }
func (r *CommandDispatchImpactResolver) Impact() string      { return r.M.Impact }

// Undefined lists the devices whose type's current profile version does not define the command.
func (r *CommandDispatchScheduleResolver) Undefined() []string {
	if r.undefined == nil {
		return []string{}
	}
	return r.undefined
}

// Ungated lists the devices that exist, are enabled, and are not gated for the command.
func (r *CommandDispatchScheduleResolver) Ungated() []string {
	if r.ungated == nil {
		return []string{}
	}
	return r.ungated
}

func (r *CommandDispatchScheduleResolver) Holds() []*CommandDispatchHoldResolver {
	out := make([]*CommandDispatchHoldResolver, 0, len(r.holds))
	for _, h := range r.holds {
		out = append(out, &CommandDispatchHoldResolver{M: *h})
	}
	return out
}

func (r *CommandDispatchScheduleResolver) Refusals() []*BatchEnqueueRefusalResolver {
	out := make([]*BatchEnqueueRefusalResolver, 0, len(r.refusals))
	for _, f := range r.refusals {
		out = append(out, &BatchEnqueueRefusalResolver{M: *f})
	}
	return out
}

// CommandDispatchSchedule decides which devices must wait for a maintenance window and which
// are refused because the window opens after the command expires. See the SDL for the
// contract; the work is model.Api.DispatchScheduleFor.
func (r *SchemaResolver) CommandDispatchSchedule(ctx context.Context, args struct {
	DeviceTokens []string
	CommandKey   string
	ExpiresAt    *string
	AlreadyHeld  *bool
}) (*CommandDispatchScheduleResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	if len(args.DeviceTokens) > maxBatchValidationTokens {
		return nil, fmt.Errorf("cannot schedule %d devices in one request; the limit is %d",
			len(args.DeviceTokens), maxBatchValidationTokens)
	}
	var expiresAt *time.Time
	if args.ExpiresAt != nil {
		parsed, err := time.Parse(time.RFC3339, *args.ExpiresAt)
		if err != nil {
			return nil, fmt.Errorf("expiresAt %q is not an RFC 3339 timestamp", *args.ExpiresAt)
		}
		expiresAt = &parsed
	}
	alreadyHeld := args.AlreadyHeld != nil && *args.AlreadyHeld
	schedule, err := r.GetApi(ctx).DispatchScheduleFor(ctx, args.DeviceTokens, args.CommandKey,
		expiresAt, time.Now(), alreadyHeld)
	if err != nil {
		return nil, err
	}
	return &CommandDispatchScheduleResolver{holds: schedule.Holds, refusals: schedule.Refusals,
		ungated: schedule.Ungated, undefined: schedule.Undefined, impacts: schedule.Impacts}, nil
}

// ------------
// Mutations
// ------------

// Create a new maintenance window.
func (r *SchemaResolver) CreateMaintenanceWindow(ctx context.Context, args struct {
	Request model.MaintenanceWindowCreateRequest
}) (*MaintenanceWindowResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	created, err := r.GetApi(ctx).CreateMaintenanceWindow(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &MaintenanceWindowResolver{M: *created, S: r, C: ctx}, nil
}

// Update an existing maintenance window. The request is a PARTIAL update (omitted leaves a
// field alone). It is declared non-null in the schema and is therefore a VALUE here.
func (r *SchemaResolver) UpdateMaintenanceWindow(ctx context.Context, args struct {
	Token   string
	Request model.MaintenanceWindowUpdateRequest
}) (*MaintenanceWindowResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	updated, err := r.GetApi(ctx).UpdateMaintenanceWindow(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &MaintenanceWindowResolver{M: *updated, S: r, C: ctx}, nil
}

// Delete a maintenance window (refused while a device type uses it).
func (r *SchemaResolver) DeleteMaintenanceWindow(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteMaintenanceWindow(ctx, args.Token)
}

// MaintenanceWindow resolves a device type's assigned window, or null.
func (r *DeviceTypeResolver) MaintenanceWindow() (*MaintenanceWindowResolver, error) {
	if r.M.MaintenanceWindow != nil {
		return &MaintenanceWindowResolver{M: *r.M.MaintenanceWindow, S: r.S, C: r.C}, nil
	}
	if r.M.MaintenanceWindowId == nil {
		return nil, nil
	}
	found, err := r.S.GetApi(r.C).MaintenanceWindowsById(r.C, []uint{*r.M.MaintenanceWindowId})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return &MaintenanceWindowResolver{M: *found[0], S: r.S, C: r.C}, nil
}
