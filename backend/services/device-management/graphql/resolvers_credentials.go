// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// --------------------------
// Device credential resolver
// --------------------------

type DeviceCredentialResolver struct {
	M model.DeviceCredential
	S *SchemaResolver
	C context.Context
}

func (r *DeviceCredentialResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceCredentialResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceCredentialResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceCredentialResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceCredentialResolver) Token() string {
	return r.M.Token
}

func (r *DeviceCredentialResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *DeviceCredentialResolver) CredentialType() string {
	return r.M.CredentialType
}

func (r *DeviceCredentialResolver) CredentialId() string {
	return r.M.CredentialId
}

// CredentialValue is write-only: the stored secret (e.g. an MQTT_BASIC password)
// is never returned on read, so a device:read holder cannot exfiltrate secrets
// through the API. It is accepted on create/update and shown once, client-side,
// by whoever submitted it.
func (r *DeviceCredentialResolver) CredentialValue() *string {
	return nil
}

func (r *DeviceCredentialResolver) Enabled() bool {
	return r.M.Enabled
}

func (r *DeviceCredentialResolver) ExpiresAt() *string {
	if r.M.ExpiresAt.Valid {
		return util.FormatTime(r.M.ExpiresAt.Time)
	}
	return nil
}

func (r *DeviceCredentialResolver) Device() *DeviceResolver {
	if r.M.Device != nil {
		return &DeviceResolver{
			M: *r.M.Device,
			S: r.S,
			C: r.C,
		}
	} else {
		ids := []string{fmt.Sprintf("%d", r.M.DeviceId)}
		rez, err := r.S.DevicesById(r.C, struct{ Ids []string }{Ids: ids})
		if err != nil || len(rez) == 0 {
			return nil
		}
		return rez[0]
	}
}

// -----------------------------------------
// Device credential search results resolver
// -----------------------------------------

type DeviceCredentialSearchResultsResolver struct {
	M model.DeviceCredentialSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceCredentialSearchResultsResolver) Results() []*DeviceCredentialResolver {
	resolvers := make([]*DeviceCredentialResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&DeviceCredentialResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *DeviceCredentialSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
