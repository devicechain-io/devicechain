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

// DeviceTrustAnchorResolver resolves the DeviceTrustAnchor type.
type DeviceTrustAnchorResolver struct {
	M model.DeviceTrustAnchor
	S *SchemaResolver
	C context.Context
}

func (r *DeviceTrustAnchorResolver) Id() gql.ID             { return gql.ID(fmt.Sprint(r.M.ID)) }
func (r *DeviceTrustAnchorResolver) CreatedAt() *string     { return util.FormatTime(r.M.CreatedAt) }
func (r *DeviceTrustAnchorResolver) UpdatedAt() *string     { return util.FormatTime(r.M.UpdatedAt) }
func (r *DeviceTrustAnchorResolver) DeletedAt() *string     { return util.FormatTime(r.M.DeletedAt.Time) }
func (r *DeviceTrustAnchorResolver) Token() string          { return r.M.Token }
func (r *DeviceTrustAnchorResolver) Name() *string          { return util.NullStr(r.M.Name) }
func (r *DeviceTrustAnchorResolver) Description() *string   { return util.NullStr(r.M.Description) }
func (r *DeviceTrustAnchorResolver) Metadata() *string      { return util.MetadataStr(r.M.Metadata) }
func (r *DeviceTrustAnchorResolver) CertificatePem() string { return r.M.CertificatePem }
func (r *DeviceTrustAnchorResolver) FingerprintSha256() string {
	return r.M.FingerprintSha256
}
func (r *DeviceTrustAnchorResolver) Subject() string   { return r.M.Subject }
func (r *DeviceTrustAnchorResolver) Issuer() string    { return r.M.Issuer }
func (r *DeviceTrustAnchorResolver) NotBefore() string { return *util.FormatTime(r.M.NotBefore) }
func (r *DeviceTrustAnchorResolver) NotAfter() string  { return *util.FormatTime(r.M.NotAfter) }
func (r *DeviceTrustAnchorResolver) Enabled() bool     { return r.M.Enabled }

// CredentialCount is how many credentials rely on the anchor.
func (r *DeviceTrustAnchorResolver) CredentialCount(ctx context.Context) (int32, error) {
	n, err := r.S.GetApi(ctx).TrustAnchorCredentialCount(ctx, r.M.ID)
	return int32(n), err
}

// ProfileCount is how many provisioning profiles authenticate against the anchor.
func (r *DeviceTrustAnchorResolver) ProfileCount(ctx context.Context) (int32, error) {
	n, err := r.S.GetApi(ctx).TrustAnchorProfileCount(ctx, r.M.ID)
	return int32(n), err
}

// DeviceTrustAnchorSearchResultsResolver resolves DeviceTrustAnchorSearchResults.
type DeviceTrustAnchorSearchResultsResolver struct {
	M model.DeviceTrustAnchorSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceTrustAnchorSearchResultsResolver) Results() []*DeviceTrustAnchorResolver {
	out := make([]*DeviceTrustAnchorResolver, 0, len(r.M.Results))
	for _, current := range r.M.Results {
		out = append(out, &DeviceTrustAnchorResolver{M: current, S: r.S, C: r.C})
	}
	return out
}

func (r *DeviceTrustAnchorSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
