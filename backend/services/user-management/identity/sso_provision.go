// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Just-in-time provisioning (rule C.4) and the recompute of managed memberships (rule E).
//
// Both belong to a TENANT connection only. An instance connection never creates an
// identity and never touches a membership: it reaches only identities a superuser linked on
// purpose, and its sessions enter a tenant only through the superuser's own break-glass.
//
// Two lines are drawn here and each has a test:
//
//   - JIT creates an identity only when NO identity has that address. An address that
//     already has one is a collision and becomes a link request for a superuser (the same
//     as when JIT is off), never an automatic link.
//   - The recompute writes only memberships this connection manages. A membership with no
//     manager is an administrator's, and one another connection manages is that
//     connection's; neither is read as a mismatch to correct.

// SsoOutcomeRefusedGroupsOverage is the outcome when the provider says a person's groups
// were too many to carry in the token (Microsoft Entra's "groups overage"). The token holds
// no usable group list then, and acting on it would turn "too many groups to list" into
// "no groups" and disable every membership the person's groups grant. The sign-in is
// refused so an administrator hears about it; the remedy is at the provider (filter the
// groups the application receives).
const SsoOutcomeRefusedGroupsOverage = "refused_groups_overage"

// SsoOutcomeRefusedGroupsClaim is the outcome when the groups claim is present but not a
// list of strings (or one string): nothing can be said about what it means, so nothing is
// granted from it.
const SsoOutcomeRefusedGroupsClaim = "refused_groups_claim"

// maxSsoGroups bounds how many group values one sign-in is matched against. A person in
// more groups than this is far outside what an ID token carries.
const maxSsoGroups = 1000

// ssoGroups reads the sign-in's group values from the connection's groups claim. A missing
// claim is no groups. When the connection maps groups, a token that reports overage is
// refused (see SsoOutcomeRefusedGroupsOverage). It returns a refusal outcome or the groups.
func ssoGroups(conn *iam.IdpConnection, c *ssoClaims, mapsGroups bool) (map[string]struct{}, string) {
	name := conn.GroupsClaim
	if name == "" {
		return nil, ""
	}
	if mapsGroups && groupsOverage(c.Raw, name) {
		return nil, SsoOutcomeRefusedGroupsOverage
	}
	raw, present := c.Raw[name]
	if !present || raw == nil {
		return nil, ""
	}
	groups := map[string]struct{}{}
	switch v := raw.(type) {
	case string:
		groups[v] = struct{}{}
	case []any:
		if len(v) > maxSsoGroups {
			return nil, SsoOutcomeRefusedGroupsClaim
		}
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, SsoOutcomeRefusedGroupsClaim
			}
			groups[s] = struct{}{}
		}
	default:
		return nil, SsoOutcomeRefusedGroupsClaim
	}
	return groups, ""
}

// groupsOverage reports Microsoft Entra's overage markers: hasgroups (JWT), or a
// _claim_names entry pointing the groups claim at a Graph endpoint instead of carrying it.
func groupsOverage(raw map[string]any, claim string) bool {
	if claimIsTrue(raw["hasgroups"]) {
		return true
	}
	names, _ := raw["_claim_names"].(map[string]any)
	if _, ok := names["groups"]; ok {
		return true
	}
	if _, ok := names[claim]; ok {
		return true
	}
	return false
}

// provisionIdentity is rule C.4: no link and no identity with the address, on a tenant
// connection with JIT enabled. It creates the identity (empty password, which no login can
// match) and its link together.
func (m *Manager) provisionIdentity(ctx context.Context, conn *iam.IdpConnection, c *ssoClaims) (*iam.Identity, error) {
	id := &iam.Identity{Email: c.Email, Enabled: true}
	if v, ok := c.Raw["given_name"].(string); ok && v != "" && len(v) <= 128 {
		id.FirstName = sql.NullString{String: v, Valid: true}
	}
	if v, ok := c.Raw["family_name"].(string); ok && v != "" && len(v) <= 128 {
		id.LastName = sql.NullString{String: v, Valid: true}
	}
	err := m.iam.CreateProvisionedIdentity(ctx, id, &iam.IdentityLink{
		ConnectionID: conn.ID, Subject: c.Subject, CreatedBy: "sso:" + conn.Token,
	})
	if err != nil {
		return nil, err
	}
	m.recordAuth(ctx, rdb.AuditOpIdentityProvisioned, id.Email, "")
	return id, nil
}

// syncSsoMemberships is rule E: for every tenant the connection serves, bring the managed
// membership to default_tenant_roles plus the roles of every mapping whose group the person
// holds. An empty result disables it; nothing is deleted.
func (m *Manager) syncSsoMemberships(ctx context.Context, conn *iam.IdpConnection, id *iam.Identity,
	groups map[string]struct{}, mappings []iam.IdpRoleMappingView) error {

	defaults, err := m.iam.CatalogueRolesByScopeTokens(ctx, iam.ScopeTenant, conn.DefaultTenantRoles)
	if err != nil {
		return fmt.Errorf("the connection's default roles: %w", err)
	}
	tenants, err := m.iam.ConnectionTenants(ctx, conn.ID)
	if err != nil {
		return err
	}
	for _, tenant := range tenants {
		t, err := m.iam.TenantByToken(ctx, tenant)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return err
		}
		if t.PurgeState.Deleted() {
			continue
		}
		roles := map[uint]iam.Role{}
		for _, r := range defaults {
			roles[r.ID] = r
		}
		for _, mv := range mappings {
			if mv.Mapping.TenantId != tenant {
				continue
			}
			if _, held := groups[mv.Mapping.GroupValue]; !held {
				continue
			}
			roles[mv.Role.ID] = mv.Role
		}
		want := make([]iam.Role, 0, len(roles))
		for _, r := range roles {
			// A mapping can only name a tenant role (the administrator call enforces it); this
			// is the second lock, so a row written any other way still cannot grant a system
			// role from a claim.
			if r.Scope != iam.ScopeTenant {
				return fmt.Errorf("role %q is not a tenant role", r.Token)
			}
			want = append(want, r)
		}
		sort.Slice(want, func(i, j int) bool { return want[i].ID < want[j].ID })
		if _, err := m.iam.SyncManagedMembership(ctx, id.ID, tenant, conn.ID, want); err != nil {
			return err
		}
	}
	return nil
}

// grantsFullAuthority reports whether a role carries the super-authority. Within a tenant
// that is every tenant-tier authority, so mapping a group to it is giving the group the
// tenant.
func grantsFullAuthority(r iam.Role) bool {
	for _, a := range r.Authorities {
		if a == string(auth.AuthorityAll) {
			return true
		}
	}
	return false
}

// ---- administration ---------------------------------------------------------------------

// ErrFullAuthorityNotAcknowledged refuses granting a full-authority role from a claim
// without the administrator saying so.
var ErrFullAuthorityNotAcknowledged = errors.New(
	"a role with full authority in the tenant would be granted from a provider claim; pass acknowledgeFullAuthority: true to confirm")

// RoleMappingInput is one group-to-role mapping to set.
type RoleMappingInput struct {
	Group string
	Role  string
}

const (
	maxMappingsPerTenant = 256
	maxDefaultRoles      = 32
)

// IdpRoleMappingDetail is a mapping as reported to an administrator.
type IdpRoleMappingDetail struct {
	Tenant string
	Group  string
	Role   string
	// FullAuthority is whether the role carries the tenant's super-authority.
	FullAuthority bool
}

func (m *Manager) roleMappingDetails(ctx context.Context, connID uint) ([]IdpRoleMappingDetail, error) {
	views, err := m.iam.ListIdpRoleMappings(ctx, connID)
	if err != nil {
		return nil, err
	}
	out := make([]IdpRoleMappingDetail, 0, len(views))
	for _, v := range views {
		out = append(out, IdpRoleMappingDetail{
			Tenant: v.Mapping.TenantId, Group: v.Mapping.GroupValue, Role: v.Role.Token,
			FullAuthority: grantsFullAuthority(v.Role),
		})
	}
	return out, nil
}

// SetIdpRoleMappings replaces a tenant connection's group-to-role mappings for one tenant it
// serves. Roles are tenant roles only: a system role token resolves to nothing in the tenant
// scope and is refused, so no claim can grant one. A role carrying the super-authority needs
// acknowledgeFullAuthority. The change takes effect at each person's next sign-in.
func (m *Manager) SetIdpRoleMappings(ctx context.Context, token, tenant string, mappings []RoleMappingInput, acknowledgeFullAuthority bool) (*IdpConnectionDetail, error) {
	conn, err := m.loadConnection(ctx, token)
	if err != nil {
		return nil, err
	}
	if conn.Kind != iam.IdpKindTenant {
		return nil, invalid("an instance connection never maps groups to roles")
	}
	if len(mappings) > maxMappingsPerTenant {
		return nil, invalid("at most %d mappings per tenant", maxMappingsPerTenant)
	}
	tokens := make([]string, 0, len(mappings))
	for _, mp := range mappings {
		if strings.TrimSpace(mp.Group) == "" || len(mp.Group) > 512 {
			return nil, invalid("a group value of up to 512 characters is required")
		}
		tokens = append(tokens, mp.Role)
	}
	roles, err := m.iam.CatalogueRolesByScopeTokens(ctx, iam.ScopeTenant, tokens)
	if err != nil {
		return nil, invalid("%v", err)
	}
	byToken := make(map[string]iam.Role, len(roles))
	for _, r := range roles {
		byToken[r.Token] = r
	}
	rows := make([]iam.IdpRoleMapping, 0, len(mappings))
	seen := map[string]struct{}{}
	for _, mp := range mappings {
		role := byToken[mp.Role]
		if grantsFullAuthority(role) && !acknowledgeFullAuthority {
			return nil, ErrFullAuthorityNotAcknowledged
		}
		key := fmt.Sprintf("%s\x00%d", mp.Group, role.ID)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		rows = append(rows, iam.IdpRoleMapping{GroupValue: mp.Group, RoleID: role.ID})
	}
	if err := m.iam.ReplaceIdpRoleMappings(ctx, conn.ID, tenant, rows); err != nil {
		if errors.Is(err, iam.ErrTenantNotServed) {
			return nil, invalid("%v", err)
		}
		return nil, err
	}
	m.recordAuth(ctx, rdb.AuditOpRoleMappingChanged, actor(ctx), tenant)
	return m.IdpConnection(ctx, token)
}

// SetIdpJit sets whether a tenant connection creates identities for people it has not seen
// (when no identity has their address), and the tenant roles every person who signs in
// through it holds in each tenant it serves. An instance connection takes neither. The
// default roles are applied at each sign-in, so a change reaches people at their next one.
func (m *Manager) SetIdpJit(ctx context.Context, token string, enabled bool, defaultTenantRoles []string, acknowledgeFullAuthority bool) (*IdpConnectionDetail, error) {
	conn, err := m.loadConnection(ctx, token)
	if err != nil {
		return nil, err
	}
	if conn.Kind != iam.IdpKindTenant && (enabled || len(defaultTenantRoles) > 0) {
		return nil, invalid("an instance connection never provisions identities or grants roles")
	}
	if len(defaultTenantRoles) > maxDefaultRoles {
		return nil, invalid("at most %d default roles", maxDefaultRoles)
	}
	roles, err := m.iam.CatalogueRolesByScopeTokens(ctx, iam.ScopeTenant, defaultTenantRoles)
	if err != nil {
		return nil, invalid("%v", err)
	}
	tokens := make([]string, 0, len(roles))
	for _, r := range roles {
		if grantsFullAuthority(r) && !acknowledgeFullAuthority {
			return nil, ErrFullAuthorityNotAcknowledged
		}
		tokens = append(tokens, r.Token)
	}
	sort.Strings(tokens)
	conn.JitEnabled, conn.DefaultTenantRoles = enabled, tokens
	if err := m.iam.UpdateIdpConnection(ctx, conn); err != nil {
		return nil, err
	}
	m.recordAuth(ctx, rdb.AuditOpRoleMappingChanged, actor(ctx), "")
	log.Info().Str("connection", conn.Token).Bool("jit", enabled).Msg("Provisioning settings changed.")
	return m.IdpConnection(ctx, token)
}
