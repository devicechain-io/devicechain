// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package admin is the instance-scoped control plane for user-management
// (ADR-033): the application service behind the admin GraphQL API. It manages the
// global identity directory, the per-tenant memberships, the global role catalog,
// and the tenant registry — all of which are instance-global iam entities reached
// through the system context (see iam.Store).
//
// It is the admin-plane counterpart to identity.Manager: where the Manager owns
// authentication (tokens, the signing key, login), this Service owns the
// administration of the principals those tokens describe. Authorization is
// enforced at the GraphQL resolver layer (auth.Authorize on a system authority);
// the Service itself is unauthenticated business logic.
package admin

import (
	"context"

	"github.com/devicechain-io/dc-user-management/iam"
)

// Service is the admin control-plane application service. It wraps the iam store
// and (in later phases) adds the cross-cutting logic the raw store should not own:
// password hashing on identity creation and authority validation on role writes.
type Service struct {
	iam *iam.Store
}

// NewService builds the admin Service over the iam store.
func NewService(store *iam.Store) *Service { return &Service{iam: store} }

// ListIdentities returns the full identity directory (system roles + memberships
// preloaded) for the admin console.
func (s *Service) ListIdentities(ctx context.Context) ([]iam.Identity, error) {
	return s.iam.ListIdentities(ctx)
}

// ListTenants returns every control-plane tenant row (ADR-033).
func (s *Service) ListTenants(ctx context.Context) ([]iam.Tenant, error) {
	return s.iam.ListTenants(ctx)
}
