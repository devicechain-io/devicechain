// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
)

// ErrStopSweep counts the current tenant as failed and stops visiting tenants.
var ErrStopSweep = errors.New("stop the sweep")

// TenantSweep records the number of tenants attempted and failed.
type TenantSweep struct{ Visited, Failed int }

// ForEachTenant visits tenants in order under their own tenant contexts, checking
// cancellation before each visit. Errors are counted and isolated to one tenant,
// unless they wrap ErrStopSweep. Empty tokens fail closed without calling fn.
// The completed tally is classified by PassResult. It does not log.
func ForEachTenant(ctx context.Context, tenants []string, fn func(context.Context, string) error) (TenantSweep, error) {
	var sweep TenantSweep
	var lastErr error
	for _, tenant := range tenants {
		if err := ctx.Err(); err != nil {
			return sweep, err
		}
		sweep.Visited++
		var err error
		if tenant == "" {
			err = ErrNoTenant
		} else {
			err = fn(WithTenant(ctx, tenant), tenant)
		}
		if err != nil {
			sweep.Failed++
			lastErr = err
			if errors.Is(err, ErrStopSweep) {
				return sweep, err
			}
		}
	}
	return sweep, PassResult(sweep.Visited, sweep.Failed, lastErr)
}
