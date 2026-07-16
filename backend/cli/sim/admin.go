// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// Admin drives the instance admin surface (/admin/graphql) as a superuser to mint
// and tear down a scoped per-sim identity + tenant. It is the ONLY thing in the
// sim flow that talks to the admin surface (ADR-035 one-directional rule).
type Admin struct {
	session  *userclient.AdminSession
	adminURL string
}

// NewAdmin builds an Admin authenticating as email/password (a superuser) against
// userGraphQL for login and adminURL for the admin mutations.
func NewAdmin(userGraphQL, adminURL, email, password string) *Admin {
	return &Admin{
		session:  userclient.NewAdminSession(nil, userGraphQL, email, password),
		adminURL: adminURL,
	}
}

// EnsureSuperuser logs in and confirms the admin identity is a superuser, failing
// fast with a clear message rather than letting each mutation reject on authority.
func (a *Admin) EnsureSuperuser(ctx context.Context) error {
	su, err := a.session.Superuser(ctx)
	if err != nil {
		return fmt.Errorf("admin login failed: %w", err)
	}
	if !su {
		return fmt.Errorf("admin identity is not a superuser; minting a sim identity requires tenant:write + user:write")
	}
	return nil
}

// simTenantTier is the tier the sim's tenant is created at (ADR-065). Every tenant
// is packaged at a tier — the field is required — so this names one explicitly
// rather than relying on a default, of which there is none by design. Silver is the
// neutral middle of the seeded vocabulary: a sim tenant is a demo, not a customer,
// and giving it gold would quietly make the sim exercise the most privileged
// packaging rather than the typical one.
//
// The token is a literal, not a shared constant: dcctl talks to the admin API over
// the wire and does not import user-management. An operator who renames or deletes
// the seeded silver tier will see this refuse with an unknown-tier error, which is
// the right failure — loud, and naming the tier.
const simTenantTier = "silver"

const (
	createTenantMutation = `mutation($token:String!,$name:String,$tier:String!){` +
		`createTenant(request:{token:$token,name:$name,tierToken:$tier}){token}}`
	createIdentityMutation = `mutation($email:String!,$password:String!){` +
		`createIdentity(request:{email:$email,password:$password,enabled:true,systemRoles:[]}){email}}`
	addMembershipMutation = `mutation($email:String!,$tenant:String!){` +
		`addMembership(email:$email,tenant:$tenant,roleTokens:["tenant-admin"]){email}}`
	setSystemRolesMutation     = `mutation($email:String!){setSystemRoles(email:$email,roleTokens:[]){email}}`
	setMembershipRolesMutation = `mutation($email:String!,$tenant:String!){` +
		`setMembershipRoles(email:$email,tenant:$tenant,roleTokens:["tenant-admin"]){email}}`
	setPasswordMutation    = `mutation($email:String!,$password:String!){setPassword(email:$email,password:$password){email}}`
	deleteIdentityMutation = `mutation($email:String!){deleteIdentity(email:$email)}`
	deleteTenantMutation   = `mutation($token:String!){deleteTenant(token:$token)}`
)

// CreateTenant creates the sim's tenant. Idempotent: an already-existing tenant is
// tolerated so a re-run of create (ADR-035 idempotent bootstrap) succeeds.
func (a *Admin) CreateTenant(ctx context.Context, token, name string) error {
	err := a.session.Query(ctx, a.adminURL, createTenantMutation,
		map[string]any{"token": token, "name": name, "tier": simTenantTier}, nil)
	return tolerateExists(fmt.Errorf("create tenant %q: %w", token, err), err)
}

// CreateIdentity creates the scoped sim identity with NO system roles (so its
// identity token carries no admin authority). Idempotent on already-exists.
func (a *Admin) CreateIdentity(ctx context.Context, email, password string) error {
	err := a.session.Query(ctx, a.adminURL, createIdentityMutation,
		map[string]any{"email": email, "password": password}, nil)
	return tolerateExists(fmt.Errorf("create identity %q: %w", email, err), err)
}

// AddTenantAdmin binds the sim identity to its tenant with the tenant-admin role
// (authorities ["*"] scoped to that one tenant). Idempotent on already-exists.
func (a *Admin) AddTenantAdmin(ctx context.Context, email, tenant string) error {
	err := a.session.Query(ctx, a.adminURL, addMembershipMutation,
		map[string]any{"email": email, "tenant": tenant}, nil)
	return tolerateExists(fmt.Errorf("add tenant-admin membership for %q in %q: %w", email, tenant, err), err)
}

// SetPassword forces the identity's password to match what dcctl stored, so create
// is idempotent even when the identity pre-existed (CreateIdentity tolerated an
// already-exists without changing the password).
func (a *Admin) SetPassword(ctx context.Context, email, password string) error {
	if err := a.session.Query(ctx, a.adminURL, setPasswordMutation,
		map[string]any{"email": email, "password": password}, nil); err != nil {
		return fmt.Errorf("set password for %q: %w", email, err)
	}
	return nil
}

// ForceNoSystemRoles reconciles the identity to have NO system roles. CreateIdentity
// tolerates an already-existing identity without stripping any admin roles it might
// carry, so this makes the no-admin-power invariant true on re-create rather than
// merely on first create — a security reconcile, not a nicety.
func (a *Admin) ForceNoSystemRoles(ctx context.Context, email string) error {
	if err := a.session.Query(ctx, a.adminURL, setSystemRolesMutation,
		map[string]any{"email": email}, nil); err != nil {
		return fmt.Errorf("clear system roles for %q: %w", email, err)
	}
	return nil
}

// ForceTenantAdmin reconciles the membership's roles to exactly tenant-admin, so a
// re-create repairs a membership whose roles drifted (AddTenantAdmin tolerates an
// existing membership without correcting its roles).
func (a *Admin) ForceTenantAdmin(ctx context.Context, email, tenant string) error {
	if err := a.session.Query(ctx, a.adminURL, setMembershipRolesMutation,
		map[string]any{"email": email, "tenant": tenant}, nil); err != nil {
		return fmt.Errorf("set tenant-admin role for %q in %q: %w", email, tenant, err)
	}
	return nil
}

// DeleteIdentity removes the sim identity and its memberships. Returns whether one
// was removed.
func (a *Admin) DeleteIdentity(ctx context.Context, email string) (bool, error) {
	var out struct {
		DeleteIdentity bool `json:"deleteIdentity"`
	}
	if err := a.session.Query(ctx, a.adminURL, deleteIdentityMutation,
		map[string]any{"email": email}, &out); err != nil {
		return false, fmt.Errorf("delete identity %q: %w", email, err)
	}
	return out.DeleteIdentity, nil
}

// DeleteTenant removes the sim's tenant. It is rejected by the server while any
// membership still references the tenant, so callers must DeleteIdentity first.
func (a *Admin) DeleteTenant(ctx context.Context, token string) (bool, error) {
	var out struct {
		DeleteTenant bool `json:"deleteTenant"`
	}
	if err := a.session.Query(ctx, a.adminURL, deleteTenantMutation,
		map[string]any{"token": token}, &out); err != nil {
		return false, fmt.Errorf("delete tenant %q: %w", token, err)
	}
	return out.DeleteTenant, nil
}

// tolerateExists returns nil when inner reports an already-exists/duplicate
// condition (so create is idempotent), and wrapped otherwise. inner is the raw
// error; wrapped is the contextualized one to return on a real failure. A pre-GA
// pragmatism: the admin schema has no assure-mutations, so idempotency is by
// error-shape until it does.
//
// The match is by POSITIVE phrase — "already exists" / "duplicate" / "unique" — not
// the bare substring "exist", which would also swallow a "does NOT exist" error and
// report a broken create as success.
func tolerateExists(wrapped, inner error) error {
	if inner == nil {
		return nil
	}
	m := strings.ToLower(inner.Error())
	for _, s := range []string{"already exists", "duplicate", "unique"} {
		if strings.Contains(m, s) {
			return nil
		}
	}
	return wrapped
}
