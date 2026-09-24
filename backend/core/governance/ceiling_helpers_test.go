// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import core "github.com/devicechain-io/dc-microservice/core"

// resolvedOK is resolveOK read as "a real fetched value backs this", the question the
// tests written before resolveOK reported a Source were asking.
func resolvedOK[V any](r *tenantResolver[V], tenant string) (V, bool) {
	v, src := r.resolveOK(tenant)
	return v, src == core.CeilingResolved
}

// pair unpacks a ceiling into (rate, burst).
func pair(c core.TenantCeiling) (float64, int) { return c.RatePerSecond, c.Burst }
