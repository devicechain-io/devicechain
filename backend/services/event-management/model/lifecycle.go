// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// lifecycleSchema is the Postgres schema all event hypertables live in.
const lifecycleSchema = "event-management"

// LifecycleHypertables are the event-management hypertables governed by the
// TimescaleDB data-lifecycle policies (ADR-026): the base events table, the three
// payload hypertables, and event_anchors. All are partitioned on occurred_time and
// carry tenant_id, so one uniform policy set (chunk sizing / compression /
// retention) applies to every one.
//
// event_anchors MUST be governed alongside the events it indexes: its own migration
// states its rows should "age out alongside the events they index", but without a
// retention policy they never do — so dropping an old events chunk while leaving its
// anchor rows would orphan them and grow that hypertable unbounded. Governing it
// here ages the anchor rows out on the same window as the events.
var LifecycleHypertables = []string{
	"events", "location_events", "measurement_events", "alert_events", "event_anchors",
}

// lifecycleLockName namespaces the startup advisory lock that serializes policy
// reconciliation across concurrently-rolling replicas (distinct from the migration
// lock, so the two never block each other).
const lifecycleLockName = "event-management-lifecycle"

// qualifiedHypertable renders the schema-qualified, double-quoted identifier for a
// hypertable (e.g. "event-management"."events"). The table always comes from the
// LifecycleHypertables allowlist, never from user input.
func qualifiedHypertable(table string) string {
	return fmt.Sprintf("%q.%q", lifecycleSchema, table)
}

// The lifecycle statements are built as pure functions so their exact shape is
// unit-testable without a live database. The hypertable identifier is embedded (it
// is an allowlisted constant); the interval magnitude is passed as a bound `?`
// parameter. TimescaleDB's policy helpers take the hypertable as a regclass, which
// accepts the quoted identifier as a string literal.

// setChunkIntervalStmt sizes future chunks. Idempotent: re-running with the same
// value is a no-op, and it never rewrites existing chunks.
func setChunkIntervalStmt(table string) string {
	return fmt.Sprintf("SELECT set_chunk_time_interval('%s'::regclass, make_interval(hours => ?))",
		qualifiedHypertable(table))
}

// enableCompressionStmt turns on columnar compression and sets the segment/order
// keys. Segmenting by tenant_id keeps a tenant's rows grouped in a compressed batch
// (every read is tenant-scoped), and ordering by occurred_time DESC matches the
// time-range scans. Re-running with identical settings is a no-op.
func enableCompressionStmt(table string) string {
	return fmt.Sprintf("ALTER TABLE %s SET (timescaledb.compress, "+
		"timescaledb.compress_segmentby = 'tenant_id', "+
		"timescaledb.compress_orderby = 'occurred_time DESC')",
		qualifiedHypertable(table))
}

// removeCompressionPolicyStmt drops the compression policy if present (if_exists =>
// true makes it a no-op when absent), so reconciliation converges when compression
// is disabled or its window changes.
func removeCompressionPolicyStmt(table string) string {
	return fmt.Sprintf("SELECT remove_compression_policy('%s'::regclass, if_exists => true)",
		qualifiedHypertable(table))
}

// addCompressionPolicyStmt schedules compression of chunks older than the given
// interval.
func addCompressionPolicyStmt(table string) string {
	return fmt.Sprintf("SELECT add_compression_policy('%s'::regclass, make_interval(days => ?))",
		qualifiedHypertable(table))
}

// removeRetentionPolicyStmt drops the retention policy if present. It runs
// unconditionally every reconcile so that setting retention back to off (0) removes
// a previously-added policy.
func removeRetentionPolicyStmt(table string) string {
	return fmt.Sprintf("SELECT remove_retention_policy('%s'::regclass, if_exists => true)",
		qualifiedHypertable(table))
}

// addRetentionPolicyStmt schedules dropping of chunks whose data is older than the
// given interval. Only run when retention is explicitly enabled (> 0 days).
func addRetentionPolicyStmt(table string) string {
	return fmt.Sprintf("SELECT add_retention_policy('%s'::regclass, make_interval(days => ?))",
		qualifiedHypertable(table))
}

// ApplyDataLifecyclePolicies reconciles the TimescaleDB chunk-sizing, compression,
// and retention policies (ADR-026) across every event hypertable, to the values
// resolved from configuration. It is idempotent — safe to run on every startup —
// and converges the live policies to the config: disabling compression or retention
// removes any policy previously added.
//
// The whole reconcile is serialized with a service-scoped advisory lock so that
// concurrently-rolling replicas do not race on the remove-then-add of a policy job.
// It runs under a system context (these are admin DDL/catalog operations, not
// tenant-scoped row access) after migrations have created the hypertables.
//
// Policy application is best-effort with loud logging: a single failing statement
// is logged at ERROR and reconciliation continues to the next hypertable, so a
// transient TimescaleDB hiccup degrades the lifecycle guarantees rather than taking
// down ingest. Callers should treat a returned error as "could not even attempt"
// (e.g. the advisory lock could not be acquired), not "a policy failed".
func ApplyDataLifecyclePolicies(ctx context.Context, mgr *rdb.RdbManager,
	chunkIntervalHours, compressAfterDays, retentionDays int) error {
	compressionEnabled := compressAfterDays > 0
	retentionEnabled := retentionDays > 0

	return mgr.WithAdvisoryLock(ctx, rdb.AdvisoryLockKey(lifecycleLockName), func() error {
		db := mgr.DB(core.WithSystemContext(ctx))
		for _, table := range LifecycleHypertables {
			if err := applyOne(db, table, chunkIntervalHours, compressAfterDays,
				compressionEnabled, retentionDays, retentionEnabled); err != nil {
				// The only error applyOne returns is a failed retention-policy removal
				// while retention is disabled — a data-safety-critical case (a lingering
				// job would keep dropping data against explicit operator intent). Fail
				// closed: refuse to start rather than silently keep deleting.
				return err
			}
		}
		log.Info().
			Int("chunkIntervalHours", chunkIntervalHours).
			Bool("compressionEnabled", compressionEnabled).
			Int("compressAfterDays", compressAfterDays).
			Bool("retentionEnabled", retentionEnabled).
			Int("retentionDays", retentionDays).
			Int("hypertables", len(LifecycleHypertables)).
			Msg("Applied TimescaleDB data-lifecycle policies (ADR-026).")
		return nil
	})
}

// applyOne reconciles all three policies for a single hypertable, logging and
// continuing past any individual statement failure so one bad table never aborts
// the rest. Ordering matters: the compression setting must be enabled before its
// policy is added, and each policy is removed before a conditional re-add so the
// live state converges to the config (disabled → removed, changed window → replaced).
// applyOne returns a non-nil error ONLY for the data-safety-critical case: a failed
// retention-policy removal while retention is disabled (see below). Every other
// statement is best-effort — logged at ERROR and skipped — because chunk sizing and
// compression only degrade a guarantee, they never destroy data.
func applyOne(db *gorm.DB, table string, chunkIntervalHours, compressAfterDays int,
	compressionEnabled bool, retentionDays int, retentionEnabled bool) error {
	exec := func(step, stmt string, args ...interface{}) {
		if err := db.Exec(stmt, args...).Error; err != nil {
			log.Error().Err(err).Str("hypertable", table).Str("step", step).
				Msg("Failed to apply data-lifecycle policy statement; continuing.")
		}
	}

	// Chunk sizing.
	exec("chunk-interval", setChunkIntervalStmt(table), chunkIntervalHours)

	// Compression: reconcile the policy (remove first so a changed window replaces it),
	// then enable + (re-)add when compression is on. Compression is lossless, so a
	// failed remove/add only leaves a stale window — best-effort is the right posture.
	exec("compression-policy-remove", removeCompressionPolicyStmt(table))
	if compressionEnabled {
		exec("compression-enable", enableCompressionStmt(table))
		exec("compression-policy-add", addCompressionPolicyStmt(table), compressAfterDays)
	}

	// Retention. Removing any existing policy converges the disable path and lets a
	// changed window be re-added cleanly.
	if retentionEnabled {
		// Best-effort: the add re-establishes intent, and any lingering old policy is
		// still retaining data, not destroying it beyond the operator's opt-in.
		exec("retention-policy-remove", removeRetentionPolicyStmt(table))
		exec("retention-policy-add", addRetentionPolicyStmt(table), retentionDays)
		return nil
	}
	// Disable path: this is the one removal whose failure has irreversible
	// consequences — a previously-added retention job would keep calling drop_chunks
	// and deleting telemetry against the operator's explicit intent to stop. Fail
	// closed. (When retention was never enabled, if_exists => true makes this a
	// succeeding no-op, so fresh installs never fail here.)
	if err := db.Exec(removeRetentionPolicyStmt(table)).Error; err != nil {
		return fmt.Errorf("remove retention policy on %q while retention is disabled "+
			"(a previously-added policy may still be dropping data): %w", table, err)
	}
	return nil
}
