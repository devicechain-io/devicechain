// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/iam"
)

// TenantResolver resolves the Tenant GraphQL type: the control-plane tenant the
// caller is acting within. It backs the console's tenant header today; branding
// (logo/colors) will extend this type as it lands.
type TenantResolver struct {
	t *iam.Tenant
}

func (r *TenantResolver) Token() string        { return r.t.Token }
func (r *TenantResolver) Name() *string        { return util.NullStr(r.t.Name) }
func (r *TenantResolver) Description() *string { return util.NullStr(r.t.Description) }

// Tenant describes the tenant the caller is acting within (ADR-033 data plane).
// It is self-scoped — a caller only ever sees their own tenant, resolved from the
// tenant in their access token — so being authenticated to the tenant is enough.
func (r *SchemaResolver) Tenant(ctx context.Context) (*TenantResolver, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	t, err := r.getIdentityManager(ctx).CurrentTenant(ctx, claims.Tenant)
	if err != nil {
		return nil, err
	}
	return &TenantResolver{t: t}, nil
}

// CurrentIdentityResolver resolves the CurrentIdentity GraphQL type: the global
// identity the caller is signed in as, for display in the console (name, falling
// back to email).
type CurrentIdentityResolver struct {
	id *iam.Identity
}

func (r *CurrentIdentityResolver) Email() string      { return r.id.Email }
func (r *CurrentIdentityResolver) FirstName() *string { return optStr(r.id.FirstName) }
func (r *CurrentIdentityResolver) LastName() *string  { return optStr(r.id.LastName) }

// Me describes the identity the caller is signed in as (ADR-033). Self-scoped:
// resolved from the email carried as the access token's subject, so being
// authenticated is enough.
func (r *SchemaResolver) Me(ctx context.Context) (*CurrentIdentityResolver, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	id, err := r.getIdentityManager(ctx).CurrentUser(ctx, claims.Username)
	if err != nil {
		return nil, err
	}
	return &CurrentIdentityResolver{id: id}, nil
}
