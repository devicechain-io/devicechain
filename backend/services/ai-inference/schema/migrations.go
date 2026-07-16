// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-microservice/secrets"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

// Migrations is the ordered ai-inference schema history (ADR-056). The shared secrets
// table (ADR-059) holds each provider's envelope-encrypted API key — resolved
// server-internal at inference time and never returned across the API; its migration
// is owned by core/secrets so every consuming service seals with the same instance
// KEK. The provider list (instance-scoped AIProvider) is this service's own table.
var Migrations = []*gormigrate.Migration{
	secrets.NewSecretStoreSchema(),
	NewAIProvidersSchema(),
}
