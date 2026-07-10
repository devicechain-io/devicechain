// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm/clause"
)

// ProfileActiveStore is the durable projection of which published version is active per stable
// profile token (ADR-051 slice 4c-2b). The published-rule fact consumer upserts it alongside the
// rule projection — last-fact-wins, so the newest publish (or rollback re-emit) names the active
// version and its publish time. The engine's dead-man arming reads it at arming time to resolve a
// roster entry's stable profile token to the active version's absence rules and their grace base.
type ProfileActiveStore struct {
	rdb *rdb.RdbManager
}

// NewProfileActiveStore wraps the rdb manager.
func NewProfileActiveStore(r *rdb.RdbManager) *ProfileActiveStore {
	return &ProfileActiveStore{rdb: r}
}

// Upsert records the active published version for a profile token. It is MONOTONIC on the publish
// time: it overwrites the row only when the incoming fact's PublishedAt is at least as new as the
// recorded one (the DO UPDATE WHERE guard). A newer publish and a rollback re-emit (which carries a
// FRESH, later publish time) both win; a stale fact redelivered after its successor — reachable
// because an unacked fact does not block newer ones and a failed ack is tolerated — is a no-op
// rather than a durable regression to an older active version. Idempotent for an exact redelivery
// (equal PublishedAt rewrites identical values). A zero PublishedAt (a pre-4c-2a fact without the
// field) can insert but never overwrites a real publish time, and is clamped downstream at arming.
func (s *ProfileActiveStore) Upsert(ctx context.Context, active *ProfileActive) error {
	return s.rdb.DB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant"}, {Name: "profile_token"}},
		DoUpdates: clause.AssignmentColumns([]string{"active_version_token", "published_at", "updated_at"}),
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "profile_actives.published_at <= excluded.published_at"},
		}},
	}).Create(active).Error
}

// LoadAll returns every profile's active-version row — the set the engine's arming cross-
// references at startup. Not tenant-scoped (tenant on the row), so it reads the whole table.
func (s *ProfileActiveStore) LoadAll(ctx context.Context) ([]ProfileActive, error) {
	var actives []ProfileActive
	if err := s.rdb.DB(ctx).Find(&actives).Error; err != nil {
		return nil, err
	}
	return actives, nil
}
