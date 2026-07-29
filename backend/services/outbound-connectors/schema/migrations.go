// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-microservice/secrets"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

// Migrations is the ordered outbound-connectors schema history (ADR-060). The shared
// secrets table (ADR-059) holds each outbound credential's envelope-encrypted value —
// resolved server-internal at dispatch and never returned across the API; its migration
// is owned by core/secrets so every consuming service seals with the same instance KEK.
// Slice C4 adds the versioned Connector entity's own tables (draft + published versions).
// The area's own tables were collapsed to a single baseline pre-GA (see NewBaselineSchema);
// the IDs of the two migrations it replaces stay recorded in any database that ran them,
// which gormigrate tolerates because the RdbManager runs with ValidateUnknownMigrations off.
//
// The secret-store entry stays SEPARATE on purpose: that table is core's schema, shared by
// every service that seals with the instance KEK, so it is not this area's to fold in.
//
// CHANGING THE SCHEMA: append a migration here. Never edit the baseline — it builds from its
// own frozen snapshot types precisely so it does not track the live models.
var Migrations = []*gormigrate.Migration{
	secrets.NewSecretStoreSchema(),
	NewBaselineSchema(),
}
