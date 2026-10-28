// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// ReplaceDevice binds a NEW physical unit to an EXISTING logical device identity
// (ADR-074): it retires every credential the outgoing unit could authenticate with,
// mints one for the incoming unit, and writes an append-only record of the swap.
//
// Gated on device:write, and that is the SAME authority the three credential
// queries and createDeviceCredential require — deliberately, because the result
// carries the new credential's id, which for an ACCESS_TOKEN is the device's
// bearer. A device:write holder can already mint a bearer for any device in the
// tenant, so this grants nothing new; a weaker gate here would be an impersonation
// escalation dressed as a lifecycle operation.
//
// The actor and the timestamp are taken HERE, from the authenticated subject and
// the server clock, and are not fields of the request. A replacement record whose
// "who" and "when" the caller supplies records only what the caller wished to
// claim.
func (r *SchemaResolver) ReplaceDevice(ctx context.Context, args struct {
	Request model.DeviceReplaceRequest
}) (*DeviceReplaceResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	result, err := api.ReplaceDevice(ctx, &args.Request, publisher(ctx), time.Now())
	if err != nil {
		return nil, err
	}
	return &DeviceReplaceResultResolver{M: *result, S: r, C: ctx}, nil
}
