// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"

	"github.com/devicechain-io/dc-device-management/model"
)

// Reading a credential is gated on device:WRITE, not device:read, and the three
// queries below are the only door to a DeviceCredential (nothing else in the
// schema exposes the type, so this gate is the whole boundary).
//
// The reason is that for two of the three credential types the credentialId IS
// the bearer secret: an ACCESS_TOKEN device authenticates by presenting its
// credentialId and nothing else (model.credentialRequiresSecret — only
// MQTT_BASIC compares a stored secret). Withholding credentialValue therefore
// defends MQTT_BASIC and does not defend ACCESS_TOKEN, so whoever may read a
// credential can impersonate its device: open a broker session as it and
// publish telemetry in its name.
//
// device:read is not a privileged authority — it is in the read-only baseline
// every enabled tenant member receives (user-management identity.viewerAuthorities),
// so a viewer holds it. Leaving these queries there made a read-only role yield
// device-impersonation capability.
//
// device:write is the right gate because it grants nothing new: createDeviceCredential
// is already device:write (mutations_credentials.go), so that holder can mint an
// ACCESS_TOKEN for any device in the tenant and impersonate it regardless. The
// capability is already theirs by construction; this gate only stops it leaking
// down to the viewer baseline.

// Find device credentials by unique id.
func (r *SchemaResolver) DeviceCredentialsById(ctx context.Context, args struct {
	Ids []string
}) ([]*DeviceCredentialResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := util.AsUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.DeviceCredentialsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceCredentialResolver, 0)
	for _, dc := range found {
		dcr := &DeviceCredentialResolver{
			M: *dc,
			S: r,
			C: ctx,
		}
		result = append(result, dcr)
	}
	return result, nil
}

// Find device credentials by unique token.
func (r *SchemaResolver) DeviceCredentialsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*DeviceCredentialResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DeviceCredentialsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceCredentialResolver, 0)
	for _, dc := range found {
		dcr := &DeviceCredentialResolver{
			M: *dc,
			S: r,
			C: ctx,
		}
		result = append(result, dcr)
	}
	return result, nil
}

// List all device credentials that match the given criteria.
func (r *SchemaResolver) DeviceCredentials(ctx context.Context, args struct {
	Criteria model.DeviceCredentialSearchCriteria
}) (*DeviceCredentialSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DeviceCredentials(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &DeviceCredentialSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}
