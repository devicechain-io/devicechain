// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package settings

import (
	"context"
	"errors"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store persists system-setting overrides. The rows are instance-global, so every
// access runs through the system context — the same lane iam.Store and the
// SigningKey use — which satisfies the fail-closed tenant guard (ADR-015) for a
// model that carries no tenant.
//
// 🔴 Its methods are UNEXPORTED, so the only way to write a setting is through
// Service, which validates. NewStore stays exported because main.go constructs
// one to hand to NewService — but a caller holding a *Store can do nothing with
// it except that. This is deliberate: a comment on Service.Set used to claim it
// was "the only write path", and that was true only because no other code
// happened to call the exported Store.Set, which took no registry, ran no
// validator, and would have accepted an unknown key. A guarantee that rests on
// nobody noticing an exported method is not a guarantee.
type Store struct {
	db *rdb.RdbManager
}

// NewStore wraps an RdbManager.
func NewStore(db *rdb.RdbManager) *Store { return &Store{db: db} }

// sys returns a gorm handle scoped to the instance-global system context.
func (s *Store) sys(ctx context.Context) *gorm.DB {
	return s.db.DB(core.WithSystemContext(ctx))
}

// overrides returns every stored override row.
func (s *Store) overrides(ctx context.Context) ([]SystemSetting, error) {
	var rows []SystemSetting
	if err := s.sys(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// get returns the override row for a key, or (nil, nil) when none is stored.
func (s *Store) get(ctx context.Context, key string) (*SystemSetting, error) {
	var row SystemSetting
	err := s.sys(ctx).Where("key = ?", key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// set upserts an override, stamping the acting identity. The value is stored
// verbatim as opaque JSON.
func (s *Store) set(ctx context.Context, key string, value []byte, updatedBy string) error {
	row := SystemSetting{Key: key, Value: datatypes.JSON(value), UpdatedBy: updatedBy}
	return s.sys(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_by", "updated_at"}),
	}).Create(&row).Error
}

// clear removes an override so the effective value reverts to the code default.
// Removing a key that has no override is a no-op.
func (s *Store) clear(ctx context.Context, key string) error {
	return s.sys(ctx).Where("key = ?", key).Delete(&SystemSetting{}).Error
}
