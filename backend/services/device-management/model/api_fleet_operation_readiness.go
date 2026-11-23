// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// FleetOperationEnforcement reads the catalog afresh on every request. This is
// observation, not the locking activation check required by a future executor.
func (api *Api) FleetOperationEnforcement(ctx context.Context) string {
	return fleetOperationEnforcement(func() ([]string, error) {
		// A catalog read touches no tenant row, so it needs no system context.
		return fleetOperationEnforcementStates(api.RDB.DB(ctx))
	})
}

// fleetOperationEnforcementStates returns the tgenabled letter of every trigger on the device
// attribute table that is the expected enforcing trigger: the reserved name, a BEFORE row trigger
// on INSERT, UPDATE and DELETE, with no WHEN condition, calling a zero-argument function of the
// reserved name in this area's schema whose body is the one this release ships.
func fleetOperationEnforcementStates(db *gorm.DB) ([]string, error) {
	var states []string
	err := db.Raw(`SELECT t.tgenabled::text
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
JOIN pg_catalog.pg_namespace f ON f.oid = p.pronamespace
WHERE n.nspname = 'device-management' AND c.relname = 'entity_attributes'
  AND f.nspname = 'device-management' AND p.proname = ? AND p.pronargs = 0
  AND md5(p.prosrc) = ?
  AND t.tgname = ? AND NOT t.tgisinternal
  AND (t.tgtype::integer & 29) = 29 AND t.tgqual IS NULL`,
		FleetOperationEnforcementFunction, FleetOperationEnforcementFunctionMD5, FleetOperationEnforcementTrigger).Scan(&states).Error
	return states, err
}

// Inject the catalog read so the unavailable state is tested without Postgres.
func fleetOperationEnforcement(read func() ([]string, error)) string {
	states, err := read()
	if err != nil {
		log.Warn().Err(err).Msg("Unable to read the database catalog for fleet operation readiness; reporting UNREADABLE")
		return "UNREADABLE"
	}
	return fleetOperationEnforcementFromStates(states)
}

// fleetOperationEnforcementFromStates is the one decision both readiness and the posting-time
// activation check make over the catalog read.
func fleetOperationEnforcementFromStates(states []string) string {
	for _, state := range states {
		if state == "A" {
			return "ENFORCED"
		}
	}
	if len(states) > 0 {
		return "DISABLED"
	}
	return "NOT_INSTALLED"
}
