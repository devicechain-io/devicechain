// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"errors"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
	"gorm.io/gorm"
)

// adminHeld reads current system-role authorities, never the token's cached grants.
// These checks belong at the served resolver boundary: internal service callers may
// intentionally operate without claims (for example during bootstrap).
func (s *Service) adminHeld(ctx context.Context) (authSet, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok || claims.Username == "" {
		return authSet{}, auth.ErrForbidden
	}
	caller, err := s.iam.IdentityByEmail(ctx, claims.Username)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return authSet{}, auth.ErrForbidden
	}
	if err != nil {
		return authSet{}, err
	}
	var lists [][]string
	for _, role := range caller.SystemRoles {
		lists = append(lists, role.Authorities)
	}
	return newAuthSet(lists...), nil
}

// CheckAdminAuthorities requires the caller to cover every proposed authority.
func (s *Service) CheckAdminAuthorities(ctx context.Context, authorities []string) error {
	held, err := s.adminHeld(ctx)
	if err != nil {
		return err
	}
	return checkAdminGrant(held, authorities)
}

func checkAdminGrant(held authSet, authorities []string) error {
	if missing := held.missingFrom(authorities); missing != "" {
		return refuse(CodeEscalation, "you cannot grant a role granting %q, which you do not hold", missing)
	}
	return nil
}

// CheckAdminSystemRoles resolves tokens using the same catalogue lookup as the write.
func (s *Service) CheckAdminSystemRoles(ctx context.Context, tokens []string) error {
	held, err := s.adminHeld(ctx)
	if err != nil {
		return err
	}
	roles, err := s.iam.CatalogueRolesByScopeTokens(ctx, iam.ScopeSystem, tokens)
	if err != nil {
		return err
	}
	for _, role := range roles {
		if err := checkAdminGrant(held, role.Authorities); err != nil {
			return err
		}
	}
	return nil
}

// CheckAdminIdentity protects all of an existing target's system and tenant grants.
// Missing targets fall through to each operation's existing not-found behavior.
func (s *Service) CheckAdminIdentity(ctx context.Context, email string) error {
	held, err := s.adminHeld(ctx)
	if err != nil {
		return err
	}
	target, err := s.iam.IdentityForAdmin(ctx, email)
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, iam.ErrEmailInvalid) {
		return nil
	}
	if err != nil {
		return err
	}
	return checkAdminIdentity(held, target)
}

func checkAdminIdentity(held authSet, target *iam.Identity) error {
	for _, role := range target.SystemRoles {
		if missing := held.missingFrom(role.Authorities); missing != "" {
			return refuse(CodeEscalation, "you cannot change %s, who holds %q, which you do not hold", target.Email, missing)
		}
	}
	// IdentityForAdmin and IdentityByEmail preload Memberships.TenantRoles.
	for _, mem := range target.Memberships {
		for _, role := range mem.TenantRoles {
			if missing := held.missingFrom(role.Authorities); missing != "" {
				return refuse(CodeEscalation, "you cannot change %s, whose membership in %s grants %q, which you do not hold", target.Email, mem.TenantId, missing)
			}
		}
	}
	return nil
}

// CheckAdminRole protects the current catalogue role and the resulting authority set.
// A nil request denotes deletion; an omitted authorities field preserves the old set.
func (s *Service) CheckAdminRole(ctx context.Context, scope, token string, request *RoleUpdateRequest) error {
	held, err := s.adminHeld(ctx)
	if err != nil {
		return err
	}
	rs, err := parseScope(scope)
	if err != nil {
		return err
	}
	role, err := s.iam.CatalogueRoleByScopeToken(ctx, rs, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if missing := held.missingFrom(role.Authorities); missing != "" {
		return refuse(CodeEscalation, "you cannot change a role granting %q, which you do not hold", missing)
	}
	if request != nil {
		return checkAdminGrant(held, request.Authorities.ApplyTo(role.Authorities))
	}
	return nil
}

// CheckAdminIdentityRecord protects an identity the caller already resolved, so the check
// judges the very record the write uses (an SSO link approval resolves its identity
// itself, from a request whose email can change between two reads).
func (s *Service) CheckAdminIdentityRecord(ctx context.Context, target *iam.Identity) error {
	held, err := s.adminHeld(ctx)
	if err != nil {
		return err
	}
	return checkAdminIdentity(held, target)
}

// CheckAdminSystemRoleChange preserves the target lookup's precedence over grants.
func (s *Service) CheckAdminSystemRoleChange(ctx context.Context, email string, tokens []string) error {
	held, err := s.adminHeld(ctx)
	if err != nil {
		return err
	}
	target, err := s.iam.IdentityForAdmin(ctx, email)
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, iam.ErrEmailInvalid) {
		return nil
	}
	if err != nil {
		return err
	}
	roles, err := s.iam.CatalogueRolesByScopeTokens(ctx, iam.ScopeSystem, tokens)
	if err != nil {
		return err
	}
	if err := checkAdminIdentity(held, target); err != nil {
		return err
	}
	for _, role := range roles {
		if err := checkAdminGrant(held, role.Authorities); err != nil {
			return err
		}
	}
	return nil
}

// CheckAdminTenantRoleGrant uses the same catalogue and tenant-owned role lookup as
// membership writes. Unknown tokens are left to the write's existing error path.
func (s *Service) CheckAdminTenantRoleGrant(ctx context.Context, tenant string, tokens []string) error {
	held, err := s.adminHeld(ctx)
	if err != nil {
		return err
	}
	return s.checkAdminTenantRoleGrant(ctx, held, tenant, tokens)
}

func (s *Service) checkAdminTenantRoleGrant(ctx context.Context, held authSet, tenant string, tokens []string) error {
	roles, err := s.iam.AssignableRolesByTokens(ctx, tenant, tokens)
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, iam.ErrAmbiguousRole) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, role := range roles {
		if err := checkAdminGrant(held, role.Authorities); err != nil {
			return err
		}
	}
	return nil
}

// CheckAdminMembershipAddition preserves identity, tenant and duplicate lookup
// precedence before judging the proposed roles.
func (s *Service) CheckAdminMembershipAddition(ctx context.Context, email, tenant string, tokens []string) error {
	held, err := s.adminHeld(ctx)
	if err != nil {
		return err
	}
	id, err := s.loadIdentity(ctx, email)
	if err != nil {
		return err
	}
	t, err := s.iam.TenantByToken(ctx, tenant)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if t.PurgeState.Deleted() {
		return nil
	}
	if _, err := s.iam.MembershipByIdentityTenant(ctx, id.ID, tenant); err == nil {
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.checkAdminTenantRoleGrant(ctx, held, tenant, tokens)
}

// CheckAdminMembership judges only the membership, never identity system roles.
func (s *Service) CheckAdminMembership(ctx context.Context, email, tenant string) error {
	held, err := s.adminHeld(ctx)
	if err != nil {
		return err
	}
	id, mem, err := s.loadMembership(ctx, email, tenant)
	if err != nil {
		return err
	}
	return checkAdminMembership(held, id.Email, tenant, mem)
}

func checkAdminMembership(held authSet, email, tenant string, mem *iam.Membership) error {
	for _, role := range mem.TenantRoles {
		if missing := held.missingFrom(role.Authorities); missing != "" {
			return refuse(CodeEscalation, "you cannot change %s's membership in %s, which grants %q, which you do not hold", email, tenant, missing)
		}
	}
	return nil
}

// CheckAdminMembershipRoleChange preserves target and unknown-role errors before
// checking both the current membership and its proposed grants.
func (s *Service) CheckAdminMembershipRoleChange(ctx context.Context, email, tenant string, tokens []string) error {
	held, err := s.adminHeld(ctx)
	if err != nil {
		return err
	}
	id, mem, err := s.loadMembership(ctx, email, tenant)
	if err != nil {
		return err
	}
	roles, err := s.iam.AssignableRolesByTokens(ctx, tenant, tokens)
	if err != nil {
		return err
	}
	if err := checkAdminMembership(held, id.Email, tenant, mem); err != nil {
		return err
	}
	for _, role := range roles {
		if err := checkAdminGrant(held, role.Authorities); err != nil {
			return err
		}
	}
	return nil
}

// CheckAdminIdpRoleMappings protects the proposed catalogue grants and all mappings
// replaced for this tenant. Only administrators can change these records.
func (s *Service) CheckAdminIdpRoleMappings(ctx context.Context, connectionToken, tenant string, tokens []string) error {
	held, err := s.adminHeld(ctx)
	if err != nil {
		return err
	}
	conn, err := s.iam.IdpConnectionByToken(ctx, connectionToken)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if conn.Kind != iam.IdpKindTenant {
		return nil
	}
	tenants, err := s.iam.ConnectionTenants(ctx, conn.ID)
	if err != nil {
		return err
	}
	served := false
	for _, token := range tenants {
		served = served || token == tenant
	}
	if !served {
		return nil // Preserve the write's tenant-not-served error.
	}
	roles, err := s.iam.CatalogueRolesByScopeTokens(ctx, iam.ScopeTenant, tokens)
	if err != nil {
		var unknown *iam.UnknownCatalogueRoleError
		if errors.As(err, &unknown) {
			return nil // Preserve the manager's unknown-token validation wrapper.
		}
		return err
	}
	mappings, err := s.iam.ListIdpRoleMappings(ctx, conn.ID)
	if err != nil {
		return err
	}
	for _, mapping := range mappings {
		if mapping.Mapping.TenantId == tenant {
			roles = append(roles, mapping.Role)
		}
	}
	for _, role := range roles {
		if err := checkAdminGrant(held, role.Authorities); err != nil {
			return err
		}
	}
	return nil
}

// CheckAdminIdpJit protects both the proposed and the current default roles.
func (s *Service) CheckAdminIdpJit(ctx context.Context, connectionToken string, tokens []string) error {
	held, err := s.adminHeld(ctx)
	if err != nil {
		return err
	}
	conn, err := s.iam.IdpConnectionByToken(ctx, connectionToken)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if conn.Kind != iam.IdpKindTenant {
		return nil // The manager rejects provisioning or grants on instance connections.
	}
	roles, err := s.iam.CatalogueRolesByScopeTokens(ctx, iam.ScopeTenant, tokens)
	if err != nil {
		var unknown *iam.UnknownCatalogueRoleError
		if errors.As(err, &unknown) {
			return nil // Preserve the manager's unknown-token validation wrapper.
		}
		return err
	}
	current, err := s.iam.CatalogueRolesByScopeTokens(ctx, iam.ScopeTenant, conn.DefaultTenantRoles)
	if err != nil {
		return err
	}
	for _, role := range append(roles, current...) {
		if err := checkAdminGrant(held, role.Authorities); err != nil {
			return err
		}
	}
	return nil
}

// CheckAdminConnectionGrants protects every grant of a tenant connection. A missing
// connection falls through to the write's existing not-found behavior.
func (s *Service) CheckAdminConnectionGrants(ctx context.Context, token string) error {
	conn, err := s.iam.IdpConnectionByToken(ctx, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.CheckAdminConnectionRecord(ctx, conn)
}

// CheckAdminConnectionRecord judges the connection resolved by the write.
func (s *Service) CheckAdminConnectionRecord(ctx context.Context, conn *iam.IdpConnection) error {
	if conn.Kind != iam.IdpKindTenant {
		return nil
	}
	held, err := s.adminHeld(ctx)
	if err != nil {
		return err
	}
	return s.checkConnectionGrants(ctx, held, conn)
}

func (s *Service) checkConnectionGrants(ctx context.Context, held authSet, conn *iam.IdpConnection) error {
	roles, err := s.iam.CatalogueRolesByScopeTokens(ctx, iam.ScopeTenant, conn.DefaultTenantRoles)
	if err != nil {
		return err
	}
	mappings, err := s.iam.ListIdpRoleMappings(ctx, conn.ID)
	if err != nil {
		return err
	}
	for _, mapping := range mappings {
		roles = append(roles, mapping.Role)
	}
	for _, role := range roles {
		if err := checkAdminGrant(held, role.Authorities); err != nil {
			return err
		}
	}
	return nil
}
