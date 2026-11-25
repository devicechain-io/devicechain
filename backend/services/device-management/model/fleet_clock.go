// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	"gorm.io/gorm"
)

// fleetClock is only the SQLite fixture clock. Shared PostgreSQL state always
// uses clock_timestamp(); leases use time.Now's process monotonic clock.
var fleetClock = time.Now

// fleetPermitHook is a test seam: it sees every grant and the pacing window it was charged to.
var fleetPermitHook func(window time.Time, granted int)

func fleetTimestamp(db *gorm.DB) any {
	if db.Dialector.Name() == "postgres" {
		return gorm.Expr("clock_timestamp()")
	}
	return fleetClock()
}

func fleetTimestampAfter(db *gorm.DB, delay time.Duration, sqliteNow time.Time) any {
	if db.Dialector.Name() == "postgres" {
		return gorm.Expr("clock_timestamp() + ? * interval '1 second'", delay.Seconds())
	}
	if sqliteNow.IsZero() {
		sqliteNow = fleetClock()
	}
	return sqliteNow.Add(delay)
}

// The locked CTE reads the current row even after waiting for another replica.
// Sample the clock AFTER that lock: a queued request must not roll a window
// against a stale transaction/statement timestamp. Materializing timed also
// gives the rollover comparison and the new window exactly the same timestamp.
// RETURNING carries the grant computed from the locked pre-update count; the
// updated count alone cannot distinguish a partial grant from a full one.
//
// A window that starts in the FUTURE rolls too. On this database clock that
// cannot be written, but a primary that fails over to one whose clock is behind,
// or a clock stepped back, would otherwise leave an operation with a spent
// window that stays shut until wall time catches up with the stored start.
const fleetAcquirePermitsSQL = `WITH locked AS MATERIALIZED (
	SELECT operation_id, pace_window_start, pace_issued
	FROM fleet_operation_runtime
	WHERE operation_id = @id AND tenant_id = @tenant
	FOR UPDATE
), timed AS MATERIALIZED (
	SELECT locked.*, clock_timestamp() AS now FROM locked
), budget AS (
	SELECT *, pace_window_start IS NULL OR pace_window_start <= now - interval '1 second' OR pace_window_start > now AS rolled
	FROM timed
), permits AS (
	SELECT *, least(@want, greatest(@allowed - CASE WHEN rolled THEN 0 ELSE pace_issued END, 0)) AS granted
	FROM budget
)
UPDATE fleet_operation_runtime AS runtime
SET pace_window_start = CASE WHEN permits.rolled THEN permits.now ELSE runtime.pace_window_start END,
	pace_issued = CASE WHEN permits.rolled THEN permits.granted ELSE runtime.pace_issued + permits.granted END
FROM permits
WHERE runtime.operation_id = permits.operation_id
RETURNING permits.granted, runtime.pace_window_start`
