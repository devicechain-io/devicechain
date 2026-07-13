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
var Migrations = []*gormigrate.Migration{
	secrets.NewSecretStoreSchema(),
	NewConnectorsSchema(),
	NewConnectorVersionsSchema(),
}
