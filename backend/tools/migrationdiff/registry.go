// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"

	commanddelivery "github.com/devicechain-io/dc-command-delivery/model"
	dashboardmanagement "github.com/devicechain-io/dc-dashboard-management/model"
	devicemanagement "github.com/devicechain-io/dc-device-management/schema"
	devicestate "github.com/devicechain-io/dc-device-state/model"
	eventmanagement "github.com/devicechain-io/dc-event-management/model"
	eventprocessing "github.com/devicechain-io/dc-event-processing/model"
	notificationmanagement "github.com/devicechain-io/dc-notification-management/schema"
	outboundconnectors "github.com/devicechain-io/dc-outbound-connectors/schema"
	usermanagement "github.com/devicechain-io/dc-user-management/schema"
)

// area is one migratable functional area: its Postgres schema name (which is the
// functional-area name verbatim, dashes and all) and its ordered gormigrate chain.
type area struct {
	name       string
	migrations []*gormigrate.Migration
}

// areas is every service that owns a migration chain, in a stable order. This is the
// ONE place that imports each service's migration package; the harness runs each
// chain through the real core/rdb path and snapshots its schema. When a service's
// chain is squashed to a single baseline (the GA migration-flatten), nothing here
// changes — the same entry now points at the baseline, and `verify` proves it
// reproduces the golden the incremental chain produced.
var areas = []area{
	{"command-delivery", commanddelivery.Migrations},
	{"dashboard-management", dashboardmanagement.Migrations},
	{"device-management", devicemanagement.Migrations},
	{"device-state", devicestate.Migrations},
	{"event-management", eventmanagement.Migrations},
	{"event-processing", eventprocessing.Migrations},
	{"notification-management", notificationmanagement.Migrations},
	{"outbound-connectors", outboundconnectors.Migrations},
	{"user-management", usermanagement.Migrations},
}
