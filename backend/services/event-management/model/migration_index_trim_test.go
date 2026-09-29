// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
)

// TestIsRetryableDropFailure pins which failures the index trim retries: only a busy
// table. Anything else must fail the migration at once rather than spend the budget.
func TestIsRetryableDropFailure(t *testing.T) {
	pg := func(code string) error { return &pgconn.PgError{Code: code} }
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"lock_timeout fired", pg("55P03"), true},
		{"deadlock victim", pg("40P01"), true},
		{"statement_timeout fired", pg("57014"), true},
		{"wrapped lock_timeout", fmt.Errorf("drop: %w", pg("55P03")), true},
		{"undefined table", pg("42P01"), false},
		{"feature not supported", pg("0A000"), false},
		{"lock table full", pg("53200"), false},
		{"not a server error", errors.New("x"), false},
		{"nil", nil, false},
	} {
		assert.Equal(t, tc.want, isRetryableDropFailure(tc.err), tc.name)
	}
	assert.True(t, isLockTableFull(fmt.Errorf("w: %w", pg("53200"))))
	assert.False(t, isLockTableFull(pg("55P03")))
}

// TestIndexTrimSnapshot pins the migration's own snapshot by value: twelve distinct
// indexes, none of them one a query needs. The integration suite proves the resulting
// schema; this catches a list edit without a server.
func TestIndexTrimSnapshot(t *testing.T) {
	assert.Len(t, indexTrimDropped, 12)
	seen := map[string]bool{}
	for _, idx := range indexTrimDropped {
		assert.False(t, seen[idx.name], "duplicate %s", idx.name)
		seen[idx.name] = true
	}
	for _, kept := range []string{
		"events_device_token_occurred_time_idx",   // the per-device read
		"measurement_events_occurred_time_idx",    // the rollup refresh's cross-tenant range
		"events_tenant_id_occurred_time_idx",      // tenant-wide reads
		"idx_measurement_tenant_device_name_time", // the raw bucketed read
		"idx_event_anchors_lookup",                // the anchor lookup
		"uq_measurement_events_idem",              // an idempotency arbiter
		"uq_state_change_events_idem",
		"idx_events_tenant_alt_id",
	} {
		assert.False(t, seen[kept], "%s is read by a query and must be kept", kept)
	}
	assert.Nil(t, NewIndexTrimSchema().Rollback,
		"a rollback would be a non-concurrent index build over live chunks")
}
