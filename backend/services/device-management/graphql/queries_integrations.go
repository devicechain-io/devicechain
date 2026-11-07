// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// Find one integration by token, or null when there is none.
func (r *SchemaResolver) Integration(ctx context.Context, args struct {
	Token string
}) (*IntegrationResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).IntegrationsByToken(ctx, []string{args.Token})
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return &IntegrationResolver{M: *found[0], S: r, C: ctx}, nil
}

// Search for integrations that meet criteria.
func (r *SchemaResolver) Integrations(ctx context.Context, args struct {
	Criteria model.IntegrationSearchCriteria
}) (*IntegrationSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	results, err := r.GetApi(ctx).Integrations(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &IntegrationSearchResultsResolver{M: *results, S: r, C: ctx}, nil
}

// Prove a bearer token belongs to an integration (service plane). Null is the one answer to every failure to
// authenticate; a disabled integration whose token matched is returned with enabled false.
// Any other error (the database, an unconfigured key) is an error: it says nothing about the
// token, and a caller must not read it as a rejection.
func (r *SchemaResolver) IntegrationAuth(ctx context.Context, args struct {
	Token  string
	Secret string
}) (*IntegrationResolver, error) {
	// Service plane: only a service token holding device:read may ask. A tenant user's
	// access token must not be able to probe tokens against integrations.
	if err := auth.AuthorizeService(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).AuthenticateIntegration(ctx, args.Token, args.Secret)
	switch {
	case err == nil, errors.Is(err, model.ErrIntegrationDisabled):
		return &IntegrationResolver{M: *found, S: r, C: ctx}, nil
	case errors.Is(err, model.ErrIntegrationAuthFailed):
		return nil, nil
	default:
		return nil, err
	}
}

// Whether a device type has a payload decoder (service plane). Null for every failure to
// authenticate, as integrationAuth; an out-of-scope or unknown device type is present: false.
func (r *SchemaResolver) IntegrationDecoder(ctx context.Context, args struct {
	Integration     string
	Secret          string
	DeviceTypeToken string
}) (*IntegrationDecoderPlanResolver, error) {
	// Service plane, like integrationAuth: a tenant user's access token must not be able to
	// probe integrations with guessed secrets.
	if err := auth.AuthorizeService(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	// Through the cached decorator when the service injected one: it is the resolver's own read, so
	// the answer is what the resolver will run, and a warm question costs a memory hit and no
	// decoder compile.
	var api model.DeviceManagementApi = r.GetApi(ctx)
	if capi, ok := ctx.Value(ContextCachedApiKey).(*model.CachedApi); ok && capi != nil {
		api = capi
	}
	answer, err := model.IntegrationDecoder(ctx, api, args.Integration, args.Secret, args.DeviceTypeToken)
	if err != nil || answer == nil {
		return nil, err
	}
	return &IntegrationDecoderPlanResolver{M: answer}, nil
}

// Whether a device's type has a payload decoder (service plane), for a device that publishes its
// own payload. Never an error for an unknown device: it is present: false, like a type with none.
func (r *SchemaResolver) DeviceDecoder(ctx context.Context, args struct {
	DeviceToken string
}) (*DeviceDecoderPlanResolver, error) {
	if err := auth.AuthorizeService(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	var api model.DeviceManagementApi = r.GetApi(ctx)
	if capi, ok := ctx.Value(ContextCachedApiKey).(*model.CachedApi); ok && capi != nil {
		api = capi
	}
	answer, err := model.DeviceDecoder(ctx, api, args.DeviceToken)
	if err != nil {
		return nil, err
	}
	return &DeviceDecoderPlanResolver{IntegrationDecoderPlanResolver{M: answer}}, nil
}

// The sender-named devices an integration has seen that the tenant has no device in scope for.
func (r *SchemaResolver) UnregisteredIntegrationDevices(ctx context.Context, args struct {
	Criteria model.UnregisteredDeviceSearchCriteria
}) (*UnregisteredIntegrationDeviceSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	results, err := r.GetApi(ctx).UnregisteredIntegrationDevices(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &UnregisteredIntegrationDeviceSearchResultsResolver{M: *results, S: r, C: ctx}, nil
}
