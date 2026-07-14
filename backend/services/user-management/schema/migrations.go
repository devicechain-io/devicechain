// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

// Migrations run in slice order (not by ID), so a new migration must be appended
// last. Migrations may also be *removed* from this slice once their shape is dead
// (the legacy role migrations were, with NewDropLegacyUserRole reclaiming their
// tables): an upgraded instance keeps the removed IDs recorded, which gormigrate
// tolerates because the RdbManager runs with ValidateUnknownMigrations off
// (backend/core/rdb). Don't enable that validation without first reconciling
// these orphaned rows.
var (
	Migrations = []*gormigrate.Migration{
		NewInitialSchema(),
		NewSigningKeyRetention(),
		NewIdentitySchema(),
		NewTenantSchema(),
		NewDropLegacyUserRole(),
		NewSettingsSchema(),
		NewTenantGovernance(),
		NewOAuthClientSchema(),
		NewTenantOutboundGovernance(),
		NewTenantBranding(),
	}
)
