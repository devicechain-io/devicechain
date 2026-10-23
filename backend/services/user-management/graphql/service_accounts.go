// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
)

// A tenant's service accounts on its own plane. Every resolver gates on auth.Authorize —
// member:read to look, member:write to change — which refuses a SCOPED caller first; the
// service then takes the caller and tenant from the verified claims and applies the
// no-escalation rules (admin/service_accounts.go).
//
// 🔴 NO RESOLVER HERE RETURNS A DIGEST. ServiceAccountSecretResolver exposes the hint and
// the dates only, and the cleartext secret reaches the wire through exactly one type,
// ServiceAccountCredentials, which only create and rotate return.

// ServiceAccountSecretResolver resolves ServiceAccountSecret.
type ServiceAccountSecretResolver struct {
	M   iam.ServiceAccountSecret
	now time.Time
}

func (r *ServiceAccountSecretResolver) Hint() string { return r.M.Hint }
func (r *ServiceAccountSecretResolver) CreatedAt() string {
	return r.M.CreatedAt.UTC().Format(time.RFC3339)
}
func (r *ServiceAccountSecretResolver) ExpiresAt() string {
	return r.M.ExpiresAt.UTC().Format(time.RFC3339)
}
func (r *ServiceAccountSecretResolver) LastUsedAt() *string { return rfc3339(r.M.LastUsedAt) }
func (r *ServiceAccountSecretResolver) Active() bool        { return r.M.Live(r.now) }

// ServiceAccountResolver resolves ServiceAccount.
type ServiceAccountResolver struct {
	M   iam.ServiceAccount
	now time.Time
}

func (r *ServiceAccountResolver) Token() string        { return r.M.Token }
func (r *ServiceAccountResolver) Name() *string        { return util.NullStr(r.M.Name) }
func (r *ServiceAccountResolver) Description() *string { return util.NullStr(r.M.Description) }
func (r *ServiceAccountResolver) ClientId() string     { return r.M.ClientId }
func (r *ServiceAccountResolver) Enabled() bool        { return r.M.Enabled }
func (r *ServiceAccountResolver) Roles() []*TenantRoleResolver {
	return tenantRoles(r.M.Roles)
}
func (r *ServiceAccountResolver) Scope() []*ScopeAnchorResolver { return scopeAnchors(r.M.Scope) }
func (r *ServiceAccountResolver) Secrets() []*ServiceAccountSecretResolver {
	out := make([]*ServiceAccountSecretResolver, 0, len(r.M.Secrets))
	for i := range r.M.Secrets {
		out = append(out, &ServiceAccountSecretResolver{M: r.M.Secrets[i], now: r.now})
	}
	return out
}
func (r *ServiceAccountResolver) CreatedBy() *string {
	if r.M.CreatedBy == "" {
		return nil
	}
	s := r.M.CreatedBy
	return &s
}
func (r *ServiceAccountResolver) CreatedAt() string { return r.M.CreatedAt.UTC().Format(time.RFC3339) }

func accountResolver(a *iam.ServiceAccount) *ServiceAccountResolver {
	return &ServiceAccountResolver{M: *a, now: time.Now().UTC()}
}

// ServiceAccountSearchResultsResolver resolves ServiceAccountSearchResults.
type ServiceAccountSearchResultsResolver struct {
	rows []iam.ServiceAccount
	pag  rdb.SearchResultsPagination
}

func (r *ServiceAccountSearchResultsResolver) Results() []*ServiceAccountResolver {
	out := make([]*ServiceAccountResolver, 0, len(r.rows))
	for i := range r.rows {
		out = append(out, accountResolver(&r.rows[i]))
	}
	return out
}

func (r *ServiceAccountSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.pag}
}

// ServiceAccountCredentialsResolver resolves ServiceAccountCredentials: the one type that
// carries a cleartext secret.
type ServiceAccountCredentialsResolver struct {
	account  *iam.ServiceAccount
	secret   string
	endpoint string
}

func (r *ServiceAccountCredentialsResolver) ServiceAccount() *ServiceAccountResolver {
	return accountResolver(r.account)
}
func (r *ServiceAccountCredentialsResolver) ClientId() string     { return r.account.ClientId }
func (r *ServiceAccountCredentialsResolver) ClientSecret() string { return r.secret }
func (r *ServiceAccountCredentialsResolver) TokenEndpoint() *string {
	if r.endpoint == "" {
		return nil
	}
	e := r.endpoint
	return &e
}

type serviceAccountCriteriaInput struct {
	PageNumber int32
	PageSize   int32
}

// ServiceAccounts lists one page of this tenant's service accounts (requires member:read).
func (r *SchemaResolver) ServiceAccounts(ctx context.Context, args struct {
	Criteria serviceAccountCriteriaInput
}) (*ServiceAccountSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberRead); err != nil {
		return nil, err
	}
	rows, pag, err := r.getMembersService(ctx).ServiceAccounts(ctx,
		rdb.Pagination{PageNumber: args.Criteria.PageNumber, PageSize: args.Criteria.PageSize})
	if err != nil {
		return nil, err
	}
	return &ServiceAccountSearchResultsResolver{rows: rows, pag: pag}, nil
}

// ServiceAccount reads one service account of this tenant (requires member:read).
func (r *SchemaResolver) ServiceAccount(ctx context.Context, args struct{ Token string }) (*ServiceAccountResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberRead); err != nil {
		return nil, err
	}
	a, err := r.getMembersService(ctx).ServiceAccount(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return accountResolver(a), nil
}

// ServiceAccountTokenEndpoint is where service accounts obtain tokens (requires member:read).
func (r *SchemaResolver) ServiceAccountTokenEndpoint(ctx context.Context) (*string, error) {
	if err := auth.Authorize(ctx, auth.MemberRead); err != nil {
		return nil, err
	}
	e := r.getIdentityManager(ctx).TokenEndpoint()
	if e == "" {
		return nil, nil
	}
	return &e, nil
}

type serviceAccountCreateInput struct {
	Token               string
	Name                *string
	Description         *string
	RoleTokens          []string
	Scope               *[]scopeAnchorInput
	SecretExpiresInDays *int32
}

// CreateServiceAccount creates a service account with its first secret (requires member:write).
func (r *SchemaResolver) CreateServiceAccount(ctx context.Context, args struct {
	Request serviceAccountCreateInput
}) (*ServiceAccountCredentialsResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberWrite); err != nil {
		return nil, err
	}
	if err := checkList("roleTokens", args.Request.RoleTokens); err != nil {
		return nil, err
	}
	var scope []admin.ScopeAnchorInput
	if args.Request.Scope != nil {
		var err error
		if scope, err = toScopeInput(*args.Request.Scope); err != nil {
			return nil, err
		}
	}
	in := admin.ServiceAccountInput{
		Token: args.Request.Token, RoleTokens: args.Request.RoleTokens, Scope: scope,
		ExpiresInDays: args.Request.SecretExpiresInDays,
	}
	if args.Request.Name != nil {
		in.Name = *args.Request.Name
	}
	if args.Request.Description != nil {
		in.Description = *args.Request.Description
	}
	a, secret, err := r.getMembersService(ctx).CreateServiceAccount(ctx, in)
	if err != nil {
		return nil, err
	}
	return &ServiceAccountCredentialsResolver{account: a, secret: secret,
		endpoint: r.getIdentityManager(ctx).TokenEndpoint()}, nil
}

// SetServiceAccountAccess replaces an account's roles and scope (requires member:write).
func (r *SchemaResolver) SetServiceAccountAccess(ctx context.Context, args struct {
	Token      string
	RoleTokens []string
	Scope      []scopeAnchorInput
}) (*ServiceAccountResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberWrite); err != nil {
		return nil, err
	}
	if err := checkList("roleTokens", args.RoleTokens); err != nil {
		return nil, err
	}
	scope, err := toScopeInput(args.Scope)
	if err != nil {
		return nil, err
	}
	a, err := r.getMembersService(ctx).SetServiceAccountAccess(ctx, args.Token, args.RoleTokens, scope)
	if err != nil {
		return nil, err
	}
	return accountResolver(a), nil
}

// SetServiceAccountEnabled enables or disables an account (requires member:write).
func (r *SchemaResolver) SetServiceAccountEnabled(ctx context.Context, args struct {
	Token   string
	Enabled bool
}) (*ServiceAccountResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberWrite); err != nil {
		return nil, err
	}
	a, err := r.getMembersService(ctx).SetServiceAccountEnabled(ctx, args.Token, args.Enabled)
	if err != nil {
		return nil, err
	}
	return accountResolver(a), nil
}

// RotateServiceAccountSecret mints a new secret for an account (requires member:write).
func (r *SchemaResolver) RotateServiceAccountSecret(ctx context.Context, args struct {
	Token         string
	OverlapHours  *int32
	ExpiresInDays *int32
}) (*ServiceAccountCredentialsResolver, error) {
	if err := auth.Authorize(ctx, auth.MemberWrite); err != nil {
		return nil, err
	}
	a, secret, err := r.getMembersService(ctx).RotateServiceAccountSecret(ctx, args.Token, args.OverlapHours, args.ExpiresInDays)
	if err != nil {
		return nil, err
	}
	return &ServiceAccountCredentialsResolver{account: a, secret: secret,
		endpoint: r.getIdentityManager(ctx).TokenEndpoint()}, nil
}

// DeleteServiceAccount deletes an account (requires member:write).
func (r *SchemaResolver) DeleteServiceAccount(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := auth.Authorize(ctx, auth.MemberWrite); err != nil {
		return false, err
	}
	return r.getMembersService(ctx).DeleteServiceAccount(ctx, args.Token)
}
