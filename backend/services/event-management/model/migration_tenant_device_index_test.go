// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTenantDeviceIndexSnapshot pins the migration's own snapshot by value: a changed column
// order, a dropped DESC or a renamed index fails here before it fails against a server.
func TestTenantDeviceIndexSnapshot(t *testing.T) {
	assert.Equal(t, tenantDeviceIndexSnapshot{
		table:    "events",
		name:     "idx_events_tenant_device_time",
		columns:  "tenant_id, device_token, occurred_time DESC",
		def:      `CREATE INDEX idx_events_tenant_device_time ON "event-management".events USING btree (tenant_id, device_token, occurred_time DESC)`,
		replaces: "events_device_token_occurred_time_idx",
	}, tenantDeviceIndex)

	// The hand-run statement builds the same index the migration does, under the same name.
	assert.Contains(t, tenantDeviceIndexManualBuild, "idx_events_tenant_device_time")
	assert.Contains(t, tenantDeviceIndexManualBuild, "(tenant_id, device_token, occurred_time DESC)")
	assert.Contains(t, tenantDeviceIndexManualBuild, "timescaledb.transaction_per_chunk")
	assert.NotContains(t, tenantDeviceIndexManualBuild, "IF NOT EXISTS",
		"an index of that name, an invalid leftover included, must fail the statement, not be skipped")
	assert.Equal(t, `DROP INDEX IF EXISTS "event-management".idx_events_tenant_device_time`, tenantDeviceIndexManualDrop)

	m := NewTenantDeviceIndexSchema()
	assert.Equal(t, "20261004000000", m.ID)
	assert.Greater(t, m.ID, NewTimeLeadingKeysSchema().ID)
	for i, mig := range Migrations {
		if mig.ID == m.ID {
			require.Greater(t, i, 0)
			assert.Equal(t, NewTimeLeadingKeysSchema().ID, Migrations[i-1].ID, "appended after the key rebuild")
		}
	}
	assert.Nil(t, m.Rollback, "a rollback would be the same build again")

	// One definition of the bounds: the upgrade notes publish one row bound and one chunk bound
	// for this migration and the key rebuild, so this migration is built on the rebuild's value.
	assert.EqualValues(t, 4_000_000, timeLeadingKeysDefaultTiming.maxRows)
	assert.EqualValues(t, 500, timeLeadingKeysDefaultTiming.maxChunks)
}

// TestDropTimingBoundsADropByTheLockAttempt: the old index is dropped under the trim's bounds,
// taken from the build's timing.
func TestDropTimingBoundsADropByTheLockAttempt(t *testing.T) {
	d := dropTiming(timeLeadingKeysDefaultTiming)
	assert.Equal(t, indexTrimTiming{
		lockTimeout: 3_000_000_000, statementTimeout: 5_000_000_000, pause: 2_000_000_000,
		budget: 60_000_000_000, maxChunks: 500, countTimeout: 10_000_000_000,
	}, d)
}

// TestDeviceIndexFailureError: every case of the failure table, with the verdict and the
// message an operator reads. Nothing here may name a recreate.
func TestDeviceIndexFailureError(t *testing.T) {
	timing := timeLeadingKeysDefaultTiming
	pg := func(code string) error { return fmt.Errorf("w: %w", &pgconn.PgError{Code: code}) }

	// A busy table is retried: nil, no verdict.
	for _, tc := range []struct {
		phase rekeyPhase
		code  string
	}{
		{rekeyLocking, "55P03"}, {rekeyLocking, "40P01"}, {rekeyLocking, "57014"},
		{rekeyBuilding, "55P03"}, {rekeyBuilding, "40P01"},
	} {
		for _, a := range []rekeyAttempt{{tc.phase, true, true}, {tc.phase, false, true}, {tc.phase, true, false}} {
			final, tooSlow := deviceIndexFailureError(a, pg(tc.code), timing)
			assert.NoErrorf(t, final, "phase %d %s is a busy table", tc.phase, tc.code)
			assert.False(t, tooSlow)
		}
	}

	// The verdict: the build had the whole allowance and still ran out.
	final, tooSlow := deviceIndexFailureError(rekeyAttempt{rekeyBuilding, true, true}, pg("57014"), timing)
	require.Error(t, final)
	assert.True(t, tooSlow, "the too-slow verdict must stick")
	for _, want := range []string{"idx_events_tenant_device_time", "40s", "events_device_token_occurred_time_idx",
		tenantDeviceIndexManualDrop, tenantDeviceIndexManualBuild,
		`COMMENT ON INDEX "event-management".events_device_token_occurred_time_idx IS NULL`} {
		assert.Contains(t, final.Error(), want)
	}
	var pgErr *pgconn.PgError
	assert.True(t, errors.As(final, &pgErr), "the server error stays wrapped")
	assert.NotContains(t, final.Error(), "dcctl destroy")

	// The same timeout with LESS than the whole allowance is this start running out of budget.
	final, tooSlow = deviceIndexFailureError(rekeyAttempt{rekeyBuilding, false, true}, pg("57014"), timing)
	require.Error(t, final)
	assert.False(t, tooSlow)
	assert.Contains(t, final.Error(), "continues from here on the next start")

	// A cancel (a 57014 that had not run its whole timeout) is not a verdict.
	final, tooSlow = deviceIndexFailureError(rekeyAttempt{rekeyBuilding, true, false}, pg("57014"), timing)
	require.Error(t, final)
	assert.False(t, tooSlow, "pg_cancel_backend is not a verdict")
	assert.Contains(t, final.Error(), "next start tries again")

	final, tooSlow = deviceIndexFailureError(rekeyAttempt{rekeyBuilding, true, true}, fmt.Errorf("w: %w", errRekeyOutOfTime), timing)
	require.Error(t, final)
	assert.False(t, tooSlow)
	assert.Contains(t, final.Error(), "continues from here on the next start")

	for _, phase := range []rekeyPhase{rekeyLocking, rekeyBuilding} {
		final, tooSlow = deviceIndexFailureError(rekeyAttempt{phase, true, true}, pg("53200"), timing)
		require.Error(t, final)
		assert.False(t, tooSlow)
		assert.Contains(t, final.Error(), "max_locks_per_transaction")
	}

	final, tooSlow = deviceIndexFailureError(rekeyAttempt{rekeyBuilding, true, true}, errors.New("connection reset"), timing)
	require.Error(t, final)
	assert.False(t, tooSlow)
	assert.Contains(t, final.Error(), "next start tries again")
	assert.Contains(t, final.Error(), tenantDeviceIndexManualBuild, "a failure that repeats names the manual build")
}

// TestDeviceIndexFailureErrorAgreesWithTheKeyRebuild: this migration's failure table is a copy
// of the key rebuild's, because that code is shipped and left alone; the two must reach the
// same verdict (retry, stop, or the sticky too-slow) for every phase, code and attempt shape,
// so one policy implemented twice cannot drift in silence.
func TestDeviceIndexFailureErrorAgreesWithTheKeyRebuild(t *testing.T) {
	k := timeLeadingKeys[0]
	timing := timeLeadingKeysDefaultTiming
	errs := []error{
		&pgconn.PgError{Code: "55P03"}, &pgconn.PgError{Code: "40P01"}, &pgconn.PgError{Code: "57014"},
		&pgconn.PgError{Code: "53200"}, &pgconn.PgError{Code: "XX000"}, errRekeyOutOfTime, errors.New("other"),
	}
	for _, phase := range []rekeyPhase{rekeyLocking, rekeyBuilding} {
		for _, full := range []bool{true, false} {
			for _, ranOut := range []bool{true, false} {
				a := rekeyAttempt{phase, full, ranOut}
				for _, err := range errs {
					wantFinal, wantSlow := timeLeadingKeysFailureError(k, a, err, timing, "p")
					gotFinal, gotSlow := deviceIndexFailureError(a, err, timing)
					assert.Equalf(t, wantFinal == nil, gotFinal == nil, "retry vs stop: %+v %v", a, err)
					assert.Equalf(t, wantSlow, gotSlow, "too-slow verdict: %+v %v", a, err)
				}
			}
		}
	}
}

// TestTenantDeviceIndexMessagesNeverSayRecreate: every message, rendered with arguments, has
// no format error, never asks for a recreate (this index is a performance index on a schema
// that is correct without it), and every refusal that leaves the choice to the operator hands
// over both statements.
func TestTenantDeviceIndexMessagesNeverSayRecreate(t *testing.T) {
	cause := errors.New("cause")
	timing := timeLeadingKeysDefaultTiming
	refusals := map[string]string{
		"history": fmt.Sprintf(tenantDeviceIndexHistoryMessage, 4000000),
		"chunks":  fmt.Sprintf(tenantDeviceIndexChunksMessage, "events (600) has", 500, chunkListQuery),
		"refused": fmt.Sprintf(tenantDeviceIndexRefusedMessage, "i", "o", "marker", "o"),
		"tooslow": fmt.Errorf(tenantDeviceIndexTooSlowMessage, "i", timing.tableBuild, "o", "o", cause).Error(),
		"failed":  fmt.Errorf(tenantDeviceIndexFailedMessage, "i", cause).Error(),
	}
	others := map[string]string{
		"chunkcount": fmt.Errorf(tenantDeviceIndexChunkCountFailedMessage, chunkListQuery, cause).Error(),
		"rowcount":   fmt.Errorf(tenantDeviceIndexRowCountFailedMessage, cause).Error(),
		"busy": fmt.Errorf(tenantDeviceIndexBusyMessage, "i", 3, timing.lockAttempt, timing.budget,
			indexTrimHolderQuery("events"), cause).Error(),
		"outoftime": fmt.Sprintf(tenantDeviceIndexOutOfTimeMessage, "i", "budget"),
		"locktable": fmt.Errorf(tenantDeviceIndexLockTableFullMessage, "i", cause).Error(),
		"shape":     fmt.Sprintf(tenantDeviceIndexShapeMessage, "i", "x", "y"),
		"marker":    fmt.Errorf(tenantDeviceIndexMarkerFailedNote, cause, cause).Error(),
	}
	for name, msg := range refusals {
		assert.Containsf(t, msg, tenantDeviceIndexManualDrop, "%s hands over the drop of a leftover", name)
		assert.Containsf(t, msg, tenantDeviceIndexManualBuild, "%s hands over the build", name)
		assert.NotContainsf(t, msg, "%!", "%s: every verb has its argument", name)
		assert.NotContainsf(t, strings.ToLower(msg), "recreate", "%s", name)
		assert.NotContainsf(t, msg, "dcctl destroy", "%s", name)
	}
	for name, msg := range others {
		assert.NotContainsf(t, msg, "%!", "%s: every verb has its argument", name)
		assert.NotContainsf(t, strings.ToLower(msg), "recreate", "%s", name)
		assert.NotContainsf(t, msg, "dcctl destroy", "%s", name)
	}
}
