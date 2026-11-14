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

// CheckAdminIdentity protects all of an existing target's current system grants.
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
