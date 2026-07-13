// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RuleStat is the durable per-rule firing projection that backs the console's rule-health
// view (ADR-051 slice 7b): "is my rule firing, and when did it last fire?". It is written
// INLINE by the single-writer publish loop (runtime.Publisher, via a FireRecorder) as each
// detection is durably published — so it is naturally ordered (one writer) and needs no
// coordination. It is a COUNTING read-model, engine-inert and off the correctness path: the
// engine never reads it, and a lost or double-counted fire (a crash between publish and this
// write, or a replay re-emitting an already-counted detection) only skews an approximate
// health number, never a detection.
//
// It is deliberately NOT a Prometheus series: the engine's metrics are aggregate / no-label
// by design (the ADR-023 G.3 per-tenant/per-rule-label cardinality DoS), so per-rule stats
// live in this small durable table instead. Keyed by the composed runtime rule id (globally
// unique), with the tenant denormalized for the tenant-scoped ruleHealth read filter.
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
// NOT tenant-scoped at the storage callback (the projection spans every tenant, tenant on the
// row); the ruleHealth read applies the tenant predicate explicitly.
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
// FireCount always increments — the accepted approximation (see the type doc). This runs on
// the publish hot path; the caller (the FireRecorder) swallows the returned error since a
// failed stat write must never wedge the correctness-bearing publish/checkpoint loop.
func (s *RuleStatStore) RecordFire(ctx context.Context, ruleID, tenant string, at time.Time, edge string) error {
	stat := RuleStat{RuleId: ruleID, Tenant: tenant, LastFiredAt: at, FireCount: 1, LastEdge: edge}
	// The SET expressions are written dialect-portably (Postgres in production, sqlite in the
	// store tests): an UNqualified column refers to the existing row and `excluded.col` to the
	// incoming one — both databases agree on that — and CASE stands in for Postgres GREATEST,
	// which sqlite lacks. last_fired_at/last_edge advance only when the incoming fire is at least
	// as recent, so replaying an older detection cannot rewind them; fire_count always increments.
	return s.rdb.DB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "rule_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"fire_count":    gorm.Expr("fire_count + 1"),
			"last_fired_at": gorm.Expr("CASE WHEN excluded.last_fired_at >= last_fired_at THEN excluded.last_fired_at ELSE last_fired_at END"),
			"last_edge":     gorm.Expr("CASE WHEN excluded.last_fired_at >= last_fired_at THEN excluded.last_edge ELSE last_edge END"),
		}),
	}).Create(&stat).Error
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
	if err := s.rdb.DB(ctx).Where("tenant = ? AND rule_id IN ?", tenant, ids).Find(&stats).Error; err != nil {
		return nil, err
	}
	for _, st := range stats {
		out[st.RuleId] = st
	}
	return out, nil
}
