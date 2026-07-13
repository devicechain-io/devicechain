// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-microservice/secrets"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

// Migrations is the ordered outbound-connectors schema history (ADR-060). In slice C3 the service's
// only persistence is the shared secrets table (ADR-059) holding each outbound credential's
// envelope-encrypted value — resolved server-internal at dispatch and never returned across the API.
// Its migration is owned by core/secrets so every consuming service seals with the same instance KEK.
// The versioned Connector entity's own tables land here in slice C4.
var Migrations = []*gormigrate.Migration{
	secrets.NewSecretStoreSchema(),
}
