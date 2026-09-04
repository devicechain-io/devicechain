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
	"state_change_events",
}

// LocationHypertable is the one hypertable holding device POSITIONS, and the only
// member of LifecycleHypertables that can carry a retention window of its own (see
// DataLifecyclePolicy.LocationRetentionDays).
const LocationHypertable = "location_events"

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

// compressSegmentBy is the compress_segmentby column list for one hypertable.
//
// WHAT SEGMENTBY BUYS. In a compressed chunk a segmentby column stays a PLAIN,
// indexed column on the compressed row; every other column is packed inside the
// compressed blob. So a predicate on a segmentby column selects which batches to
// decompress, and a predicate on any other column can only be applied AFTER
// decompressing. Segmenting by tenant_id alone therefore makes a device-scoped read
// decompress the whole tenant's chunk and then discard all but one device's rows —
// read amplification that grows with the fleet, on exactly the query the product is
// for ("this device, this window").
//
// 🔴 WHAT IT COSTS IS NOT "COMPRESSES SOMEWHAT WORSE". BELOW A THRESHOLD IT IS NET
// EXPANSION, AND THE MEASURED CURVE IS HERE SO THE NEXT PERSON CHOOSING A KEY HAS THE
// NUMBER RATHER THAN THE INTUITION. Rows are packed into batches of up to 1000 WITHIN
// a segment and every segment pays its own per-row overhead, so a segment holding a
// handful of rows per chunk spends more on overhead than it saves on packing.
// Measured on TimescaleDB 2.28.3 — 10k devices, one tenant, one 24h chunk (the
// DataLifecyclePolicy.ChunkIntervalHours default), identical rows under both layouts:
//
//	rows/device/chunk   'tenant_id'   'tenant_id, device_token'
//	                1        3.38×      0.33×  <- compression TRIPLES the size
//	                4        3.82×      0.93×  <- still net expansion
//	               12        3.91×      2.04×
//	               24        3.94×      3.21×
//	               96        3.97×      4.15×
//	             1440        3.99×      5.08×
//
// Break-even is around 60–90 rows per device per chunk. Below it the engine says so
// itself — `WARNING: poor compression ratio detected for chunk ... 0.34 ... HINT:
// Changing compression settings can improve compression rate` — and then compresses
// anyway, because the compression policy fires unattended at 7 days on every chunk.
// At 100k devices × 1 row/chunk that is 13.4 MB of data stored as 39.6 MB.
//
// 🔴 SO THE PRINCIPLE GOVERNING THIS MAP IS AN ASYMMETRY, NOT A PREFERENCE FOR ONE
// KEY: A READ THAT IS SLOWER IS VISIBLE AND TUNABLE; STORAGE THAT SILENTLY GROWS
// UNDER THE NAME "COMPRESSION" IS NEITHER. A slow read shows up in a query timing and
// the operator can act on it. An expanding disk surfaces as nothing until it is full,
// the direction of the failure is the opposite of what the word "compression"
// promises, and the operator's only lever is instance-wide (the chunk interval).
// Where a table is plausibly below break-even, prefer the tenant-only key even though
// the device-keyed one would read faster.
//
// 🔴 tenant_id IS PRESENT IN EVERY LIST, AND PRESENCE — NOT POSITION — IS WHAT THE
// ERASURE MEASUREMENT RESTS ON. The tenant-erasure cost measurement (ADR-077,
// tenant_purge_cost_test.go) rests on `DELETE ... WHERE tenant_id` matching WHOLE
// compressed rows, which holds while tenant_id is A segmentby column: a predicate
// over segmentby columns alone never has to decompress, and it does not have to name
// all of them. Measured rather than reasoned — with segmentby 'device_token,
// tenant_id' (tenant SECOND) a delete of 20k compressed rows still reports `Batches
// scanned: 10000, Batches deleted: 10000` and decompresses nothing, exactly as with
// tenant first. DROPPING tenant_id out of a list is therefore the change that would
// silently turn the cheap case into the worst one and reopen the schema-per-tenant
// alternative ADR-077 rejected on exactly that evidence.
//
// tenant_id nevertheless LEADS every list, for a second and narrower reason.
// Timescale auto-creates a btree on the compressed chunk over the segmentby columns
// followed by the orderby metadata — (tenant_id, device_token, _ts_meta_...) — and a
// btree serves only a predicate over a PREFIX of its columns. Every read here is
// tenant-scoped and not all of them are device-scoped, so leading with tenant_id is
// what keeps that index usable by the tenant-only reads too.
// TestTenantIdLeadsEverySegmentBy pins the order for that reason.
//
// 🔴 A SEGMENTBY COLUMN MUST NOT ALSO APPEAR IN compress_orderby. Measured on
// TimescaleDB 2.28.3, not inferred: the ALTER fails with `cannot use column
// "occurred_time" for both ordering and segmenting`. The orderby is occurred_time
// alone, which appears in no list below, and TestSegmentByNeverCollidesWithOrderBy
// pins that it never starts to.
//
// Every governed hypertable is listed, INCLUDING the ones whose answer equals the
// tenant-only fallback, because a listed table has had its key decided and an absent
// one has merely inherited it. TestEverySegmentByIsDeclared makes a newly governed
// hypertable fail a test rather than quietly inherit the fallback.
var compressSegmentBy = map[string]string{
	// SAMPLED STREAMS — segmented per device. These are the tables a reporting device
	// writes to repeatedly: a device emitting telemetry at all clears the break-even
	// above at any interval a real deployment uses (a 24h chunk needs one row every
	// ~20 minutes). They are read through commonEventFilters, whose only non-time
	// predicate is device_token (event_type is a coarse IN-list, too low-cardinality
	// to be worth a segment of its own). events additionally carries an index leading
	// with exactly these two columns.
	"events":          "tenant_id, device_token",
	"location_events": "tenant_id, device_token",
	// measurement_events is read both directly and, for bucketed aggregation, on the
	// (tenant_id, device_token, name, occurred_time DESC) index; the
	// measurement_rollups continuous aggregate groups by the same device key. `name`
	// is deliberately NOT segmented on: it would multiply the segment count by the
	// metric vocabulary, and it is a predicate only on the aggregation path, which
	// reads the rollup rather than the raw hypertable whenever it can.
	"measurement_events": "tenant_id, device_token",

	// 🔴 SPARSE BY NATURE — tenant-only, DELIBERATELY DIFFERENT FROM THE THREE ABOVE,
	// and the reason is the table's own semantics rather than any fleet size. An
	// alert and a presence transition are events that BY DEFINITION do not occur
	// continuously: a device that alerts or flips presence 60–90 times a day is a
	// device in trouble, not a device working normally. So these tables sit below the
	// break-even for essentially every realistic deployment, where the per-device key
	// would not compress them less well — it would grow them.
	//
	// What that costs, stated rather than glossed: alert_events IS read by device
	// (through commonEventFilters), so this key gives up the batch selectivity there
	// and accepts the read amplification, on the asymmetry above. state_change_events
	// gives up nothing today — it has NO reader at all, being the write-only presence
	// history; its (tenant_id, device_token, occurred_time DESC) index anticipates a
	// presence-timeline reader that does not exist yet, and when it arrives THAT is
	// the moment to re-measure this table's write density rather than assume it.
	//
	// Do NOT "make these consistent" with the three above without a measurement that
	// moves the curve; TestSparseTablesAreNotSegmentedPerDevice is what such an edit
	// has to argue with.
	"alert_events":        "tenant_id",
	"state_change_events": "tenant_id",

	// 🔴 event_anchors is the one table NOT keyed by device, and segmenting it by
	// device_token would have been the wrong answer reached by pattern-matching. Its
	// reason to exist is the anchor lookup — "every event anchored to this customer /
	// area / asset" — which filters (anchor_type, anchor_token) and projects event_id.
	// device_token is on the table but is read only by the periodic reconciliation
	// sweep's `SELECT DISTINCT device_token`, which decompresses to answer it — as it
	// did under the tenant-only key too, since device_token was no more a segmentby
	// column then than it is now. This key changes what the anchor LOOKUP costs; it
	// leaves that sweep exactly where it was.
	"event_anchors": "tenant_id, anchor_type, anchor_token",
}

// compressOrderBy is the compress_orderby setting shared by every hypertable: all six
// are partitioned on occurred_time and all are read as descending time ranges.
const compressOrderBy = "occurred_time DESC"

// segmentByFor resolves a hypertable's segmentby list, falling back to the tenant-only
// setting for anything undeclared (see compressSegmentBy).
func segmentByFor(table string) string {
	if cols, ok := compressSegmentBy[table]; ok {
		return cols
	}
	return "tenant_id"
}

// enableCompressionStmt turns on columnar compression and sets the segment/order keys.
// Ordering by occurred_time DESC matches the time-range scans; the segment key is
// per-table (see compressSegmentBy). Re-running with identical settings is a no-op.
//
// 🔴 HOW A CHANGED SEGMENT KEY ACTUALLY LANDS, MEASURED ON TimescaleDB 2.28.3 RATHER
// THAN ASSUMED. The widely-repeated version of this is "you must decompress the chunks
// first, so the ALTER fails". That is NOT what 2.28.3 does. With compressed chunks
// present the ALTER SUCCEEDS and reports:
//
//	NOTICE: updated compression settings will only apply to future compressions
//	DETAIL: Existing compressed chunks will not be recompressed.
//	HINT:   Use compress_chunk(chunk, recompress => true) to recompress.
//
// So applyOne, which runs this on every startup, converges NEW chunks on any instance —
// there is no "already enabled, skip" branch to defeat it, and no error to swallow. What
// does not converge is history: chunks compressed under the old key keep the old layout
// until they are recompressed by hand or dropped by retention, and the compression
// POLICY does not relayout an already-compressed chunk. An instance therefore reads fast
// for new data and at the old amplification for old data, silently and with nothing
// broken — which is exactly why moving these lists is cheapest before there is any
// history, and why after GA it is a planned recompression rather than a config edit.
func enableCompressionStmt(table string) string {
	return fmt.Sprintf("ALTER TABLE %s SET (timescaledb.compress, "+
		"timescaledb.compress_segmentby = '%s', "+
		"timescaledb.compress_orderby = '%s')",
		qualifiedHypertable(table), segmentByFor(table), compressOrderBy)
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

// DataLifecyclePolicy is the resolved set of TimescaleDB lifecycle windows to
// reconcile (ADR-026). It is built from configuration in main and passed whole so
// that adding a per-table window is a field rather than another positional int.
type DataLifecyclePolicy struct {
	// ChunkIntervalHours sizes future chunks on every hypertable.
	ChunkIntervalHours int

	// CompressAfterDays is the compression window applied uniformly to every
	// hypertable; 0 disables the compression policy. It is deliberately NOT
	// per-table: compression is lossless, so it changes how the rows are stored and
	// never whether they are still held. Only retention answers "for how long".
	CompressAfterDays int

	// RetentionDays is the uniform retention window across every hypertable; 0 (the
	// default) means retention is off and nothing is ever dropped.
	RetentionDays int

	// LocationRetentionDays optionally overrides RetentionDays for location_events
	// ALONE. nil means "no override" — location ages out on the uniform window, so
	// an operator who never sets it sees exactly the previous behavior.
	//
	// The reason it exists is regulatory rather than technical. A vehicle track, or
	// a lone-worker's badge trail, is personal data in a way a temperature series is
	// not: it is about a PERSON, and the lawful basis for holding it is usually
	// narrower and shorter than the basis for holding telemetry. With one uniform
	// window a tenant asked "how long do you keep position data?" can only answer by
	// quoting the window it keeps everything for — and the only lever it has is to
	// delete everything or nothing. That is the first question a data-protection
	// review asks, and "we shorten it for locations" is the expected answer.
	//
	// An explicit 0 is meaningful and distinct from nil: it turns retention OFF for
	// location while leaving it on elsewhere. That direction is unusual but it is a
	// real configuration (a tenant under a legal hold on positions), so the pointer
	// carries "unset" rather than overloading 0 to mean it.
	//
	// What it drops is the COORDINATES: location_events is the payload hypertable, and
	// the base `events` row for the same reading ages out on the uniform window. So a
	// shorter location window removes where the device was while leaving the fact that
	// it reported a position at that time. That is the intended split — the position is
	// the personal data — but state it that way to an auditor rather than claiming the
	// reading vanishes entirely.
	//
	// 🔴 PER-DEVICE (and per-subject) ERASURE IS DELIBERATELY OUT OF SCOPE HERE, and
	// this is the place someone will come looking for it. Retention is a per-chunk
	// policy: TimescaleDB drops a whole chunk once every row in it is older than the
	// window, which is why it is cheap. A targeted `DELETE ... WHERE device_token =`
	// is the opposite shape — against compressed chunks it is a decompress, rewrite
	// and recompress maintenance operation, not a query, with a cost set by the
	// chunk's size rather than by the number of rows being erased. It needs its own
	// design (a batched, resumable, rate-limited job with its own request/audit
	// record), and squeezing it in behind a config knob would produce an erasure
	// promise the storage engine cannot keep under load. It is an unbuilt feature,
	// not an oversight.
	//
	// TENANT-level deletion is already covered and needs nothing here: the tenant
	// purge is catalog-driven and sweeps every table carrying a tenant_id column, so
	// location_events is included by construction rather than by being listed.
	LocationRetentionDays *int
}

// retentionDaysFor resolves the retention window for one hypertable: the location
// override when the table is location_events AND an override is set, the uniform
// window otherwise. Kept as a pure method so both directions — override applied,
// override absent — are assertable without a database.
func (p DataLifecyclePolicy) retentionDaysFor(table string) int {
	if table == LocationHypertable && p.LocationRetentionDays != nil {
		return *p.LocationRetentionDays
	}
	return p.RetentionDays
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
	policy DataLifecyclePolicy) error {
	compressionEnabled := policy.CompressAfterDays > 0

	return mgr.WithAdvisoryLock(ctx, rdb.AdvisoryLockKey(lifecycleLockName), func() error {
		db := mgr.DB(core.WithSystemContext(ctx))
		for _, table := range LifecycleHypertables {
			if err := applyOne(db, table, policy); err != nil {
				// The only error applyOne returns is a failed retention-policy removal
				// while retention is disabled — a data-safety-critical case (a lingering
				// job would keep dropping data against explicit operator intent). Fail
				// closed: refuse to start rather than silently keep deleting.
				return err
			}
		}
		event := log.Info().
			Int("chunkIntervalHours", policy.ChunkIntervalHours).
			Bool("compressionEnabled", compressionEnabled).
			Int("compressAfterDays", policy.CompressAfterDays).
			Bool("retentionEnabled", policy.RetentionDays > 0).
			Int("retentionDays", policy.RetentionDays).
			Int("hypertables", len(LifecycleHypertables))
		// Logged only when an override is in force, so the absence of the field means
		// "location ages out with everything else" rather than "unknown".
		if policy.LocationRetentionDays != nil {
			event = event.Int("locationRetentionDays", *policy.LocationRetentionDays)
		}
		event.Msg("Applied TimescaleDB data-lifecycle policies (ADR-026).")
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
//
// The retention window is resolved PER TABLE (retentionDaysFor), so location_events
// can age out on a shorter window than the telemetry beside it; every other policy
// is uniform.
func applyOne(db *gorm.DB, table string, policy DataLifecyclePolicy) error {
	compressAfterDays := policy.CompressAfterDays
	compressionEnabled := compressAfterDays > 0
	retentionDays := policy.retentionDaysFor(table)
	retentionEnabled := retentionDays > 0

	exec := func(step, stmt string, args ...interface{}) {
		if err := db.Exec(stmt, args...).Error; err != nil {
			log.Error().Err(err).Str("hypertable", table).Str("step", step).
				Msg("Failed to apply data-lifecycle policy statement; continuing.")
		}
	}

	// Chunk sizing.
	exec("chunk-interval", setChunkIntervalStmt(table), policy.ChunkIntervalHours)

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
