// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"time"

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

// DeviceRevokedCertificateResolver resolves DeviceRevokedCertificate.
type DeviceRevokedCertificateResolver struct {
	M model.DeviceRevokedCertificate
	S *SchemaResolver
	C context.Context
	// Ended is the revoke mutation's verdict on the live sessions; nil on every other path.
	Ended *bool
}

func (r *DeviceRevokedCertificateResolver) SessionsEnded() *bool { return r.Ended }

func (r *DeviceRevokedCertificateResolver) Kind() string               { return r.M.Kind }
func (r *DeviceRevokedCertificateResolver) FingerprintSha256() *string { return r.M.Fingerprint }
func (r *DeviceRevokedCertificateResolver) IssuerKey() *string         { return r.M.IssuerKey }
func (r *DeviceRevokedCertificateResolver) Serial() *string            { return r.M.Serial }
func (r *DeviceRevokedCertificateResolver) Reason() string {
	return model.RevocationReasonEnum(r.M.Reason)
}
func (r *DeviceRevokedCertificateResolver) RevokedAt() string { return *util.FormatTime(r.M.RevokedAt) }

// DeviceRevokedCertificateSearchResultsResolver resolves DeviceRevokedCertificateSearchResults.
type DeviceRevokedCertificateSearchResultsResolver struct {
	M model.DeviceRevokedCertificateSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceRevokedCertificateSearchResultsResolver) Results() []*DeviceRevokedCertificateResolver {
	out := make([]*DeviceRevokedCertificateResolver, 0, len(r.M.Results))
	for _, current := range r.M.Results {
		out = append(out, &DeviceRevokedCertificateResolver{M: current, S: r.S, C: r.C})
	}
	return out
}

func (r *DeviceRevokedCertificateSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

// RevocationMode is the anchor's mode; a stored value this build does not know is an error, not ENTRIES.
func (r *DeviceTrustAnchorResolver) RevocationMode() (string, error) {
	m, err := r.M.RevocationModeOf()
	return string(m), err
}

// RevocationStaleGrace is the grace in force for the anchor.
func (r *DeviceTrustAnchorResolver) RevocationStaleGrace(ctx context.Context) (string, error) {
	g, err := r.M.RevocationStaleGraceOf(r.S.GetApi(ctx).RevocationLimits().DefaultStaleGrace)
	if err != nil {
		return "", err
	}
	return g.String(), nil
}

// RevocationLists are the anchor's current lists.
func (r *DeviceTrustAnchorResolver) RevocationLists(ctx context.Context) ([]*DeviceRevocationListResolver, error) {
	api := r.S.GetApi(ctx)
	lists, err := api.DeviceRevocationListsOf(ctx, r.M.ID)
	if err != nil {
		return nil, err
	}
	grace, err := r.M.RevocationStaleGraceOf(api.RevocationLimits().DefaultStaleGrace)
	if err != nil {
		return nil, err
	}
	out := make([]*DeviceRevocationListResolver, 0, len(lists))
	for _, l := range lists {
		out = append(out, &DeviceRevocationListResolver{M: l, Grace: grace})
	}
	return out, nil
}

// RevocationStatus is the worst freshness over the anchor's lists.
func (r *DeviceTrustAnchorResolver) RevocationStatus(ctx context.Context) (string, error) {
	api := r.S.GetApi(ctx)
	lists, err := api.DeviceRevocationListsOf(ctx, r.M.ID)
	if err != nil {
		return "", err
	}
	grace, err := r.M.RevocationStaleGraceOf(api.RevocationLimits().DefaultStaleGrace)
	if err != nil {
		return "", err
	}
	return model.RevocationAnchorStatus(lists, grace, time.Now()), nil
}

// DeviceRevocationListResolver resolves DeviceRevocationList.
type DeviceRevocationListResolver struct {
	M     model.DeviceRevocationList
	Grace time.Duration
}

func (r *DeviceRevocationListResolver) IssuerSubject() string { return r.M.IssuerSubject }
func (r *DeviceRevocationListResolver) IssuerKey() string     { return r.M.IssuerKey }
func (r *DeviceRevocationListResolver) IssuerName() string    { return r.M.IssuerName }
func (r *DeviceRevocationListResolver) CrlNumber() string     { return r.M.CrlNumber }
func (r *DeviceRevocationListResolver) ThisUpdate() string    { return *util.FormatTime(r.M.ThisUpdate) }
func (r *DeviceRevocationListResolver) NextUpdate() string    { return *util.FormatTime(r.M.NextUpdate) }
func (r *DeviceRevocationListResolver) EntryCount() int32     { return r.M.EntryCount }
func (r *DeviceRevocationListResolver) CrlSha256() string     { return r.M.CrlSha256 }
func (r *DeviceRevocationListResolver) Source() string        { return r.M.Source }
func (r *DeviceRevocationListResolver) Status() string {
	return model.RevocationListStatus(&r.M, r.Grace, time.Now())
}

// DeviceRevocationListResultResolver resolves DeviceRevocationListResult.
type DeviceRevocationListResultResolver struct {
	M     model.RevocationListPutResult
	Grace time.Duration
}

func (r *DeviceRevocationListResultResolver) List() *DeviceRevocationListResolver {
	return &DeviceRevocationListResolver{M: r.M.List, Grace: r.Grace}
}
func (r *DeviceRevocationListResultResolver) NewlyRevoked() int32 { return int32(r.M.NewlyRevoked) }
func (r *DeviceRevocationListResultResolver) Unchanged() bool     { return r.M.Unchanged }
func (r *DeviceRevocationListResultResolver) SessionsEnded() bool { return r.M.SessionsEnded }
