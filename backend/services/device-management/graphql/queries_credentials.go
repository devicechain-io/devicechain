// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find device credentials by unique id.
func (r *SchemaResolver) DeviceCredentialsById(ctx context.Context, args struct {
	Ids []string
}) ([]*DeviceCredentialResolver, error) {
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
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
