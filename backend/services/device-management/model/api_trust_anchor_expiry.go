// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// CountTrustAnchorsExpiring is how many enabled trust anchors, across every tenant, are past
// their notAfter or reach it within the window. It is one instance-wide count, not a series per
// anchor or per tenant, so the gauge it feeds has no cardinality.
//
// An anchor already past its notAfter counts: it no longer verifies anything (the chain verifier
// re-checks it at every connect), so every device that relies on it is refused until it is
// replaced, which is exactly what the operator reading the alert needs to hear.
//
// Disabled anchors do not count: nothing authenticates through one, and the operator has already
// acted on it.
func (api *Api) CountTrustAnchorsExpiring(ctx context.Context, within time.Duration, now time.Time) (int64, error) {
	var n int64
	// Cross-tenant by necessity: one gauge for the whole instance. Reads a count and nothing a
	// tenant could be told.
	err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&DeviceTrustAnchor{}).
		Where("enabled = ? AND not_after <= ?", true, now.Add(within)).Count(&n).Error
	return n, err
}
