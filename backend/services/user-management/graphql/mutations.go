// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/identity"
)

// AuthTokenResolver resolves the AuthToken GraphQL type (a tenant-scoped pair).
type AuthTokenResolver struct {
	pair *identity.TokenPair
}

func (r *AuthTokenResolver) AccessToken() string  { return r.pair.AccessToken }
func (r *AuthTokenResolver) RefreshToken() string { return r.pair.RefreshToken }
func (r *AuthTokenResolver) ExpiresAt() string    { return r.pair.ExpiresAt.UTC().Format(time.RFC3339) }

// IdentityAuthResolver resolves the IdentityAuth GraphQL type: the instance-scoped
// identity token plus the tenants the identity may select (ADR-033).
type IdentityAuthResolver struct {
	auth *identity.IdentityAuth
}

func (r *IdentityAuthResolver) IdentityToken() string { return r.auth.IdentityToken }
func (r *IdentityAuthResolver) ExpiresAt() string {
	return r.auth.ExpiresAt.UTC().Format(time.RFC3339)
}
func (r *IdentityAuthResolver) Superuser() bool { return r.auth.Superuser }
func (r *IdentityAuthResolver) Memberships() []*MembershipResolver {
	out := make([]*MembershipResolver, 0, len(r.auth.Memberships))
	for i := range r.auth.Memberships {
		out = append(out, &MembershipResolver{m: r.auth.Memberships[i]})
	}
	return out
}

// MembershipResolver resolves the Membership GraphQL type.
type MembershipResolver struct {
	m identity.MembershipInfo
}

func (r *MembershipResolver) Tenant() string    { return r.m.Tenant }
func (r *MembershipResolver) Roles() []string   { return r.m.Roles }
func (r *MembershipResolver) SsoRequired() bool { return r.m.SsoRequired }
func (r *MembershipResolver) MfaRequired() bool { return r.m.MfaRequired }

// Login authenticates an email/password and returns an identity token + the
// identity's memberships (ADR-033). The caller then picks a tenant via
// selectTenant to obtain a tenant-scoped token pair.
func (r *SchemaResolver) Login(ctx context.Context, args struct {
	Email    string
	Password string
}) (*IdentityAuthResolver, error) {
	res, err := r.getIdentityManager(ctx).Login(ctx, args.Email, args.Password)
	if err != nil {
		return nil, err
	}
	return &IdentityAuthResolver{auth: res}, nil
}

// SelectTenant exchanges an identity token for a tenant-scoped token pair.
func (r *SchemaResolver) SelectTenant(ctx context.Context, args struct {
	IdentityToken string
	Tenant        string
}) (*AuthTokenResolver, error) {
	pair, err := r.getIdentityManager(ctx).SelectTenant(ctx, args.IdentityToken, args.Tenant)
	if err != nil {
		return nil, err
	}
	return &AuthTokenResolver{pair: pair}, nil
}

// Refresh exchanges a refresh token for a new token pair.
func (r *SchemaResolver) Refresh(ctx context.Context, args struct {
	RefreshToken string
}) (*AuthTokenResolver, error) {
	pair, err := r.getIdentityManager(ctx).Refresh(ctx, args.RefreshToken)
	if err != nil {
		return nil, err
	}
	return &AuthTokenResolver{pair: pair}, nil
}

// Logout ends the sign-in a refresh or identity token belongs to (server-side sign-out)
// and, when asked for an app to return to, says where to sign out at the provider too.
func (r *SchemaResolver) Logout(ctx context.Context, args struct {
	Token    string
	ReturnTo *string
}) (*SignOutResolver, error) {
	var app identity.SignOutReturn
	if args.ReturnTo != nil {
		// The schema's enum has already refused any other value.
		app = identity.SignOutReturn(*args.ReturnTo)
	}
	u, err := r.getIdentityManager(ctx).SignOut(ctx, args.Token, app)
	if err != nil {
		return nil, err
	}
	return &SignOutResolver{providerURL: u}, nil
}

// SignOutResolver resolves SignOut.
type SignOutResolver struct {
	providerURL string
}

// ProviderSignOutUrl is the provider's sign-out address, or null when there is none.
func (r *SignOutResolver) ProviderSignOutUrl() *string {
	if r.providerURL == "" {
		return nil
	}
	return &r.providerURL
}

// UpdateProfile lets the signed-in user edit their own display name (first/last).
// Self-scoped: it targets the identity carried in the caller's token, so being
// authenticated is sufficient; email and credentials are immutable here.
//
// It takes a dedicated ProfileUpdateRequest rather than two inline arguments. The
// arguments were already effectively three-state — a nil pointer left a field alone —
// but on a hand-rolled mechanism nothing certified and the exhaustiveness guard could
// not see. See identity.ProfileUpdateRequest for what each state does, including why
// null and "" agree here.
func (r *SchemaResolver) UpdateProfile(ctx context.Context, args struct {
	Request identity.ProfileUpdateRequest
}) (*CurrentIdentityResolver, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	id, err := r.getIdentityManager(ctx).UpdateProfile(ctx, claims.Username, &args.Request)
	if err != nil {
		return nil, err
	}
	scope, _ := auth.ScopeFromContext(ctx)
	return &CurrentIdentityResolver{id: id, scope: scope}, nil
}

// SsoBeginResolver resolves the SsoBegin GraphQL type.
type SsoBeginResolver struct {
	b *identity.SsoBegin
}

func (r *SsoBeginResolver) AuthorizationUrl() string { return r.b.AuthorizationURL }

// BeginSso starts a single sign-on. Public, and rate limited per source address.
func (r *SchemaResolver) BeginSso(ctx context.Context, args struct {
	Email         *string
	Connection    *string
	ReturnTo      string
	CodeChallenge string
}) (*SsoBeginResolver, error) {
	in := identity.BeginSsoInput{ReturnTo: args.ReturnTo, CodeChallenge: args.CodeChallenge}
	if args.Email != nil {
		in.Email = *args.Email
	}
	if args.Connection != nil {
		in.Connection = *args.Connection
	}
	b, err := r.getIdentityManager(ctx).BeginSso(ctx, in, gqlcore.SourceAddr(ctx))
	if err != nil || b == nil {
		return nil, err
	}
	return &SsoBeginResolver{b: b}, nil
}

// CompleteSso finishes a single sign-on with the login code and the browser's verifier.
func (r *SchemaResolver) CompleteSso(ctx context.Context, args struct {
	Code         string
	CodeVerifier string
}) (*IdentityAuthResolver, error) {
	res, err := r.getIdentityManager(ctx).CompleteSso(ctx, args.Code, args.CodeVerifier)
	if err != nil {
		return nil, err
	}
	return &IdentityAuthResolver{auth: res}, nil
}

// BeginSsoLink starts linking the signed-in person's provider account to their identity.
func (r *SchemaResolver) BeginSsoLink(ctx context.Context, args struct {
	IdentityToken string
	ReturnTo      string
}) (*SsoBeginResolver, error) {
	b, err := r.getIdentityManager(ctx).BeginSsoLink(ctx, args.IdentityToken, args.ReturnTo, gqlcore.SourceAddr(ctx))
	if err != nil {
		return nil, err
	}
	return &SsoBeginResolver{b: b}, nil
}
