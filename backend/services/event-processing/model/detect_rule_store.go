// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DetectRuleStore is the durable projection of the DETECT rule set (ADR-051 slice 4b-3).
// The published-rule fact consumer upserts each fact's rules here BEFORE it acks the fact,
// and the engine's rule set is rebuilt from LoadAll at startup — so a rule survives a
// restart independent of the finite-retention fact stream. Rows are keyed by the composed
// runtime rule id, so a re-published or redelivered fact overwrites in place (last-wins,
// idempotent).
type DetectRuleStore struct {
	rdb *rdb.RdbManager
}

// NewDetectRuleStore wraps the rdb manager.
func NewDetectRuleStore(r *rdb.RdbManager) *DetectRuleStore {
	return &DetectRuleStore{rdb: r}
}

// Upsert persists a batch of rules (one published-rule fact's worth) in a single
// transaction, overwriting any existing row for the same rule id. It is idempotent, so a
// redelivered fact is a no-op replay. An empty batch is a no-op. The mutable columns are
// updated on conflict — the definition AND the group scope (ADR-062 S4): a deleted-and-reused
// profile token can re-mint an existing rule id with a DIFFERENT scope (the same token-reuse
// case the definition update exists for), and a stale projected scope would reload the wrong
// membership set after a restart. tenant/profileVersionToken/ruleToken are functionally
// determined by the id, so they never change for a given id.
func (s *DetectRuleStore) Upsert(ctx context.Context, rules []DetectRule) error {
	if len(rules) == 0 {
		return nil
	}
	return s.rdb.DB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "rule_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"definition", "entity_group_token", "entity_group_version", "updated_at"}),
	}).Create(&rules).Error
}

// LoadAll returns every persisted rule — the full set the engine's rule registry is rebuilt
// from at startup. It is not tenant-scoped (the projection spans every tenant, tenant on the
// row), so it reads the whole table.
func (s *DetectRuleStore) LoadAll(ctx context.Context) ([]DetectRule, error) {
	var rules []DetectRule
	if err := s.rdb.DB(ctx).Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

// LoadByProfileVersion returns the projected (published, enabled) rules for one profile version
// — the rules the engine currently runs for that "{profileToken}@{version}" scope. The console's
// rule-health view resolves a profile's ACTIVE version token (via the ProfileActive projection)
// and reads its live rule set here. Tenant-filtered explicitly (the projection spans tenants).
func (s *DetectRuleStore) LoadByProfileVersion(ctx context.Context, tenant, profileVersionToken string) ([]DetectRule, error) {
	var rules []DetectRule
	err := s.rdb.DB(ctx).
		Where("tenant = ? AND profile_version_token = ?", tenant, profileVersionToken).
		Find(&rules).Error
	if err != nil {
		return nil, err
	}
	return rules, nil
}

// LoadByID returns the single rule row for a composed runtime rule id, or ok=false when no such
// rule is persisted. It is the REACT dispatcher's resolution path (ADR-051 slice 5b): a derived
// event carries the rule id that fired, and the dispatcher reads the authoritative rule Definition
// (its action chain + severity) from this durable projection — the same source of truth the engine
// rebuilds from — rather than from the wire event, so an action-chain edit takes effect without
// re-publishing events. The id is the primary key, so this is a point read. A store error is
// returned (the caller must NOT treat a transient read failure as "rule gone" and drop the action).
func (s *DetectRuleStore) LoadByID(ctx context.Context, ruleID string) (*DetectRule, bool, error) {
	var rule DetectRule
	err := s.rdb.DB(ctx).Where("rule_id = ?", ruleID).Take(&rule).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &rule, true, nil
}
