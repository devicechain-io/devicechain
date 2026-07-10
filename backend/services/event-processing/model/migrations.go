// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

// Migrations is the ordered gormigrate migration list for the event-processing
// service, run under a Postgres advisory lock at startup (see rdb.RdbManager).
var (
	Migrations = []*gormigrate.Migration{
		NewInitialSchema(),
		NewDetectRulesSchema(),
		NewRosterSchema(),
	}
)
