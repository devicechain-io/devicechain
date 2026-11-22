// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
)

// FleetOperationEnforcement reads the catalog afresh on every request. This is
// observation, not the locking activation check required by a future executor.
func (api *Api) FleetOperationEnforcement(ctx context.Context) string {
	return fleetOperationEnforcement(func() ([]string, error) {
		var states []string
		// A catalog read touches no tenant row, so it needs no system context.
		err := api.RDB.DB(ctx).Raw(`SELECT t.tgenabled::text
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
JOIN pg_catalog.pg_namespace f ON f.oid = p.pronamespace
WHERE n.nspname = 'device-management' AND c.relname = 'entity_attributes'
  AND f.nspname = 'device-management' AND p.proname = ? AND p.pronargs = 0
  AND t.tgname = ? AND NOT t.tgisinternal
  AND (t.tgtype::integer & 28) = 28 AND t.tgqual IS NULL`, FleetOperationEnforcementFunction, FleetOperationEnforcementTrigger).Scan(&states).Error
		return states, err
	})
}

// Inject the catalog read so the unavailable state is tested without Postgres.
func fleetOperationEnforcement(read func() ([]string, error)) string {
	states, err := read()
	if err != nil {
		return "UNREADABLE"
	}
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
