// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
)

// The tenant's own "second factor required" setting, from the two planes that may change it. The
// store writes one column; the tenant's EFFECTIVE requirement is that column OR its tier's floor,
// so neither plane can lower what the tier sets.

// SetOwnTenantMfaRequired is the tenant plane's setter: the caller's own tenant, taken from the
// verified claims. A scoped caller is refused (tenantCaller), and so is one whose live grant no
// longer holds an unscoped membership or the superuser role (resolveActor).
func (s *Service) SetOwnTenantMfaRequired(ctx context.Context, required bool) (*iam.Tenant, error) {
	caller, err := tenantCaller(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.resolveActor(ctx, s.iam, caller); err != nil {
		return nil, err
	}
	return s.setTenantMfaRequired(ctx, caller.Tenant, caller.Email, required)
}

// SetTenantMfaRequired is the operator plane's setter for any tenant.
func (s *Service) SetTenantMfaRequired(ctx context.Context, tenant string, required bool) (*iam.Tenant, error) {
	actor := "system"
	if c, ok := auth.ClaimsFromContext(ctx); ok && c.Username != "" {
		actor = c.Username
	}
	return s.setTenantMfaRequired(ctx, tenant, actor, required)
}

func (s *Service) setTenantMfaRequired(ctx context.Context, tenant, actor string, required bool) (*iam.Tenant, error) {
	t, err := s.loadTenant(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if t.PurgeState.Deleted() {
		return nil, ErrTenantNotFound
	}
	if t.MfaRequired == required {
		return t, nil
	}
	if err := s.iam.SetTenantMfaRequired(ctx, t, required); err != nil {
		return nil, err
	}
	label := "required"
	if !required {
		label = "not required"
	}
	// Best-effort, like every auth audit row: the setting has already changed.
	_ = s.iam.RecordAuth(ctx, rdb.AuditOpTenantMfaRequiredChanged, actor, tenant, label)
	return t, nil
}
