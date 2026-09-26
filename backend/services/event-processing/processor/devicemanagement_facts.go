// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
)

// DeviceManagementFacts is the OFF-LOOP seam onto device-management's authoritative state, of
// which this service's rule, active-version, roster and attribute projections are caches. The
// fact reconcile (fact_reconcile.go) reads through it; nothing on the single-writer loop does.
//
// 🔴 EVERY READ RETURNS THE WHOLE ANSWER OR AN ERROR — NEVER A PREFIX. A walk that failed on its
// third page must not hand back the first two: the reconcile treats a row it holds that is absent
// from the answer as deleted, so a partial answer would read as a mass deletion. Returning slices
// rather than streaming entries into a callback is what makes that structural instead of a
// promise each caller has to keep.
type DeviceManagementFacts interface {
	// Tenants lists every tenant on the instance (user-management's tenantTokens).
	Tenants(ctx context.Context) ([]string, error)
	// ActiveProfiles returns every published profile of the tenant with its active version,
	// that version's activation instant and its enabled rules.
	ActiveProfiles(ctx context.Context, tenant string) ([]dmmodel.ActiveProfileRules, error)
	// Roster returns every device of the tenant with its profile and membership instant.
	Roster(ctx context.Context, tenant string) ([]dmmodel.DeviceRosterEntry, error)
	// ThresholdAttributes returns every attribute of the tenant a dynamic threshold can read.
	ThresholdAttributes(ctx context.Context, tenant string) ([]dmmodel.DeviceThresholdAttribute, error)
}
