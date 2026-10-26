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

// Prove a bearer token belongs to an integration. Null is the one answer to every failure to
// authenticate; a disabled integration whose token matched is returned with enabled false.
// Any other error (the database, an unconfigured key) is an error: it says nothing about the
// token, and a caller must not read it as a rejection.
func (r *SchemaResolver) IntegrationAuth(ctx context.Context, args struct {
	Token  string
	Secret string
}) (*IntegrationResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
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
