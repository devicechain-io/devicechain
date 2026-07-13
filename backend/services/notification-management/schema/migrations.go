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
var (
	Migrations = []*gormigrate.Migration{
		NewNotificationChannelSchema(),
		NewNotificationPolicySchema(),
		NewNotificationStateSchema(),
		secrets.NewSecretStoreSchema(),
		// ADR-059 S3: retire the reversible plaintext channel.secret column now that the
		// delivery secret lives in the secret store above (real cutover on existing DBs).
		NewNotificationChannelDropSecretSchema(),
	}
)
