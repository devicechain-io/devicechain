// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	gql "github.com/graph-gophers/graphql-go"
)

// ContextAdminSsoKey injects the identity Manager into the ADMIN request context. The
// connection administration needs what the Manager owns -- the secret store, the
// egress-guarded HTTP client, the sign-in session rules -- and not the admin Service's plain
// store access.
const ContextAdminSsoKey = gqlcore.ContextKey("admin-sso")

func (r *AdminResolver) sso(ctx context.Context) *identity.Manager {
	return ctx.Value(ContextAdminSsoKey).(*identity.Manager)
}

// IdpConnectionResolver resolves the IdpConnection type.
type IdpConnectionResolver struct {
	d identity.IdpConnectionDetail
}

func wrapIdp(d *identity.IdpConnectionDetail, err error) (*IdpConnectionResolver, error) {
	if err != nil {
		return nil, err
	}
	return &IdpConnectionResolver{d: *d}, nil
}

func (r *IdpConnectionResolver) Token() string    { return r.d.Connection.Token }
func (r *IdpConnectionResolver) Name() string     { return r.d.Connection.Name }
func (r *IdpConnectionResolver) Kind() string     { return string(r.d.Connection.Kind) }
func (r *IdpConnectionResolver) Enabled() bool    { return r.d.Connection.Enabled }
func (r *IdpConnectionResolver) Issuer() string   { return r.d.Connection.Issuer }
func (r *IdpConnectionResolver) ClientId() string { return r.d.Connection.ClientID }
func (r *IdpConnectionResolver) HasSecret() bool  { return r.d.HasSecret }
func (r *IdpConnectionResolver) Scopes() []string { return nonNil(r.d.Connection.Scopes) }
func (r *IdpConnectionResolver) EmailClaim() string {
	return r.d.Connection.EmailClaim
}
func (r *IdpConnectionResolver) GroupsClaim() string { return r.d.Connection.GroupsClaim }
func (r *IdpConnectionResolver) JitEnabled() bool    { return r.d.Connection.JitEnabled }
func (r *IdpConnectionResolver) DefaultTenantRoles() []string {
	return nonNil(r.d.Connection.DefaultTenantRoles)
}
func (r *IdpConnectionResolver) RoleMappings() []*IdpRoleMappingResolver {
	out := make([]*IdpRoleMappingResolver, 0, len(r.d.RoleMappings))
	for i := range r.d.RoleMappings {
		out = append(out, &IdpRoleMappingResolver{d: r.d.RoleMappings[i]})
	}
	return out
}
func (r *IdpConnectionResolver) RequiredClaims() []*IdpClaimRequirementResolver {
	names := make([]string, 0, len(r.d.Connection.RequiredClaims))
	for k := range r.d.Connection.RequiredClaims {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]*IdpClaimRequirementResolver, 0, len(names))
	for _, k := range names {
		out = append(out, &IdpClaimRequirementResolver{name: k, value: r.d.Connection.RequiredClaims[k]})
	}
	return out
}
func (r *IdpConnectionResolver) RequiredAmr() []string {
	return nonNil(r.d.Connection.RequiredAmr)
}
func (r *IdpConnectionResolver) Domains() []*IdpDomainResolver {
	out := make([]*IdpDomainResolver, 0, len(r.d.Domains))
	for i := range r.d.Domains {
		out = append(out, &IdpDomainResolver{d: r.d.Domains[i]})
	}
	return out
}
func (r *IdpConnectionResolver) Tenants() []string { return nonNil(r.d.Tenants) }
func (r *IdpConnectionResolver) SessionMaxAgeSeconds() int32 {
	return int32(r.d.Connection.SessionMaxAgeSeconds)
}
func (r *IdpConnectionResolver) CallbackUrl() string { return r.d.CallbackURL }
func (r *IdpConnectionResolver) ProviderSignOut() bool {
	return r.d.Connection.ProviderSignOut
}
func (r *IdpConnectionResolver) CaCertificates() []*IdpCaCertificateResolver {
	out := make([]*IdpCaCertificateResolver, 0, len(r.d.CaCertificates))
	for i := range r.d.CaCertificates {
		out = append(out, &IdpCaCertificateResolver{c: r.d.CaCertificates[i]})
	}
	return out
}

// IdpCaCertificateResolver resolves the IdpCaCertificate type.
type IdpCaCertificateResolver struct{ c x509trust.CaCertificate }

func (r *IdpCaCertificateResolver) FingerprintSha256() string { return r.c.FingerprintSHA256 }
func (r *IdpCaCertificateResolver) Subject() string           { return r.c.Subject }
func (r *IdpCaCertificateResolver) Issuer() string            { return r.c.Issuer }
func (r *IdpCaCertificateResolver) NotBefore() string         { return *gqlcore.FormatTime(r.c.NotBefore) }
func (r *IdpCaCertificateResolver) NotAfter() string          { return *gqlcore.FormatTime(r.c.NotAfter) }
func (r *IdpCaCertificateResolver) IsCa() bool                { return r.c.IsCA }

func (r *IdpConnectionResolver) SignOutReturnUrls() []string {
	return nonNil(r.d.SignOutReturnURLs)
}
func (r *IdpConnectionResolver) Discovery() *IdpDiscoveryResolver {
	return &IdpDiscoveryResolver{d: r.d}
}
func (r *IdpConnectionResolver) CreatedAt() *string {
	return gqlcore.FormatTime(r.d.Connection.CreatedAt)
}
func (r *IdpConnectionResolver) UpdatedAt() *string {
	return gqlcore.FormatTime(r.d.Connection.UpdatedAt)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// IdpRoleMappingResolver resolves the IdpRoleMapping type.
type IdpRoleMappingResolver struct{ d identity.IdpRoleMappingDetail }

func (r *IdpRoleMappingResolver) Tenant() string      { return r.d.Tenant }
func (r *IdpRoleMappingResolver) Group() string       { return r.d.Group }
func (r *IdpRoleMappingResolver) Role() string        { return r.d.Role }
func (r *IdpRoleMappingResolver) FullAuthority() bool { return r.d.FullAuthority }

// IdpClaimRequirementResolver resolves the IdpClaimRequirement type.
type IdpClaimRequirementResolver struct{ name, value string }

func (r *IdpClaimRequirementResolver) Name() string  { return r.name }
func (r *IdpClaimRequirementResolver) Value() string { return r.value }

// IdpDomainResolver resolves the IdpDomain type.
type IdpDomainResolver struct{ d iam.IdpConnectionDomain }

func (r *IdpDomainResolver) Domain() string     { return r.d.Domain }
func (r *IdpDomainResolver) VerifiedAt() string { return *gqlcore.FormatTime(r.d.VerifiedAt) }
func (r *IdpDomainResolver) VerifiedBy() string { return r.d.VerifiedBy }

// IdpDiscoveryResolver resolves the IdpDiscovery type.
type IdpDiscoveryResolver struct{ d identity.IdpConnectionDetail }

func (r *IdpDiscoveryResolver) snap() iam.DiscoverySnapshot {
	if r.d.Connection.DiscoverySnapshot == nil {
		return iam.DiscoverySnapshot{}
	}
	return *r.d.Connection.DiscoverySnapshot
}
func (r *IdpDiscoveryResolver) AuthorizationEndpoint() string { return r.snap().AuthorizationEndpoint }
func (r *IdpDiscoveryResolver) TokenEndpoint() string         { return r.snap().TokenEndpoint }
func (r *IdpDiscoveryResolver) JwksUri() string               { return r.snap().JwksURI }
func (r *IdpDiscoveryResolver) EndSessionEndpoint() *string {
	if e := r.snap().EndSessionEndpoint; e != "" {
		return &e
	}
	return nil
}
func (r *IdpDiscoveryResolver) FetchedAt() *string  { return gqlcore.FormatTime(r.snap().FetchedAt) }
func (r *IdpDiscoveryResolver) Fingerprint() string { return r.d.Fingerprint }
func (r *IdpDiscoveryResolver) Approved() bool      { return r.d.Connection.DiscoveryApprovedAt != nil }
func (r *IdpDiscoveryResolver) ApprovedBy() *string {
	if r.d.Connection.DiscoveryApprovedBy == "" {
		return nil
	}
	return &r.d.Connection.DiscoveryApprovedBy
}
func (r *IdpDiscoveryResolver) ApprovedAt() *string {
	if r.d.Connection.DiscoveryApprovedAt == nil {
		return nil
	}
	return gqlcore.FormatTime(*r.d.Connection.DiscoveryApprovedAt)
}

// SsoLinkRequestResolver resolves the SsoLinkRequest type.
type SsoLinkRequestResolver struct{ v identity.SsoLinkRequestView }

func (r *SsoLinkRequestResolver) Id() gql.ID         { return gql.ID(fmt.Sprint(r.v.Request.ID)) }
func (r *SsoLinkRequestResolver) Connection() string { return r.v.Connection }
func (r *SsoLinkRequestResolver) Subject() string    { return r.v.Request.Subject }
func (r *SsoLinkRequestResolver) Email() string      { return r.v.Request.Email }
func (r *SsoLinkRequestResolver) CreatedAt() *string {
	return gqlcore.FormatTime(r.v.Request.CreatedAt)
}
func (r *SsoLinkRequestResolver) IdentityExists() bool { return r.v.IdentityExists }
func (r *SsoLinkRequestResolver) SubjectChanged() bool { return r.v.SubjectChanged }
func (r *SsoLinkRequestResolver) ManualMemberships() []string {
	return nonNil(r.v.ManualMemberships)
}

// IdpConnections lists the connections (requires user:read).
func (r *AdminResolver) IdpConnections(ctx context.Context) ([]*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserRead); err != nil {
		return nil, err
	}
	details, err := r.sso(ctx).ListIdpConnections(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*IdpConnectionResolver, 0, len(details))
	for i := range details {
		out = append(out, &IdpConnectionResolver{d: details[i]})
	}
	return out, nil
}

// IdpConnection returns one connection or null (requires user:read).
func (r *AdminResolver) IdpConnection(ctx context.Context, args struct{ Token string }) (*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserRead); err != nil {
		return nil, err
	}
	d, err := r.sso(ctx).IdpConnection(ctx, args.Token)
	if errors.Is(err, identity.ErrIdpNotFound) {
		return nil, nil
	}
	return wrapIdp(d, err)
}

// SsoLinkRequests lists pending link requests (requires user:read).
func (r *AdminResolver) SsoLinkRequests(ctx context.Context) ([]*SsoLinkRequestResolver, error) {
	if err := auth.Authorize(ctx, auth.UserRead); err != nil {
		return nil, err
	}
	views, err := r.sso(ctx).ListSsoLinkRequests(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*SsoLinkRequestResolver, 0, len(views))
	for i := range views {
		out = append(out, &SsoLinkRequestResolver{v: views[i]})
	}
	return out, nil
}

type idpClaimRequirementInput struct {
	Name  string
	Value string
}

type idpConnectionCreateInput struct {
	Token            string
	Name             string
	Kind             string
	Issuer           string
	ClientId         string
	ClientSecret     *string
	Scopes           *[]string
	EmailClaim       *string
	GroupsClaim      *string
	RequiredClaims   *[]idpClaimRequirementInput
	RequiredAmr      *[]string
	MfaEnforcedByIdp *bool
	CaBundle         *string
}

func claimsMap(in *[]idpClaimRequirementInput) (map[string]string, error) {
	if in == nil {
		return nil, nil
	}
	out := make(map[string]string, len(*in))
	for _, c := range *in {
		if _, dup := out[c.Name]; dup {
			return nil, fmt.Errorf("the claim %q is listed twice", c.Name)
		}
		out[c.Name] = c.Value
	}
	return out, nil
}

// CreateIdpConnection creates a connection (requires user:write).
func (r *AdminResolver) CreateIdpConnection(ctx context.Context, args struct {
	Request idpConnectionCreateInput
}) (*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	rq := args.Request
	claims, err := claimsMap(rq.RequiredClaims)
	if err != nil {
		return nil, err
	}
	in := identity.IdpConnectionInput{
		Token: rq.Token, Name: rq.Name, Kind: iam.IdpKind(rq.Kind), Issuer: rq.Issuer, ClientID: rq.ClientId,
		RequiredClaims: claims,
	}
	if rq.ClientSecret != nil {
		in.ClientSecret = *rq.ClientSecret
	}
	if rq.Scopes != nil {
		in.Scopes = *rq.Scopes
	}
	if rq.EmailClaim != nil {
		in.EmailClaim = *rq.EmailClaim
	}
	if rq.GroupsClaim != nil {
		in.GroupsClaim = *rq.GroupsClaim
	}
	if rq.RequiredAmr != nil {
		in.RequiredAmr = *rq.RequiredAmr
	}
	if rq.MfaEnforcedByIdp != nil {
		in.MfaEnforcedByIdp = *rq.MfaEnforcedByIdp
	}
	if rq.CaBundle != nil {
		in.CaBundle = *rq.CaBundle
	}
	return wrapIdp(r.sso(ctx).CreateIdpConnection(ctx, in))
}

// SetIdpCaBundle replaces or clears the private CA bundle (requires user:write).
func (r *AdminResolver) SetIdpCaBundle(ctx context.Context, args struct {
	Token    string
	CaBundle string
}) (*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	return wrapIdp(r.sso(ctx).SetIdpCaBundle(ctx, args.Token, args.CaBundle))
}

// UpdateIdpConnection applies a partial update (requires user:write).
func (r *AdminResolver) UpdateIdpConnection(ctx context.Context, args struct {
	Token   string
	Request identity.IdpConnectionUpdateRequest
}) (*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	return wrapIdp(r.sso(ctx).UpdateIdpConnection(ctx, args.Token, &args.Request))
}

// SetIdpRequiredClaims replaces the required claims (requires user:write).
func (r *AdminResolver) SetIdpRequiredClaims(ctx context.Context, args struct {
	Token  string
	Claims []idpClaimRequirementInput
}) (*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	claims, err := claimsMap(&args.Claims)
	if err != nil {
		return nil, err
	}
	return wrapIdp(r.sso(ctx).SetIdpRequiredClaims(ctx, args.Token, claims))
}

// DeleteIdpConnection deletes a connection (requires user:write).
func (r *AdminResolver) DeleteIdpConnection(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return false, err
	}
	return r.sso(ctx).DeleteIdpConnection(ctx, args.Token)
}

// SetIdpConnectionSecret stores the client secret, write-only (requires user:write).
func (r *AdminResolver) SetIdpConnectionSecret(ctx context.Context, args struct {
	Token  string
	Secret string
}) (*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	if err := r.sso(ctx).SetIdpConnectionSecret(ctx, args.Token, args.Secret); err != nil {
		return nil, err
	}
	return wrapIdp(r.sso(ctx).IdpConnection(ctx, args.Token))
}

// SetIdpConnectionTenants sets the tenants a connection serves (requires user:write).
func (r *AdminResolver) SetIdpConnectionTenants(ctx context.Context, args struct {
	Token   string
	Tenants []string
}) (*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	return wrapIdp(r.sso(ctx).SetIdpConnectionTenants(ctx, args.Token, args.Tenants))
}

// AddIdpDomain asserts a domain for a connection (requires user:write).
func (r *AdminResolver) AddIdpDomain(ctx context.Context, args struct{ Token, Domain string }) (*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	return wrapIdp(r.sso(ctx).AddIdpDomain(ctx, args.Token, args.Domain))
}

// RemoveIdpDomain removes a connection's domain (requires user:write).
func (r *AdminResolver) RemoveIdpDomain(ctx context.Context, args struct{ Token, Domain string }) (*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	return wrapIdp(r.sso(ctx).RemoveIdpDomain(ctx, args.Token, args.Domain))
}

// RefreshIdpDiscovery re-fetches the discovery document (requires user:write).
func (r *AdminResolver) RefreshIdpDiscovery(ctx context.Context, args struct{ Token string }) (*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	return wrapIdp(r.sso(ctx).RefreshIdpDiscovery(ctx, args.Token))
}

// ApproveIdpDiscovery approves the reviewed snapshot (requires user:write).
func (r *AdminResolver) ApproveIdpDiscovery(ctx context.Context, args struct{ Token, Fingerprint string }) (*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	return wrapIdp(r.sso(ctx).ApproveIdpDiscovery(ctx, args.Token, args.Fingerprint))
}

// LinkIdentity links an external account to an identity (requires user:write).
func (r *AdminResolver) LinkIdentity(ctx context.Context, args struct{ Email, Connection, Subject string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return false, err
	}
	if err := r.sso(ctx).LinkIdentity(ctx, args.Email, args.Connection, args.Subject); err != nil {
		return false, err
	}
	return true, nil
}

// UnlinkIdentity removes a link (requires user:write).
func (r *AdminResolver) UnlinkIdentity(ctx context.Context, args struct{ Email, Connection string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return false, err
	}
	return r.sso(ctx).UnlinkIdentity(ctx, args.Email, args.Connection)
}

func parseRequestID(id gql.ID) (uint, error) {
	n, err := strconv.ParseUint(string(id), 10, 32)
	if err != nil {
		return 0, identity.ErrLinkRequestNotFound
	}
	return uint(n), nil
}

// ApproveSsoLinkRequest links the request's account (requires user:write).
func (r *AdminResolver) ApproveSsoLinkRequest(ctx context.Context, args struct {
	Id                    gql.ID
	MembershipDisposition *string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return false, err
	}
	id, err := parseRequestID(args.Id)
	if err != nil {
		return false, err
	}
	disposition := ""
	if args.MembershipDisposition != nil {
		disposition = *args.MembershipDisposition
	}
	if err := r.sso(ctx).ApproveSsoLinkRequest(ctx, id, disposition); err != nil {
		return false, err
	}
	return true, nil
}

// RejectSsoLinkRequest deletes a request (requires user:write).
func (r *AdminResolver) RejectSsoLinkRequest(ctx context.Context, args struct{ Id gql.ID }) (bool, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return false, err
	}
	id, err := parseRequestID(args.Id)
	if err != nil {
		return false, err
	}
	return r.sso(ctx).RejectSsoLinkRequest(ctx, id)
}

type idpRoleMappingInput struct {
	Group string
	Role  string
}

// SetIdpRoleMappings replaces a connection's group mappings for a tenant (requires
// user:write).
func (r *AdminResolver) SetIdpRoleMappings(ctx context.Context, args struct {
	Connection               string
	Tenant                   string
	Mappings                 []idpRoleMappingInput
	AcknowledgeFullAuthority *bool
}) (*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	in := make([]identity.RoleMappingInput, 0, len(args.Mappings))
	for _, m := range args.Mappings {
		in = append(in, identity.RoleMappingInput{Group: m.Group, Role: m.Role})
	}
	ack := args.AcknowledgeFullAuthority != nil && *args.AcknowledgeFullAuthority
	return wrapIdp(r.sso(ctx).SetIdpRoleMappings(ctx, args.Connection, args.Tenant, in, ack))
}

// SetIdpJit sets just-in-time provisioning and the default tenant roles (requires
// user:write).
func (r *AdminResolver) SetIdpJit(ctx context.Context, args struct {
	Connection               string
	Enabled                  bool
	DefaultTenantRoles       []string
	AcknowledgeFullAuthority *bool
}) (*IdpConnectionResolver, error) {
	if err := auth.Authorize(ctx, auth.UserWrite); err != nil {
		return nil, err
	}
	ack := args.AcknowledgeFullAuthority != nil && *args.AcknowledgeFullAuthority
	return wrapIdp(r.sso(ctx).SetIdpJit(ctx, args.Connection, args.Enabled, args.DefaultTenantRoles, ack))
}
