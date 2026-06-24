// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import "context"

// systemContextKey is a private type for the system-context marker key.
type systemContextKey struct{}

// WithSystemContext marks ctx as a deliberate, tenant-unscoped "system"
// operation. The tenant-scope GORM callbacks (rdb) skip both the
// "WHERE tenant_id = ?" predicate and the fail-closed ErrNoTenant check when
// this marker is present, so a query can read across tenants.
//
// SECURITY: this is the one sanctioned bypass of the otherwise un-skippable
// tenant isolation (ADR-015). It exists for the narrow bootstrap operations
// that must run before a tenant is known — above all the login lookup, which
// resolves a user (and therefore their tenant) from a username. It MUST only
// ever be set deep inside those trusted operations and MUST NEVER be derived
// from request input or set in HTTP middleware. Every call site is a security
// review point. The standing cross-tenant isolation test guards the normal
// (non-system) path; a system context intentionally falls outside it.
func WithSystemContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, systemContextKey{}, true)
}

// IsSystemContext reports whether ctx was marked as a system (tenant-unscoped)
// operation via WithSystemContext.
func IsSystemContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value, ok := ctx.Value(systemContextKey{}).(bool)
	return ok && value
}
