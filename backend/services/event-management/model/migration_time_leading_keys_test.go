// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTimeLeadingKeysSnapshot pins the migration's own snapshot by value, without a
// server: five keys, each a PERMUTATION of its old columns that leads with
// (tenant_id, occurred_time) — the same set is what lets ON CONFLICT infer the rebuilt key
// from the unchanged conflict lists, on this release and on the previous one — and exactly
// the four tenant-time indexes dropped, none of the indexes a read still needs.
func TestTimeLeadingKeysSnapshot(t *testing.T) {
	require.Len(t, timeLeadingKeys, 5)
	sortedCols := func(s string) string {
		c := strings.Split(s, ",")
		sort.Strings(c)
		return strings.Join(c, ",")
	}
	var dropped []string
	for _, k := range timeLeadingKeys {
		assert.Truef(t, strings.HasPrefix(k.newColumns, "tenant_id,occurred_time,"), "%s leads with %s", k.name, k.newColumns)
		assert.Equalf(t, sortedCols(k.oldColumns), sortedCols(k.newColumns), "%s must keep its column set", k.name)
		assert.NotEqualf(t, k.oldColumns, k.newColumns, "%s must change order", k.name)
		assert.Equal(t, k.name == "events_pkey", k.primaryKey, k.name)
		if k.dropped != "" {
			assert.Equal(t, k.table+"_tenant_id_occurred_time_idx", k.dropped)
			dropped = append(dropped, k.dropped)
		}
	}
	assert.Equal(t, []string{
		"events_tenant_id_occurred_time_idx",
		"measurement_events_tenant_id_occurred_time_idx",
		"location_events_tenant_id_occurred_time_idx",
		"alert_events_tenant_id_occurred_time_idx",
	}, dropped)
	assert.Equal(t, "event_anchors", timeLeadingKeys[4].table)
	assert.Empty(t, timeLeadingKeys[4].dropped, "event_anchors has no tenant-time index")
	assert.Equal(t, "events", timeLeadingKeys[0].table, "events goes first")

	m := NewTimeLeadingKeysSchema()
	assert.Nil(t, m.Rollback, "a rollback would be the same build over live chunks")
	assert.Equal(t, "20261001000000", m.ID)
	assert.Greater(t, m.ID, NewIndexTrimSchema().ID)
	assert.Equal(t, m.ID, Migrations[len(Migrations)-1].ID, "appended last")

	// The defaults, by value: the arithmetic on timeLeadingKeysDefaultTiming depends on them.
	assert.Equal(t, timeLeadingKeysTiming{
		lockTimeout: 3 * time.Second, lockAttempt: 5 * time.Second, pause: 2 * time.Second,
		tableBuild: 40 * time.Second, minBuild: time.Second, budget: 60 * time.Second,
		countTimeout: 10 * time.Second, maxRows: 4_000_000, maxChunks: 500, buildMemory: "256MB",
	}, timeLeadingKeysDefaultTiming)
	assert.EqualValues(t, 500, indexTrimDefaultTiming.maxChunks, "the trim gates at the same ceiling")
	assert.Equal(t, 10*time.Second, indexTrimDefaultTiming.countTimeout)
}

// TestOverChunkCeiling: the ceiling is strict (exactly limit chunks is allowed), per table
// (never a sum across tables), and names every table over it, with its count, in the order
// given.
func TestOverChunkCeiling(t *testing.T) {
	tables := []string{"events", "measurement_events", "location_events"}
	assert.Equal(t, "events (1001) has",
		overChunkCeiling(map[string]int64{"events": 1001, "measurement_events": 1000, "location_events": 999}, tables, 1000))
	assert.Equal(t, "events (1001), location_events (5000) have",
		overChunkCeiling(map[string]int64{"location_events": 5000, "events": 1001}, tables, 1000))
	assert.Equal(t, "", overChunkCeiling(map[string]int64{"events": 1000, "measurement_events": 1000,
		"location_events": 1000}, tables, 1000), "3,000 chunks across three tables is not over a per-table 1,000")
	assert.Equal(t, "", overChunkCeiling(map[string]int64{"alert_events": 5000}, tables, 1000),
		"a table this start would not lock is not counted against it")
	assert.Equal(t, "", overChunkCeiling(nil, tables, 1000))
}

// TestEventStoreChunksMessage: the refusal names the tables and counts, the ceiling, what this
// start changed, how to list the chunks, the remedy and what it deletes, and the recreate.
func TestEventStoreChunksMessage(t *testing.T) {
	msg := fmt.Sprintf(eventStoreChunksMessage, "rebuilding the event store's identity keys", "events (1834) has",
		1000, "no table has been re-keyed yet", chunkListQuery)
	for _, want := range []string{"rebuilding the event store's identity keys: events (1834) has more than 1000 chunks",
		"changed nothing (no table has been re-keyed yet)", "Nothing is marked", "lifecycle.chunkIntervalHours",
		chunkListQuery, "revoke the device's credential", "drop_chunks", "every tenant's events",
		"The event store's keys lead with time", "dcctl destroy", "export",
		"previous event-management keeps storing"} {
		assert.Contains(t, msg, want)
	}
	assert.NotContains(t, msg, "%!", "every verb has its argument")
}

// TestTimeoutSetting pins the one rendering of a statement_timeout: rounded UP to whole
// milliseconds, and never a value under one, because 0 turns the timeout OFF and a negative
// value is a server error that would be reported as something else.
func TestTimeoutSetting(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{40 * time.Second, "40000ms"},
		{1200 * time.Microsecond, "2ms"},
		{time.Millisecond, "1ms"},
	} {
		got, err := timeoutSetting(tc.d)
		require.NoError(t, err, tc.d)
		assert.Equal(t, tc.want, got, tc.d)
	}
	for _, d := range []time.Duration{0, 400 * time.Microsecond, -150 * time.Millisecond} {
		got, err := timeoutSetting(d)
		assert.ErrorIsf(t, err, errRekeyOutOfTime, "%s", d)
		assert.Emptyf(t, got, "%s must never render a setting", d)
	}
}

// TestBuildAllowance: a swap gets the whole tableBuild while the budget has it, and what is
// left otherwise — reported as not full, so its timeout is never the too-slow verdict — and
// none at all, out of time, once less than minBuild is left.
func TestBuildAllowance(t *testing.T) {
	timing := timeLeadingKeysDefaultTiming
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		left time.Duration
		want time.Duration
		full bool
	}{
		{55 * time.Second, 40 * time.Second, true},
		{40 * time.Second, 40 * time.Second, true}, // exactly tableBuild left is the whole allowance
		{12 * time.Second, 12 * time.Second, false},
		{time.Second, time.Second, false}, // exactly minBuild left is still a swap
	} {
		got, full, err := buildAllowance(timing, now.Add(tc.left), now)
		require.NoError(t, err, tc.left)
		assert.Equal(t, tc.want, got, tc.left)
		assert.Equal(t, tc.full, full, tc.left)
	}
	// Under minBuild — including the 1 ms that timeoutSetting alone would still render —
	// no swap is started.
	for _, left := range []time.Duration{time.Second - time.Nanosecond, 5 * time.Millisecond, 0, -time.Second} {
		got, full, err := buildAllowance(timing, now.Add(left), now)
		assert.ErrorIsf(t, err, errRekeyOutOfTime, "%s left", left)
		assert.Zero(t, got, left)
		assert.False(t, full, left)
	}
}

// TestTimeLeadingKeysFailureError pins what each failed attempt means and the message the
// operator then reads: which are retried, which resume on the next start, and the ONE that
// names the recreate as a verdict (too slow, with the whole allowance). A wrong "recreate"
// is data loss by advice, so every case that must not name it is asserted not to.
func TestTimeLeadingKeysFailureError(t *testing.T) {
	k := timeLeadingKeys[1] // measurement_events
	timing := timeLeadingKeysDefaultTiming
	progress := "already re-keyed: events"
	pg := func(code string) error { return fmt.Errorf("w: %w", &pgconn.PgError{Code: code}) }

	retried := []struct {
		phase rekeyPhase
		code  string
	}{
		{rekeyLocking, "55P03"}, {rekeyLocking, "40P01"}, {rekeyLocking, "57014"},
		{rekeyBuilding, "55P03"}, {rekeyBuilding, "40P01"},
	}
	for _, tc := range retried {
		for _, a := range []rekeyAttempt{{tc.phase, true, true}, {tc.phase, false, true}, {tc.phase, true, false}} {
			final, tooSlow := timeLeadingKeysFailureError(k, a, pg(tc.code), timing, progress)
			assert.NoErrorf(t, final, "phase %d %s is a busy table", tc.phase, tc.code)
			assert.False(t, tooSlow)
		}
	}

	// The verdict: the swap had the whole allowance and still ran out.
	final, tooSlow := timeLeadingKeysFailureError(k, rekeyAttempt{rekeyBuilding, true, true}, pg("57014"), timing, progress)
	require.Error(t, final)
	assert.True(t, tooSlow, "the too-slow verdict must stick")
	for _, want := range []string{`"event-management".measurement_events`, "uq_measurement_events_idem",
		"40s", "previous key", progress, "dcctl destroy", "dcctl bootstrap", "export",
		`COMMENT ON INDEX "event-management".uq_measurement_events_idem IS NULL`,
		"drop_chunks", chunkListQuery, "every tenant's events"} {
		assert.Contains(t, final.Error(), want)
	}
	assert.NotContains(t, final.Error(), "%!", "every verb has its argument")
	refused := fmt.Sprintf(timeLeadingKeysRefusedMessage, k.name, k.table, "marker", progress, k.name)
	for _, want := range []string{"drop_chunks", chunkListQuery, `COMMENT ON INDEX "event-management".uq_measurement_events_idem IS NULL`} {
		assert.Contains(t, refused, want)
	}
	assert.NotContains(t, refused, "%!", "every verb has its argument")
	var pgErr *pgconn.PgError
	assert.True(t, errors.As(final, &pgErr), "the server error stays wrapped")

	// The same timeout with LESS than the whole allowance is this start running out of
	// budget, not a verdict: it resumes, and never names the recreate.
	final, tooSlow = timeLeadingKeysFailureError(k, rekeyAttempt{rekeyBuilding, false, true}, pg("57014"), timing, progress)
	require.Error(t, final)
	assert.False(t, tooSlow)
	assert.Contains(t, final.Error(), "continues from here on the next start")
	assert.Contains(t, final.Error(), progress)
	assert.NotContains(t, final.Error(), "dcctl destroy")

	final, tooSlow = timeLeadingKeysFailureError(k, rekeyAttempt{rekeyBuilding, true, true}, fmt.Errorf("w: %w", errRekeyOutOfTime), timing, progress)
	require.Error(t, final)
	assert.False(t, tooSlow)
	assert.Contains(t, final.Error(), "continues from here on the next start")
	assert.NotContains(t, final.Error(), "dcctl destroy")

	for _, phase := range []rekeyPhase{rekeyLocking, rekeyBuilding} {
		final, tooSlow = timeLeadingKeysFailureError(k, rekeyAttempt{phase, true, true}, pg("53200"), timing, progress)
		require.Error(t, final)
		assert.False(t, tooSlow)
		assert.Contains(t, final.Error(), "max_locks_per_transaction")
		assert.Contains(t, final.Error(), progress)
		assert.NotContains(t, final.Error(), "dcctl destroy")
	}

	// Anything else — Timescale refusing the DDL, a lost connection, or a 57014 from a
	// statement that had NOT run for its whole timeout (an operator's pg_cancel_backend,
	// with or without the whole allowance) — is retried on the next start, and names the
	// recreate only for a failure that repeats. Never the sticky too-slow verdict.
	for _, tc := range []struct {
		attempt rekeyAttempt
		err     error
	}{
		{rekeyAttempt{rekeyBuilding, true, true}, pg("0A000")},
		{rekeyAttempt{rekeyBuilding, true, true}, errors.New("connection reset")},
		{rekeyAttempt{rekeyBuilding, true, false}, pg("57014")},
		{rekeyAttempt{rekeyBuilding, false, false}, pg("57014")},
	} {
		err := tc.err
		final, tooSlow = timeLeadingKeysFailureError(k, tc.attempt, err, timing, progress)
		require.Error(t, final)
		assert.False(t, tooSlow)
		for _, want := range []string{"uq_measurement_events_idem", "previous key", "The next start tries again",
			"on every start", "dcctl destroy", "dcctl bootstrap", "export", progress} {
			assert.Contains(t, final.Error(), want)
		}
	}
}

// TestTimeLeadingKeysHistoryMessage: the gate's refusal names the bound, the remedy, and
// what the remedy costs.
func TestTimeLeadingKeysHistoryMessage(t *testing.T) {
	msg := fmt.Sprintf(timeLeadingKeysHistoryMessage, 4000000, "events, measurement_events",
		"no table has been re-keyed yet")
	for _, want := range []string{"more than 4000000 rows", "events, measurement_events", "changed nothing",
		"dcctl destroy", "dcctl bootstrap", "Recreating discards ALL of the instance's data", "export",
		"no table has been re-keyed yet", "previous event-management keeps storing"} {
		assert.Contains(t, msg, want)
	}
}

// TestRekeyProgress: every terminal message says which tables are already on the new key.
func TestRekeyProgress(t *testing.T) {
	plans := []rekeyPlan{{timeLeadingKeys[0], rekeyDone}, {timeLeadingKeys[1], rekeySwap},
		{timeLeadingKeys[2], rekeyDropOnly}, {timeLeadingKeys[4], rekeyDone}}
	assert.Equal(t, "already re-keyed: events, event_anchors", rekeyProgress(plans))
	assert.Equal(t, "no table has been re-keyed yet", rekeyProgress(plans[1:3]))
}
