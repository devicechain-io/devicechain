// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// --------------------------
// Provisioning profile resolver
// --------------------------

type ProvisioningProfileResolver struct {
	M model.ProvisioningProfile
	S *SchemaResolver
	C context.Context
}

func (r *ProvisioningProfileResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *ProvisioningProfileResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *ProvisioningProfileResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *ProvisioningProfileResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *ProvisioningProfileResolver) Token() string {
	return r.M.Token
}

func (r *ProvisioningProfileResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *ProvisioningProfileResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *ProvisioningProfileResolver) ProvisionKey() string {
	return r.M.ProvisionKey
}

// ProvisionSecret is deliberately not exposed: the schema's ProvisioningProfile
// type omits it so the shared secret is write-only.

func (r *ProvisioningProfileResolver) Strategy() string {
	return r.M.Strategy
}

func (r *ProvisioningProfileResolver) CredentialType() string {
	return r.M.CredentialType
}

func (r *ProvisioningProfileResolver) Enabled() bool {
	return r.M.Enabled
}

func (r *ProvisioningProfileResolver) ExpiresAt() *string {
	if r.M.ExpiresAt.Valid {
		return util.FormatTime(r.M.ExpiresAt.Time)
	}
	return nil
}

func (r *ProvisioningProfileResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *ProvisioningProfileResolver) DeviceType() *DeviceTypeResolver {
	if r.M.DeviceType != nil {
		return &DeviceTypeResolver{
			M: *r.M.DeviceType,
			S: r.S,
			C: r.C,
		}
	}
	ids := []string{fmt.Sprintf("%d", r.M.DeviceTypeId)}
	rez, err := r.S.DeviceTypesById(r.C, struct{ Ids []string }{Ids: ids})
	if err != nil || len(rez) == 0 {
		return nil
	}
	return rez[0]
}

// -------------------------------------------
// Provisioning profile search results resolver
// -------------------------------------------

type ProvisioningProfileSearchResultsResolver struct {
	M model.ProvisioningProfileSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *ProvisioningProfileSearchResultsResolver) Results() []*ProvisioningProfileResolver {
	resolvers := make([]*ProvisioningProfileResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&ProvisioningProfileResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *ProvisioningProfileSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
