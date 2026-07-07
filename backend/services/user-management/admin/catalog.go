// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"gorm.io/gorm"
)

// Role-catalog and tenant errors (ADR-033). Sentinels for the resolver layer.
var (
	ErrRoleNotFound         = errors.New("role not found")
	ErrProtectedRole        = errors.New("the superuser system role cannot be deleted")
	ErrTenantHasMemberships = errors.New("tenant still has memberships; remove them first")
)

// RoleInput is the data to create a role (ADR-008 RBAC / ADR-033). Scope is
// "system" or "tenant"; every authority must name a known capability.
type RoleInput struct {
	Scope       string
	Token       string
	Name        string
	Description string
	Authorities []string
}

// RoleMutableInput is the data to update a role: its identity (scope, token) is
// fixed, only the name/description/authorities change.
type RoleMutableInput struct {
	Name        string
	Description string
	Authorities []string
}

// ListRoles returns the role catalog, optionally filtered to a scope.
func (s *Service) ListRoles(ctx context.Context, scope *iam.RoleScope) ([]iam.Role, error) {
	return s.iam.ListRoles(ctx, scope)
}

// CreateRole creates a role after validating its scope and authorities.
func (s *Service) CreateRole(ctx context.Context, in RoleInput) (*iam.Role, error) {
	scope, err := parseScope(in.Scope)
	if err != nil {
		return nil, err
	}
	if in.Token == "" {
		return nil, fmt.Errorf("token is required")
	}
	if err := validateAuthorities(in.Authorities); err != nil {
		return nil, err
	}
	r := &iam.Role{
		Scope: scope, Token: in.Token, Authorities: in.Authorities,
		NamedEntity: rdb.NamedEntity{Name: rdb.NullStrOf(&in.Name), Description: rdb.NullStrOf(&in.Description)},
	}
	if err := s.iam.CreateRole(ctx, r); err != nil {
		return nil, err
	}
	return s.iam.RoleByScopeToken(ctx, scope, in.Token)
}

// UpdateRole replaces a role's name/description/authorities.
func (s *Service) UpdateRole(ctx context.Context, scope, token string, in RoleMutableInput) (*iam.Role, error) {
	rs, err := parseScope(scope)
	if err != nil {
		return nil, err
	}
	if err := validateAuthorities(in.Authorities); err != nil {
		return nil, err
	}
	r, err := s.loadRole(ctx, rs, token)
	if err != nil {
		return nil, err
	}
	r.Name = rdb.NullStrOf(&in.Name)
	r.Description = rdb.NullStrOf(&in.Description)
	r.Authorities = in.Authorities
	if err := s.iam.UpdateRole(ctx, r); err != nil {
		return nil, err
	}
	return s.iam.RoleByScopeToken(ctx, rs, token)
}

// DeleteRole removes a role and clears its assignments. Idempotent: a missing
// role returns (false, nil). The seeded superuser system role is protected so the
// instance cannot be locked out of its own admin plane.
func (s *Service) DeleteRole(ctx context.Context, scope, token string) (bool, error) {
	rs, err := parseScope(scope)
	if err != nil {
		return false, err
	}
	if rs == iam.ScopeSystem && token == iam.SuperuserRoleToken {
		return false, ErrProtectedRole
	}
	r, err := s.iam.RoleByScopeToken(ctx, rs, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.iam.DeleteRole(ctx, r); err != nil {
		return false, err
	}
	return true, nil
}

// TenantInput is the data to create a tenant. Config is freeform JSON (ADR-033).
// The Ingest* governance overrides are nil to inherit the platform default.
type TenantInput struct {
	Token                   string
	Name                    string
	Config                  map[string]any
	IngestMessagesPerSecond *float64
	IngestBurst             *int
}

// TenantMutableInput is the data to update a tenant: its token is fixed.
type TenantMutableInput struct {
	Name                    string
	Config                  map[string]any
	IngestMessagesPerSecond *float64
	IngestBurst             *int
}

// validateGovernance rejects a non-positive override. A nil field means "inherit
// the platform default"; a provided value must be positive — a zero or negative
// ceiling is never a valid override (the platform default, itself always
// positive, is the fail-safe floor), so callers clear an override by omitting it,
// not by setting it to zero.
func validateGovernance(mps *float64, burst *int) error {
	if mps != nil && *mps <= 0 {
		return fmt.Errorf("ingestMessagesPerSecond override must be positive (got %v); omit it to inherit the platform default", *mps)
	}
	if burst != nil && *burst <= 0 {
		return fmt.Errorf("ingestBurst override must be positive (got %d); omit it to inherit the platform default", *burst)
	}
	return nil
}

// CreateTenant registers a new tenant (enabled by default).
func (s *Service) CreateTenant(ctx context.Context, in TenantInput) (*iam.Tenant, error) {
	if in.Token == "" {
		return nil, fmt.Errorf("token is required")
	}
	if err := validateGovernance(in.IngestMessagesPerSecond, in.IngestBurst); err != nil {
		return nil, err
	}
	t := &iam.Tenant{
		Token: in.Token, Enabled: true, Config: in.Config,
		NamedEntity:             rdb.NamedEntity{Name: rdb.NullStrOf(&in.Name)},
		IngestMessagesPerSecond: in.IngestMessagesPerSecond,
		IngestBurst:             in.IngestBurst,
	}
	if err := s.iam.CreateTenant(ctx, t); err != nil {
		return nil, err
	}
	return s.iam.TenantByToken(ctx, in.Token)
}

// UpdateTenant replaces a tenant's name, config, and governance overrides. A nil
// override field clears it (reverting the tenant to the platform default), so the
// update is a full replace of the mutable fields, not a partial patch.
func (s *Service) UpdateTenant(ctx context.Context, token string, in TenantMutableInput) (*iam.Tenant, error) {
	if err := validateGovernance(in.IngestMessagesPerSecond, in.IngestBurst); err != nil {
		return nil, err
	}
	t, err := s.loadTenant(ctx, token)
	if err != nil {
		return nil, err
	}
	t.Name = rdb.NullStrOf(&in.Name)
	t.Config = in.Config
	t.IngestMessagesPerSecond = in.IngestMessagesPerSecond
	t.IngestBurst = in.IngestBurst
	if err := s.iam.UpdateTenant(ctx, t); err != nil {
		return nil, err
	}
	return s.iam.TenantByToken(ctx, token)
}

// SetTenantEnabled enables or disables a tenant.
func (s *Service) SetTenantEnabled(ctx context.Context, token string, enabled bool) (*iam.Tenant, error) {
	t, err := s.loadTenant(ctx, token)
	if err != nil {
		return nil, err
	}
	if err := s.iam.SetTenantEnabled(ctx, t, enabled); err != nil {
		return nil, err
	}
	return s.iam.TenantByToken(ctx, token)
}

// DeleteTenant removes a tenant. Idempotent: a missing tenant returns (false,
// nil). Rejected when memberships still reference it, so it cannot orphan access.
func (s *Service) DeleteTenant(ctx context.Context, token string) (bool, error) {
	t, err := s.iam.TenantByToken(ctx, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	n, err := s.iam.CountMembershipsInTenant(ctx, token)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, ErrTenantHasMemberships
	}
	if err := s.iam.DeleteTenant(ctx, t); err != nil {
		return false, err
	}
	return true, nil
}

// loadRole resolves a role by (scope, token), mapping not-found to ErrRoleNotFound.
func (s *Service) loadRole(ctx context.Context, scope iam.RoleScope, token string) (*iam.Role, error) {
	r, err := s.iam.RoleByScopeToken(ctx, scope, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRoleNotFound
	}
	return r, err
}

// loadTenant resolves a tenant by token, mapping not-found to ErrTenantNotFound.
func (s *Service) loadTenant(ctx context.Context, token string) (*iam.Tenant, error) {
	t, err := s.iam.TenantByToken(ctx, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTenantNotFound
	}
	return t, err
}

// parseScope maps the wire scope string to a RoleScope, rejecting anything else.
func parseScope(s string) (iam.RoleScope, error) {
	scope := iam.RoleScope(s)
	if !scope.Valid() {
		return "", fmt.Errorf("invalid role scope %q (want %q or %q)", s, iam.ScopeSystem, iam.ScopeTenant)
	}
	return scope, nil
}

// validateAuthorities rejects the request if any authority is not a known
// capability, so a typo cannot create a role that silently grants nothing.
func validateAuthorities(authorities []string) error {
	for _, a := range authorities {
		if !auth.ValidAuthority(a) {
			return fmt.Errorf("unknown authority %q", a)
		}
	}
	return nil
}
