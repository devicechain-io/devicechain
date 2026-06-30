// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
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

func (r *MembershipResolver) Tenant() string  { return r.m.Tenant }
func (r *MembershipResolver) Roles() []string { return r.m.Roles }

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

// UpdateProfile lets the signed-in user edit their own display name (first/last).
// Self-scoped: it targets the identity carried in the caller's token, so being
// authenticated is sufficient; email and credentials are immutable here.
func (r *SchemaResolver) UpdateProfile(ctx context.Context, args struct {
	FirstName *string
	LastName  *string
}) (*CurrentIdentityResolver, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	id, err := r.getIdentityManager(ctx).UpdateProfile(ctx, claims.Username, args.FirstName, args.LastName)
	if err != nil {
		return nil, err
	}
	return &CurrentIdentityResolver{id: id}, nil
}
