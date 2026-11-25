// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/ledger"
	"gorm.io/gorm"
)

// fleetDrivableScope excludes operations whose protocol this build cannot drive.
// Background writers share this predicate; ordinary reads remain unfiltered.
func fleetDrivableScope(db *gorm.DB) *gorm.DB {
	return db.Where("driver_protocol >= ? AND driver_protocol <= ?", 1, ledger.SupportedDriverProtocol)
}
