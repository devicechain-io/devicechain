// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDeviceAttributeLockSchema installs the sole attribute lock-key definition.
// The key definition is never changed in place: a new definition gets a NEW
// function name, so rolling versions cannot silently lock different keys.
func NewDeviceAttributeLockSchema() *gormigrate.Migration {
	return &gormigrate.Migration{ID: "20261220120000", Migrate: func(tx *gorm.DB) error {
		if tx.Dialector.Name() != "postgres" {
			return nil
		}
		return tx.Exec(`CREATE OR REPLACE FUNCTION "device-management".dc_entity_attr_lock(p_type text, p_id bigint)
RETURNS void LANGUAGE sql VOLATILE AS $f$
  SELECT pg_advisory_xact_lock(hashtextextended('dc.attr|' || p_type || '|' || p_id::text, 0))
$f$;`).Error
	}, Rollback: func(*gorm.DB) error {
		return errors.New("device attribute lock cannot be rolled back: every device attribute writer requires the function")
	}}
}
