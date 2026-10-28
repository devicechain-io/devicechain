// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
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

// CredentialId is the MQTT username as sent, and for an ACCESS_TOKEN the token ONLY ON
// THE RESPONSE TO THE WRITE THAT SET IT (IssuedCredentialId). The platform stores an access
// token as its keyed digest, so every read afterwards returns that digest, a fingerprint
// that identifies the credential and cannot be presented as the token. The token is shown
// once, as an MQTT_BASIC password is.
//
// The queries returning this type stay gated on device:WRITE, which they no longer need to
// be for the sake of the token; see queries_credentials.go.
func (r *DeviceCredentialResolver) CredentialId() string {
	if r.M.IssuedCredentialId != "" {
		return r.M.IssuedCredentialId
	}
	return r.M.CredentialId
}

// CredentialValue is write-only: the stored secret is never returned on read. It
// is accepted on create/update and shown once, client-side, by whoever submitted
// it.
//
// Note what this does and does not buy. Only MQTT_BASIC keeps a comparable secret
// here, so this defends the MQTT_BASIC password and nothing else — it is not, on
// its own, a guarantee that a credential cannot be exfiltrated. The authority gate
// on the queries is what makes that true for the other types.
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

// ProvisioningProfile is the profile whose registration issued the credential, or nil.
func (r *DeviceCredentialResolver) ProvisioningProfile(ctx context.Context) (*ProvisioningProfileResolver, error) {
	if r.M.ProvisioningProfileId == nil {
		return nil, nil
	}
	return r.S.provisioningProfileByID(ctx, *r.M.ProvisioningProfileId)
}

// Certificate is the pinned certificate of an X509_CERTIFICATE credential, or nil. Requires
// device:read, unscoped, because it can name the credential's trust anchor.
func (r *DeviceCredentialResolver) Certificate(ctx context.Context) (*X509CertificateResolver, error) {
	info := r.M.X509Info()
	if info == nil {
		return nil, nil
	}
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	return &X509CertificateResolver{M: *info, S: r.S, C: r.C}, nil
}

// X509CertificateResolver resolves the X509Certificate type.
type X509CertificateResolver struct {
	M model.X509CertificateInfo
	S *SchemaResolver
	C context.Context
}

func (r *X509CertificateResolver) FingerprintSha256() string { return r.M.FingerprintSha256 }
func (r *X509CertificateResolver) SubjectCn() string         { return r.M.SubjectCn }
func (r *X509CertificateResolver) SerialHex() string         { return r.M.SerialHex }
func (r *X509CertificateResolver) NotAfter() string          { return *util.FormatTime(r.M.NotAfter) }
func (r *X509CertificateResolver) TrustAnchor(ctx context.Context) (*DeviceTrustAnchorResolver, error) {
	if r.M.TrustAnchorId == 0 {
		return nil, nil
	}
	return r.S.trustAnchorByID(ctx, r.M.TrustAnchorId)
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
