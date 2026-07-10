// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/rdb"
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
// redelivered fact is a no-op replay. An empty batch is a no-op. The definition column is
// updated on conflict (tenant/profileVersionToken/ruleToken are functionally determined by
// the id, so they never change for a given id).
func (s *DetectRuleStore) Upsert(ctx context.Context, rules []DetectRule) error {
	if len(rules) == 0 {
		return nil
	}
	return s.rdb.DB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "rule_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"definition", "updated_at"}),
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
