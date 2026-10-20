// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeviceOrderIndexSnapshot pins the migration's own snapshot by value and its place in the
// chain: a dropped DESC, a lost event_id or a renamed index fails here before it fails against a
// server.
func TestDeviceOrderIndexSnapshot(t *testing.T) {
	assert.Equal(t, deviceOrderIndexSnapshot{
		table:    "events",
		name:     "idx_events_tenant_device_order",
		columns:  "tenant_id, device_token, occurred_time DESC, event_id DESC",
		def:      `CREATE INDEX idx_events_tenant_device_order ON "event-management".events USING btree (tenant_id, device_token, occurred_time DESC, event_id DESC)`,
		replaces: "idx_events_tenant_device_time",
	}, deviceOrderIndex)
	// What it replaces is exactly what the earlier migration built.
	assert.Equal(t, tenantDeviceIndex.name, deviceOrderIndex.replaces)

	m := NewDeviceOrderIndexSchema()
	assert.Equal(t, "20261020000000", m.ID)
	assert.Greater(t, m.ID, NewCalculatedBackfillSchema().ID)
	assert.Equal(t, migrationPosition(t, NewCalculatedBackfillSchema().ID)+1, migrationPosition(t, m.ID),
		"appended directly after the migration before it")
	assert.Nil(t, m.Rollback)
	assert.Contains(t, deviceOrderIndexManualAdvice, deviceOrderIndex.name)
}

// The hand-run build is chunk by chunk and, like the migration's own, has no IF NOT EXISTS: a
// leftover of that name must fail the statement loudly rather than be skipped as built.
func TestDeviceOrderIndexManualStatements(t *testing.T) {
	assert.Contains(t, deviceOrderIndexManualBuild, "CREATE INDEX idx_events_tenant_device_order")
	assert.Contains(t, deviceOrderIndexManualBuild, "(tenant_id, device_token, occurred_time DESC, event_id DESC)")
	assert.Contains(t, deviceOrderIndexManualBuild, "timescaledb.transaction_per_chunk")
	assert.NotContains(t, deviceOrderIndexManualBuild, "IF NOT EXISTS")
	assert.Equal(t, `DROP INDEX IF EXISTS "event-management".idx_events_tenant_device_order`, deviceOrderIndexManualDrop)
}

// TestDeviceOrderIndexFailureErrorAgreesWithTheDeviceIndex: this migration's failure table is
// the tenant-led device index's, verdict for verdict. In particular a build that ran out its
// whole allowance once locked is the sticky too-slow verdict, never a busy table to retry: a
// retry would hold the store's writers a second time within one start, and on every restart.
func TestDeviceOrderIndexFailureErrorAgreesWithTheDeviceIndex(t *testing.T) {
	timing := timeLeadingKeysDefaultTiming
	pg := func(code string) error { return fmt.Errorf("w: %w", &pgconn.PgError{Code: code}) }
	errs := []error{pg("55P03"), pg("40P01"), pg("57014"), pg("53200"), pg("XX000"),
		fmt.Errorf("w: %w", errRekeyOutOfTime), errors.New("connection reset")}
	for _, phase := range []rekeyPhase{rekeyLocking, rekeyBuilding} {
		for _, full := range []bool{true, false} {
			for _, ranOut := range []bool{true, false} {
				for _, err := range errs {
					a := rekeyAttempt{phase, full, ranOut}
					wantFinal, wantSlow := deviceIndexFailureError(a, err, timing)
					final, slow := deviceOrderIndexFailureError(a, err, timing)
					assert.Equalf(t, wantFinal == nil, final == nil, "%+v %v: retry verdict", a, err)
					assert.Equalf(t, wantSlow, slow, "%+v %v: too-slow verdict", a, err)
				}
			}
		}
	}

	final, tooSlow := deviceOrderIndexFailureError(rekeyAttempt{rekeyBuilding, true, true}, pg("57014"), timing)
	require.Error(t, final)
	assert.True(t, tooSlow)
	for _, want := range []string{deviceOrderIndex.name, "40s", deviceOrderIndexManualDrop, deviceOrderIndexManualBuild,
		`COMMENT ON INDEX "event-management".idx_events_tenant_device_time IS NULL`} {
		assert.Contains(t, final.Error(), want)
	}
	var pgErr *pgconn.PgError
	assert.True(t, errors.As(final, &pgErr), "the server error stays wrapped")
	assert.NotContains(t, final.Error(), "dcctl destroy")
}
