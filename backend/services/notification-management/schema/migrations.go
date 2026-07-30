// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-microservice/secrets"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

// Migrations is the ordered notification-management schema history (ADR-017). The
// service had no persistence before N.B; these are its initial tables. The shared
// secrets table (ADR-059) holds each channel's envelope-encrypted delivery secret
// (this is the first consumer of the secret store, S3); its migration is owned by
// core/secrets so every consuming service seals with the same crypto.
// The area's own tables were collapsed to a single baseline pre-GA (see NewBaselineSchema); the
// IDs of the four migrations it replaces stay recorded in any database that ran them, which
// gormigrate tolerates because the RdbManager runs with ValidateUnknownMigrations off.
//
// The secret-store entry stays SEPARATE on purpose: that table is core's schema, shared by every
// service that seals with the instance KEK, so it is not this area's to fold in.
//
// CHANGING THE SCHEMA: append a migration here. Never edit the baseline — it builds from its own
// frozen snapshot types precisely so it does not track the live models.
var (
	Migrations = []*gormigrate.Migration{
		secrets.NewSecretStoreSchema(),
		NewBaselineSchema(),
	}
)
