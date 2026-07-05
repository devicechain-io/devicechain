// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

// Migrations is the ordered notification-management schema history (ADR-017). The
// service had no persistence before N.B; these are its initial tables.
var (
	Migrations = []*gormigrate.Migration{
		NewNotificationChannelSchema(),
		NewNotificationPolicySchema(),
		NewNotificationStateSchema(),
	}
)
