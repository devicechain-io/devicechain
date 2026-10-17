// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RuleStat is the durable per-rule firing projection that backs the console's rule-health
// view (ADR-051 slice 7b): "is my rule firing, and when did it last fire?". Each detection the
// single-writer publish loop durably publishes (runtime.Publisher, via a FireRecorder) is
// admitted to an in-memory accumulator, and ONE background worker writes the coalesced deltas
// in tenant batches (RecordFireBatch), in the order they were admitted — still one writer per
// process, so it needs no coordination. It is a COUNTING read-model, engine-inert and off the
// correctness path: the engine never reads it, and a lost or double-counted fire (a crash
// before a batch is written, a batch that failed and was discarded, or a replay re-emitting an
// already-counted detection) only skews an approximate health number, never a detection.
//
// It is deliberately NOT a Prometheus series: the engine's metrics are aggregate / no-label
// by design (the ADR-023 G.3 per-tenant/per-rule-label cardinality DoS), so per-rule stats
// live in this small durable table instead. Keyed by the composed runtime rule id (globally
// unique), with the tenant denormalized for the tenant-scoped ruleHealth read filter.
//
// Row growth: the rule id embeds "{profileToken}@{version}", so a re-publish strands the prior
// version's stat rows (never read again once that version is superseded). This mirrors the
// sibling DetectRule projection's deferred growth posture (bounding row count is the ADR-023/052
// governance concern); a supersede-time or periodic sweep is a cheap follow-up, not this slice.
type RuleStat struct {
	// RuleId is the composed runtime id "{tenant}/{profileVersionToken}/{ruleToken}" — the
	// same key DetectRule and the engine use, so a fire joins its rule row directly.
	RuleId string `gorm:"primaryKey;size:512;not null"`
	// Tenant is denormalized (functionally determined by RuleId) so the ruleHealth read can
	// filter by tenant without parsing the id — the storage-layer tenant predicate.
	Tenant string `gorm:"size:256;not null;index"`
	// LastFiredAt is the logical (event) time of the most recent fire. Advanced MONOTONICALLY
	// on upsert (GREATEST) so an out-of-order replay of an older detection never rewinds it —
	// the Slice-4c-2b replay-guard lesson applied to a counting projection.
	LastFiredAt time.Time `gorm:"not null"`
	// FireCount is the lifetime fire count. It is approximate by construction (a replay re-emits
	// and re-counts the detections since the last checkpoint), which is acceptable for a health
	// signal; a rolling-window rate is the deferred refinement (spec OQ2).
	FireCount int64 `gorm:"not null"`
	// LastEdge is the edge of the most recent fire ("raised"/"resolved", ADR-057), tracked
	// alongside LastFiredAt so the health view can show whether the rule is currently raised.
	LastEdge string `gorm:"size:16;not null"`
}

// AuditExempt opts the fire projection out of the audit journal (ADR-019): it is a derived
// counting mirror of detections the engine produced, not a control-plane entity mutation.
func (RuleStat) AuditExempt() bool { return true }

// RuleStatStore persists per-rule firing stats. Like the sibling DetectRule projection it is
// tenant-scoped at the storage callback on its plain `tenant` column: RecordFire and LoadByIDs
// each put their tenant argument in the context and the predicate is injected from there. The
// ruleHealth read used to apply that predicate by hand, which is what made a forgotten clause a
// silent cross-tenant read rather than an error.
type RuleStatStore struct {
	rdb *rdb.RdbManager
}

// NewRuleStatStore wraps the rdb manager.
func NewRuleStatStore(r *rdb.RdbManager) *RuleStatStore {
	return &RuleStatStore{rdb: r}
}

// RecordFire records one durably-published detection: it upserts the rule's stat row,
// incrementing the count and advancing last_fired_at/last_edge to the newer fire. The
// last_fired_at advance is monotonic (GREATEST) and last_edge follows only when the incoming
// fire is at least as recent, so replaying an older buffered detection cannot rewind either.
// FireCount always increments — the accepted approximation (see the type doc).
//
// It is one erasure-fence read and one upsert per fire. The detection loop no longer calls it:
// fires are coalesced off the loop (internal/rulehealth) and written with RecordFireBatch,
// which pays those two round trips once per tenant batch. It stays as the single-fire form of
// the same write, which the output-path benchmark's baseline adapter still measures.
func (s *RuleStatStore) RecordFire(ctx context.Context, ruleID, tenant string, at time.Time, edge string) error {
	stat := RuleStat{RuleId: ruleID, Tenant: tenant, LastFiredAt: at, FireCount: 1, LastEdge: edge}
	// The conflict target is rule_id alone, which cannot collide across tenants (see DetectRuleStore.Upsert).
	db := rdb.AllowTenantlessUpsert(s.rdb.DB(dccore.WithTenant(ctx, tenant)),
		"rule_id is {tenant}/... and tokens exclude '/', so it is tenant-unique by construction")
	return db.Clauses(ruleStatUpsert()).Create(&stat).Error
}

// ruleStatUpsert is RecordFire's ON CONFLICT clause. It is a function of its own so the SET
// expressions can be checked without a database (TestRuleStatUpsertQualifiesEveryColumn).
//
// Every column reference in a SET expression is QUALIFIED: `rule_stats.col` for the existing
// row, `excluded.col` for the incoming one. That is not style. Postgres rejects an unqualified
// reference as ambiguous (SQLSTATE 42702), because inside DO UPDATE both the target table and
// `excluded` are in scope and both carry every column. sqlite resolves it to the existing row
// without complaint, which is why the sqlite store tests passed while every fire failed in
// production. Since RecordFire swallows its error (off the correctness path), that failure
// showed only as a warning per fire and a rule-health view reporting zero fires forever.
// `rule_stats` is the table's own name, which both databases accept as the existing row's
// qualifier even though Postgres places the table in a schema. CASE stands in for Postgres
// GREATEST, which sqlite lacks. last_fired_at/last_edge advance only when the incoming fire is
// at least as recent, so replaying an older detection cannot rewind them; fire_count always
// increments.
func ruleStatUpsert() clause.OnConflict {
	return clause.OnConflict{
		Columns: []clause.Column{{Name: "rule_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"fire_count":    gorm.Expr("rule_stats.fire_count + 1"),
			"last_fired_at": gorm.Expr("CASE WHEN excluded.last_fired_at >= rule_stats.last_fired_at THEN excluded.last_fired_at ELSE rule_stats.last_fired_at END"),
			"last_edge":     gorm.Expr("CASE WHEN excluded.last_fired_at >= rule_stats.last_fired_at THEN excluded.last_edge ELSE rule_stats.last_edge END"),
		}),
	}
}

// RuleFireDelta is one rule's coalesced fires since it was last written: how many, and the
// newest of them. RecordFireBatch adds Count to the stored count and advances the stored last
// fire to LastFiredAt/LastEdge when it is at least as recent.
type RuleFireDelta struct {
	RuleID      string
	Count       int64
	LastFiredAt time.Time
	LastEdge    string
}

// ErrInvalidFireBatch is RecordFireBatch refusing its input before anything is sent: a delta
// with a non-positive count, an empty edge, or a rule id outside the batch's tenant. It is a
// caller bug, never a database condition, so retrying the same batch can never succeed.
var ErrInvalidFireBatch = errors.New("invalid rule-health fire batch")

// ErrFireCountOverflow is RecordFireBatch refusing a batch whose duplicate rows, summed, would
// pass the largest count a row can hold. The count is never allowed to wrap negative.
var ErrFireCountOverflow = errors.New("rule-health fire count would overflow int64")

// RecordFireBatch writes one tenant's coalesced fire deltas in ONE transaction, returning how
// many distinct rule rows it wrote.
//
//   - ONE TENANT PER CALL, bound into the context, so the production scoping and erasure-fence
//     callbacks run exactly as they do for RecordFire. The fence is read once for the whole
//     transaction (core/rdb's fence memo) rather than once per fire. A rule id whose tenant
//     prefix is not tenant is refused with ErrInvalidFireBatch: the conflict target is rule_id
//     alone, so a row of another tenant must not be able to reach this transaction at all.
//   - DUPLICATES ARE CONSOLIDATED FIRST. PostgreSQL refuses an INSERT ... ON CONFLICT that
//     would update one row twice ("command cannot affect row a second time"). The counts are
//     summed (overflow refused with ErrFireCountOverflow) and, of two entries for one rule, the
//     newer fire wins and on EQUAL times the later entry in deltas wins: the tie rule the
//     conflict update applies against the stored row, so an equal-time edge admitted later
//     replaces one admitted earlier whether the two met in memory or in the table.
//   - ROWS ARE SORTED BY RULE ID, so two transactions over overlapping rules take their row
//     locks in one order, and they are inserted with rdb.CreateChunked, which never binds more
//     than the wire protocol's parameter limit in one statement.
//   - The update is ADDITIVE and MONOTONIC (ruleStatBatchUpsert): fire_count adds the delta,
//     and last_fired_at/last_edge only move forward.
//
// It is all or nothing. A failed or timed-out call may or may not have committed, so a caller
// must not retry it blindly: replaying an ambiguous commit would add its counts twice. The
// processor discards a failed batch and counts the fires it held (internal/rulehealth).
func (s *RuleStatStore) RecordFireBatch(ctx context.Context, tenant string, deltas []RuleFireDelta) (int, error) {
	rows, err := consolidateFireDeltas(tenant, deltas)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	err = s.rdb.DB(dccore.WithTenant(ctx, tenant)).Transaction(func(tx *gorm.DB) error {
		up := rdb.AllowTenantlessUpsert(tx.Clauses(ruleStatBatchUpsert()),
			"rule_id is {tenant}/... and tokens exclude '/', so it is tenant-unique by construction; "+
				"RecordFireBatch refuses a rule id outside the batch's tenant before anything is sent")
		return rdb.CreateChunked(up, &rows).Error
	})
	if err != nil {
		return 0, err
	}
	return len(rows), nil
}

// consolidateFireDeltas validates deltas, merges duplicate rule ids in order (the later entry
// wins an equal-time tie) and returns the rows sorted by rule id.
func consolidateFireDeltas(tenant string, deltas []RuleFireDelta) ([]RuleStat, error) {
	if len(deltas) == 0 {
		return nil, nil
	}
	if tenant == "" {
		return nil, fmt.Errorf("%w: no tenant", ErrInvalidFireBatch)
	}
	prefix := tenant + "/"
	byID := make(map[string]int, len(deltas))
	rows := make([]RuleStat, 0, len(deltas))
	for _, d := range deltas {
		if d.Count <= 0 {
			return nil, fmt.Errorf("%w: rule %q has count %d", ErrInvalidFireBatch, d.RuleID, d.Count)
		}
		if d.LastEdge == "" {
			return nil, fmt.Errorf("%w: rule %q has no edge", ErrInvalidFireBatch, d.RuleID)
		}
		if !strings.HasPrefix(d.RuleID, prefix) || len(d.RuleID) == len(prefix) {
			return nil, fmt.Errorf("%w: rule %q is not tenant %q's", ErrInvalidFireBatch, d.RuleID, tenant)
		}
		i, dup := byID[d.RuleID]
		if !dup {
			byID[d.RuleID] = len(rows)
			rows = append(rows, RuleStat{RuleId: d.RuleID, Tenant: tenant, FireCount: d.Count,
				LastFiredAt: d.LastFiredAt, LastEdge: d.LastEdge})
			continue
		}
		r := &rows[i]
		if r.FireCount > math.MaxInt64-d.Count {
			return nil, fmt.Errorf("%w: rule %q", ErrFireCountOverflow, d.RuleID)
		}
		r.FireCount += d.Count
		if !d.LastFiredAt.Before(r.LastFiredAt) {
			r.LastFiredAt, r.LastEdge = d.LastFiredAt, d.LastEdge
		}
	}
	sort.Slice(rows, func(a, b int) bool { return rows[a].RuleId < rows[b].RuleId })
	return rows, nil
}

// ruleStatBatchUpsert is RecordFireBatch's ON CONFLICT clause: ruleStatUpsert with the count
// ADDED rather than incremented. Every column reference is qualified, for the reason
// ruleStatUpsert gives (TestRuleStatUpsertQualifiesEveryColumn checks both clauses). The sum is
// a bigint addition, so on PostgreSQL a stored count that would overflow fails the statement
// ("bigint out of range") rather than wrapping, and the batch is discarded like any failure.
func ruleStatBatchUpsert() clause.OnConflict {
	return clause.OnConflict{
		Columns: []clause.Column{{Name: "rule_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"fire_count":    gorm.Expr("rule_stats.fire_count + excluded.fire_count"),
			"last_fired_at": gorm.Expr("CASE WHEN excluded.last_fired_at >= rule_stats.last_fired_at THEN excluded.last_fired_at ELSE rule_stats.last_fired_at END"),
			"last_edge":     gorm.Expr("CASE WHEN excluded.last_fired_at >= rule_stats.last_fired_at THEN excluded.last_edge ELSE rule_stats.last_edge END"),
		}),
	}
}

// LoadByIDs returns the stats for a set of rule ids as a map keyed by rule id (a missing id
// simply has no entry — a rule that has never fired). It filters by tenant as well as id as
// defense-in-depth, though the ids are already this tenant's (they come from a tenant-scoped
// DetectRule read). An empty id set is a no-op.
func (s *RuleStatStore) LoadByIDs(ctx context.Context, tenant string, ids []string) (map[string]RuleStat, error) {
	out := make(map[string]RuleStat, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var stats []RuleStat
	if err := s.rdb.DB(dccore.WithTenant(ctx, tenant)).Where("rule_id IN ?", ids).Find(&stats).Error; err != nil {
		return nil, err
	}
	for _, st := range stats {
		out[st.RuleId] = st
	}
	return out, nil
}
