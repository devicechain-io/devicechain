// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
)

// ErrNoTenant is the fail-closed sentinel returned when an operation that
// touches tenant-scoped data is attempted without a tenant in context. The
// tenant-scope GORM callbacks abort statements with this error rather than
// silently running unscoped (which would risk cross-tenant data leakage).
var ErrNoTenant = errors.New("no tenant present in context; tenant-scoped operation rejected (fail-closed)")

// tenantContextKey is a private type for the tenant context key to avoid
// collisions with keys defined in other packages.
type tenantContextKey struct{}

// WithTenant returns a copy of ctx carrying the given tenant id.
func WithTenant(ctx context.Context, tenantId string) context.Context {
	return context.WithValue(ctx, tenantContextKey{}, tenantId)
}

// TenantFromContext extracts the tenant id from ctx. It returns ("", false)
// when no tenant is present or when the stored tenant is empty.
func TenantFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	value, ok := ctx.Value(tenantContextKey{}).(string)
	if !ok || value == "" {
		return "", false
	}
	return value, true
}
