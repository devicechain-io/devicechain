// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/conflict"
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

// DefaultTenantTier is the tier a sim tenant is packaged at unless --tier overrides
// it. Silver is the neutral middle of the seeded vocabulary (see the note above):
// a sim is a demo, not a customer, so it should exercise the typical packaging, not
// the most privileged.
const DefaultTenantTier = simTenantTier

const (
	createTenantMutation = `mutation($token:String!,$name:String,$tier:String!,$shedPriority:Int){` +
		`createTenant(request:{token:$token,name:$name,tierToken:$tier,shedPriority:$shedPriority}){token}}`
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

// CreateTenant creates the sim's tenant at the given tier, optionally with an ADR-063
// shed-priority override (nil = inherit the tier's). Idempotent: an already-existing
// tenant is tolerated so a re-run of create (ADR-035 idempotent bootstrap) succeeds —
// but note a re-run does NOT re-tier or re-prioritize an existing tenant, since
// createTenant tolerates-exists rather than updates; a load-test harness that needs a
// specific tier/priority should use a fresh sim name.
func (a *Admin) CreateTenant(ctx context.Context, token, name, tier string, shedPriority *int) error {
	err := a.session.Query(ctx, a.adminURL, createTenantMutation,
		map[string]any{"token": token, "name": name, "tier": tier, "shedPriority": shedPriority}, nil)
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

// tolerateExists returns nil when inner is the server's CONFLICT refusal (so create is
// idempotent), and wrapped otherwise. inner is the raw error; wrapped is the
// contextualized one to return on a real failure. A pre-GA pragmatism: the admin schema
// has no assure-mutations, so idempotency is by the refusal's code until it does.
//
// 🔴 IT READS THE SERVER'S MACHINE-READABLE CODE, NEVER THE MESSAGE. An earlier version
// matched phrases ("already exists", "duplicate", "unique") in the text, and prose made
// a poor vote: the membership refusal used none of the phrases, so a re-run stopped at
// that step, and the server echoes the caller's own identifiers into its messages, so a
// sim's NAME could decide whether a failure counted as success. The text, including any
// identifiers echoed in it, no longer has a say.
//
// WHY CONFLICT IS SOUND TO TOLERATE HERE, AND ONLY HERE. CONFLICT means "a value that
// must be unique is already in use", which does not by itself mean "the record you asked
// for exists". It does for exactly the three mutations this wraps: createTenant,
// createIdentity and addMembership can only collide on a value the CALLER chose — the
// tenant token, the identity's email, the (identity, tenant) pair. A deleted tenant's
// reserved token is refused WITHOUT the code (user-management pins that), because
// carrying on there would mint access to a tenant nobody can enter.
//
// EVERY error in the response must carry the code (userclient.AllHaveCode): one
// CONFLICT beside an uncoded real failure is not a benign refusal.
func tolerateExists(wrapped, inner error) error {
	if inner == nil || userclient.AllHaveCode(inner, conflict.Code) {
		return nil
	}
	return wrapped
}
