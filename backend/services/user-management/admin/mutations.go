// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-user-management/iam"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Admin control-plane errors. Sentinels so the resolver layer (or a future error
// mapper) can distinguish them without string matching.
var (
	ErrIdentityNotFound   = errors.New("identity not found")
	ErrMembershipNotFound = errors.New("membership not found")
	ErrTenantNotFound     = errors.New("tenant not found")
	ErrAlreadyMember      = errors.New("identity already has a membership in this tenant")
)

// CreateIdentityInput is the data to create a global identity (ADR-033). Password
// is hashed here; SystemRoles are role tokens resolved in the system scope.
type CreateIdentityInput struct {
	Email       string
	Password    string
	FirstName   string
	LastName    string
	Enabled     bool
	SystemRoles []string
}

// CreateIdentity creates a global identity with the given system roles, hashing
// the password. The email is normalized; the unique constraint rejects a
// duplicate. Returns the freshly-loaded identity.
func (s *Service) CreateIdentity(ctx context.Context, in CreateIdentityInput) (*iam.Identity, error) {
	email := iam.NormalizeEmail(in.Email)
	if email == "" {
		return nil, fmt.Errorf("email is required")
	}
	if in.Password == "" {
		return nil, fmt.Errorf("password is required")
	}
	roles, err := s.iam.RolesByScopeTokens(ctx, iam.ScopeSystem, in.SystemRoles)
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	id := &iam.Identity{
		Email: email, FirstName: in.FirstName, LastName: in.LastName,
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
	roles, err := s.iam.RolesByScopeTokens(ctx, iam.ScopeSystem, roleTokens)
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
	id, err := s.iam.IdentityByEmail(ctx, iam.NormalizeEmail(email))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.iam.DeleteIdentity(ctx, id); err != nil {
		return false, err
	}
	return true, nil
}

// AddMembership binds an identity to an (existing) tenant with the given tenant
// roles. Rejects an unknown tenant and a duplicate membership.
func (s *Service) AddMembership(ctx context.Context, email, tenant string, roleTokens []string) (*iam.Identity, error) {
	id, err := s.loadIdentity(ctx, email)
	if err != nil {
		return nil, err
	}
	if _, err := s.iam.TenantByToken(ctx, tenant); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTenantNotFound
		}
		return nil, err
	}
	if _, err := s.iam.MembershipByIdentityTenant(ctx, id.ID, tenant); err == nil {
		return nil, ErrAlreadyMember
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	roles, err := s.iam.RolesByScopeTokens(ctx, iam.ScopeTenant, roleTokens)
	if err != nil {
		return nil, err
	}
	if _, err := s.iam.AddMembership(ctx, id.ID, tenant, roles); err != nil {
		return nil, err
	}
	return s.loadIdentity(ctx, email)
}

// SetMembershipRoles replaces a membership's tenant-role assignments.
func (s *Service) SetMembershipRoles(ctx context.Context, email, tenant string, roleTokens []string) (*iam.Identity, error) {
	id, mem, err := s.loadMembership(ctx, email, tenant)
	if err != nil {
		return nil, err
	}
	roles, err := s.iam.RolesByScopeTokens(ctx, iam.ScopeTenant, roleTokens)
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
	if err := s.iam.SetMembershipEnabled(ctx, mem, enabled); err != nil {
		return nil, err
	}
	return s.loadIdentity(ctx, id.Email)
}

// RemoveMembership deletes an identity's membership in a tenant.
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

// loadIdentity resolves an identity by (normalized) email, mapping not-found to
// ErrIdentityNotFound.
func (s *Service) loadIdentity(ctx context.Context, email string) (*iam.Identity, error) {
	id, err := s.iam.IdentityByEmail(ctx, iam.NormalizeEmail(email))
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
