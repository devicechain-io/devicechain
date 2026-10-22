// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new device type.
func (r *SchemaResolver) CreateDeviceType(ctx context.Context, args struct {
	Request *model.DeviceTypeCreateRequest
}) (*DeviceTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateDeviceType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing device type. The request is a PARTIAL update: a field the
// caller omitted is left alone, an explicit null clears it, a value sets it.
//
// Request is declared non-null in the schema and therefore a VALUE here, not a
// pointer — graphql-go refuses a pointer field for a required argument. That is
// the right contract anyway: an update naming no fields at all is a caller error,
// not a no-op to be absorbed silently.
func (r *SchemaResolver) UpdateDeviceType(ctx context.Context, args struct {
	Token   string
	Request model.DeviceTypeUpdateRequest
}) (*DeviceTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	// Route through the caching decorator: attaching/detaching/changing the
	// profile (ADR-045) changes what the ingest path resolves for this type, so
	// the update must evict the type's cached metric definitions.
	api := r.GetCachedApi(ctx)
	updated, err := api.UpdateDeviceType(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new device.
func (r *SchemaResolver) CreateDevice(ctx context.Context, args struct {
	Request *model.DeviceCreateRequest
}) (*DeviceResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateDevice(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create many devices in one transaction from a template (bulk fleet
// provisioning). The server renders the batch from the request's templates and
// creates them all-or-nothing — an invalid template, a rendered token that
// collides within the batch or with an existing device, or an unknown device
// type fails them all.
func (r *SchemaResolver) CreateDevices(ctx context.Context, args struct {
	Request model.DeviceBulkCreateRequest
}) ([]*DeviceResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateDevicesFromTemplate(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	resolvers := make([]*DeviceResolver, 0, len(created))
	for _, c := range created {
		resolvers = append(resolvers, &DeviceResolver{M: *c, S: r, C: ctx})
	}
	return resolvers, nil
}

// Update an existing device.
// The request is a PARTIAL update: a field the caller omitted is left alone, an
// explicit null clears it, a value sets it. It is declared non-null in the schema and
// is therefore a VALUE here, not a pointer — graphql-go refuses a pointer field for a
// required argument.
//
// 🔴 Non-null refuses a MISSING request (`request: null`), not an EMPTY one. `{}` is a
// perfectly good non-null input object, and it is accepted as a no-op — which is the
// correct reading of "change nothing", and what the harness's
// EmptyRequestChangesNothing asserts. An earlier version of this comment claimed an
// update naming no fields was a caller error; nothing enforced that, and nothing
// should.
func (r *SchemaResolver) UpdateDevice(ctx context.Context, args struct {
	Token   string
	Request model.DeviceUpdateRequest
}) (*DeviceResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateDevice(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}

	dt := &DeviceResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Put a device behind a gateway device. The model owns every rule; this only authorizes.
//
// The authority is the one that manages device credentials (device:write): assigning a gateway
// gives it the same standing over the device as issuing the device a credential. Authorize
// refuses a member limited to part of the tenant outright, so both devices are necessarily in
// the caller's scope: a scoped caller cannot reach this at all.
func (r *SchemaResolver) AssignDeviceGateway(ctx context.Context, args struct {
	DeviceToken  string
	GatewayToken string
}) (*DeviceResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	updated, err := r.GetApi(ctx).AssignDeviceGateway(ctx, args.DeviceToken, args.GatewayToken)
	if err != nil {
		return nil, err
	}
	return &DeviceResolver{M: *updated, S: r, C: ctx}, nil
}

// Take a device out from behind its gateway.
func (r *SchemaResolver) ClearDeviceGateway(ctx context.Context, args struct {
	DeviceToken string
}) (*DeviceResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	updated, err := r.GetApi(ctx).ClearDeviceGateway(ctx, args.DeviceToken)
	if err != nil {
		return nil, err
	}
	return &DeviceResolver{M: *updated, S: r, C: ctx}, nil
}

// Import devices from rows, one transaction per row, reporting the outcome of each. The
// model owns every rule; this only authorizes. Rows (and the credential secrets they may
// carry) are passed through and never logged here.
func (r *SchemaResolver) ImportDevices(ctx context.Context, args struct {
	Rows   []*model.DeviceImportRow
	Mode   string
	DryRun bool
}) (*DeviceImportResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	result, err := api.ImportDevices(ctx, args.Rows, args.Mode, args.DryRun)
	if err != nil {
		return nil, err
	}
	return &DeviceImportResultResolver{M: result}, nil
}
