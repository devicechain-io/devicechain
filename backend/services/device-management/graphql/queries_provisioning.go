// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find provisioning profiles by unique id.
func (r *SchemaResolver) ProvisioningProfilesById(ctx context.Context, args struct {
	Ids []string
}) ([]*ProvisioningProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := util.AsUintIds(args.Ids)
	if err != nil {
		return nil, err
	}

	found, err := api.ProvisioningProfilesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*ProvisioningProfileResolver, 0)
	for _, pp := range found {
		result = append(result, &ProvisioningProfileResolver{
			M: *pp,
			S: r,
			C: ctx,
		})
	}
	return result, nil
}

// provisioningProfileByID resolves the provisioning profile a device or a credential names,
// for a caller holding device:read WITHOUT a scope. A scoped caller is refused with an error
// rather than handed nil, so the field never answers "none" about a profile that exists.
func (r *SchemaResolver) provisioningProfileByID(ctx context.Context, id uint) (*ProvisioningProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).ProvisioningProfilesById(ctx, []uint{id})
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return &ProvisioningProfileResolver{M: *found[0], S: r, C: ctx}, nil
}

// Find provisioning profiles by unique token.
func (r *SchemaResolver) ProvisioningProfilesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*ProvisioningProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.ProvisioningProfilesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*ProvisioningProfileResolver, 0)
	for _, pp := range found {
		result = append(result, &ProvisioningProfileResolver{
			M: *pp,
			S: r,
			C: ctx,
		})
	}
	return result, nil
}

// Search for provisioning profiles that meet criteria.
func (r *SchemaResolver) ProvisioningProfiles(ctx context.Context, args struct {
	Criteria model.ProvisioningProfileSearchCriteria
}) (*ProvisioningProfileSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	results, err := api.ProvisioningProfiles(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	return &ProvisioningProfileSearchResultsResolver{
		M: *results,
		S: r,
		C: ctx,
	}, nil
}
