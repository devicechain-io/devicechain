// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Admin control-plane errors. Sentinels so the resolver layer (or a future error
// mapper) can distinguish them without string matching.
var (
	ErrIdentityNotFound   = rdb.NewNotFound("identity not found")
	ErrMembershipNotFound = rdb.NewNotFound("membership not found")
	ErrTenantNotFound     = rdb.NewNotFound("tenant not found")
	// ErrAlreadyMember is a uniqueness conflict (one membership per identity and
	// tenant), so it carries extensions.code CONFLICT. dcctl's sim flow tolerates that
	// code on a re-run, which is what makes adding the membership idempotent.
	ErrAlreadyMember error = conflict.New("identity already has a membership in this tenant")
	// ErrMembershipOrphaned refuses re-enabling a membership that an identity provider
	// connection managed and that was disabled when the connection was deleted or stopped
	// serving the tenant. Nothing would ever revoke it again, so enabling it would turn a
	// grant the provider made into a permanent manual one. Remove it and add a membership on
	// purpose instead.
	ErrMembershipOrphaned = errors.New("this membership was managed by an identity provider connection that no longer exists or no longer serves this tenant; remove it and add a new membership instead of enabling it")
)

// CreateIdentityInput is the data to create a global identity (ADR-033). Password
// is hashed here; SystemRoles are role tokens resolved in the system scope.
type CreateIdentityInput struct {
	Email    string
	Password string
	// FirstName / LastName are optional display text: trimmed, and nil or blank stores
	// NULL, the same rule every nullable name on the platform follows.
	FirstName   *string
	LastName    *string
	Enabled     bool
	SystemRoles []string
}

// CreateIdentity creates a global identity with the given system roles, hashing
// the password. The email is normalized; the unique constraint rejects a
// duplicate. Returns the freshly-loaded identity.
func (s *Service) CreateIdentity(ctx context.Context, in CreateIdentityInput) (*iam.Identity, error) {
	if strings.TrimSpace(in.Email) == "" {
		return nil, fmt.Errorf("email is required")
	}
	email, err := iam.CanonicalEmail(in.Email)
	if err != nil {
		return nil, err
	}
	if in.Password == "" {
		return nil, fmt.Errorf("password is required")
	}
	roles, err := s.iam.CatalogueRolesByScopeTokens(ctx, iam.ScopeSystem, in.SystemRoles)
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	id := &iam.Identity{
		Email: email, FirstName: rdb.NullStrOf(in.FirstName), LastName: rdb.NullStrOf(in.LastName),
		Enabled: in.Enabled, PasswordHash: string(hash), SystemRoles: roles,
	}
	if err := s.iam.CreateIdentity(ctx, id); err != nil {
		return nil, err
	}
	return s.loadIdentity(ctx, email)
}

// SetIdentityEnabled enables or disables an identity (a disabled identity cannot
// authenticate).
func (s *Service) SetIdentityEnabled(ctx context.Context, email string, enabled bool) (*iam.Identity, error) {
	id, err := s.loadIdentity(ctx, email)
	if err != nil {
		return nil, err
	}
	if err := s.iam.SetIdentityEnabled(ctx, id, enabled); err != nil {
		return nil, err
	}
	return s.loadIdentity(ctx, email)
}

// SetSystemRoles replaces an identity's system-role assignments.
func (s *Service) SetSystemRoles(ctx context.Context, email string, roleTokens []string) (*iam.Identity, error) {
	id, err := s.loadIdentity(ctx, email)
	if err != nil {
		return nil, err
	}
	roles, err := s.iam.CatalogueRolesByScopeTokens(ctx, iam.ScopeSystem, roleTokens)
	if err != nil {
		return nil, err
	}
	if err := s.iam.ReplaceSystemRoles(ctx, id, roles); err != nil {
		return nil, err
	}
	return s.loadIdentity(ctx, email)
}

// SetPassword replaces an identity's password (hashed here).
func (s *Service) SetPassword(ctx context.Context, email, password string) (*iam.Identity, error) {
	if password == "" {
		return nil, fmt.Errorf("password is required")
	}
	id, err := s.loadIdentity(ctx, email)
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	if err := s.iam.SetPasswordHash(ctx, id, string(hash)); err != nil {
		return nil, err
	}
	return s.loadIdentity(ctx, email)
}

// DeleteIdentity removes an identity and its dependent rows. Idempotent: a missing
// identity returns (false, nil).
func (s *Service) DeleteIdentity(ctx context.Context, email string) (bool, error) {
	// IdentityForAdmin, so an identity whose stored email is not canonical can be removed.
	id, err := s.iam.IdentityForAdmin(ctx, email)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.iam.DeleteIdentity(ctx, s.secrets, id); err != nil {
		return false, err
	}
	return true, nil
}

// AddMembership binds an identity to an (existing) tenant with the given tenant
// roles and no scope. Rejects an unknown tenant and a duplicate membership.
func (s *Service) AddMembership(ctx context.Context, email, tenant string, roleTokens []string) (*iam.Identity, error) {
	return s.AddScopedMembership(ctx, email, tenant, roleTokens, nil)
}

// AddScopedMembership binds an identity to an (existing) tenant with the given tenant
// roles and scope (ADR-081; an empty scope is the whole tenant). The roles may be the
// catalogue's tenant-scope roles or the tenant's own. Rejects an unknown tenant and a
// duplicate membership.
func (s *Service) AddScopedMembership(ctx context.Context, email, tenant string, roleTokens []string, anchors []ScopeAnchorInput) (*iam.Identity, error) {
	scope, err := NormalizeScope(anchors)
	if err != nil {
		return nil, err
	}
	id, err := s.loadIdentity(ctx, email)
	if err != nil {
		return nil, err
	}
	t, err := s.iam.TenantByToken(ctx, tenant)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTenantNotFound
		}
		return nil, err
	}
	// A deleted tenant takes no new members (ADR-077). Its row survives only to reserve
	// the token, and the grant path refuses everyone, so a membership added here could
	// never be used: the identity would see the tenant in its menu and be told "invalid
	// credentials" on every attempt to enter, indistinguishable from a bad password.
	// It would also un-do DeleteTenant's zero-membership precondition after the fact.
	if t.PurgeState.Deleted() {
		return nil, ErrTenantDeleted
	}
	if _, err := s.iam.MembershipByIdentityTenant(ctx, id.ID, tenant); err == nil {
		return nil, ErrAlreadyMember
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	roles, err := s.iam.AssignableRolesByTokens(ctx, tenant, roleTokens)
	if err != nil {
		return nil, err
	}
	if _, err := s.iam.AddScopedMembership(ctx, id.ID, tenant, roles, scope, true); err != nil {
		return nil, err
	}
	return s.loadIdentity(ctx, email)
}

// SetMembershipScope replaces a membership's scope (ADR-081). An empty scope is the whole
// tenant.
//
// A change here (narrowing included, and the same holds for the role, enable and remove
// operations below) takes effect at the person's next token exchange, not by ending their
// session (ADR-081 decision 8, as amended 2026-10-16). Every exchange (refresh,
// selectTenant, the OAuth code and refresh grants) re-reads the membership and mints from
// it, so the next access token carries the new grant; one already issued runs to its expiry
// (at most the access lifetime, shorter for a scoped member). The session epoch is per
// IDENTITY, so rotating it here would sign the person out of every tenant they belong to,
// and would let one tenant's administrator do that to them at will.
func (s *Service) SetMembershipScope(ctx context.Context, email, tenant string, anchors []ScopeAnchorInput) (*iam.Identity, error) {
	scope, err := NormalizeScope(anchors)
	if err != nil {
		return nil, err
	}
	id, mem, err := s.loadMembership(ctx, email, tenant)
	if err != nil {
		return nil, err
	}
	if err := s.iam.SetMembershipScope(ctx, mem, scope); err != nil {
		return nil, err
	}
	return s.loadIdentity(ctx, id.Email)
}

// SetMembershipRoles replaces a membership's tenant-role assignments. It takes effect at
// the next token exchange (see SetMembershipScope).
func (s *Service) SetMembershipRoles(ctx context.Context, email, tenant string, roleTokens []string) (*iam.Identity, error) {
	id, mem, err := s.loadMembership(ctx, email, tenant)
	if err != nil {
		return nil, err
	}
	roles, err := s.iam.AssignableRolesByTokens(ctx, tenant, roleTokens)
	if err != nil {
		return nil, err
	}
	if err := s.iam.ReplaceMembershipRoles(ctx, mem, roles); err != nil {
		return nil, err
	}
	return s.loadIdentity(ctx, id.Email)
}

// SetMembershipEnabled enables or disables a membership (a disabled membership
// cannot be selected for a tenant token).
func (s *Service) SetMembershipEnabled(ctx context.Context, email, tenant string, enabled bool) (*iam.Identity, error) {
	id, mem, err := s.loadMembership(ctx, email, tenant)
	if err != nil {
		return nil, err
	}
	if enabled && mem.ManagedBy != nil {
		if _, err := s.iam.IdpConnectionByID(ctx, *mem.ManagedBy); errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrMembershipOrphaned
		} else if err != nil {
			return nil, err
		}
		// A connection that no longer serves the tenant never recomputes the membership
		// either, so enabling it would be the same permanent grant.
		served, err := s.iam.ConnectionTenants(ctx, *mem.ManagedBy)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(served, mem.TenantId) {
			return nil, ErrMembershipOrphaned
		}
	}
	if err := s.iam.SetMembershipEnabled(ctx, mem, enabled); err != nil {
		return nil, err
	}
	return s.loadIdentity(ctx, id.Email)
}

// RemoveMembership deletes an identity's membership in a tenant. The person's next token
// exchange for the tenant is refused (see SetMembershipScope); their sessions in other
// tenants are untouched.
func (s *Service) RemoveMembership(ctx context.Context, email, tenant string) (*iam.Identity, error) {
	id, mem, err := s.loadMembership(ctx, email, tenant)
	if err != nil {
		return nil, err
	}
	if err := s.iam.RemoveMembership(ctx, mem); err != nil {
		return nil, err
	}
	return s.loadIdentity(ctx, id.Email)
}

// loadIdentity resolves the identity an administrator names (iam.Store.IdentityForAdmin:
// canonical form first, then the stored spelling, so an identity whose stored email is not
// canonical can still be managed), mapping not-found to ErrIdentityNotFound.
func (s *Service) loadIdentity(ctx context.Context, email string) (*iam.Identity, error) {
	id, err := s.iam.IdentityForAdmin(ctx, email)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrIdentityNotFound
	}
	return id, err
}

// loadMembership resolves an identity and its membership in a tenant, mapping
// not-found to ErrIdentityNotFound / ErrMembershipNotFound.
func (s *Service) loadMembership(ctx context.Context, email, tenant string) (*iam.Identity, *iam.Membership, error) {
	id, err := s.loadIdentity(ctx, email)
	if err != nil {
		return nil, nil, err
	}
	mem, err := s.iam.MembershipByIdentityTenant(ctx, id.ID, tenant)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrMembershipNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	return id, mem, nil
}
