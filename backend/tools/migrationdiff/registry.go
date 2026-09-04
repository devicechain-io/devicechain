// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"

	aiinference "github.com/devicechain-io/dc-ai-inference/schema"
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

	// extraSchemas names any OTHER Postgres schema this area's chain creates, so the
	// golden covers it too.
	//
	// 🔴 IT EXISTS BECAUSE THE DUMP IS SCOPED TO `--schema <area name>`, WHICH MAKES A
	// SCHEMA CREATED UNDER ANOTHER NAME INVISIBLE TO THE ONLY GATE THAT EXERCISES
	// MIGRATIONS AT ALL. That is not hypothetical: event-management's chain builds an
	// `analytics` schema whose view bodies ARE the tenant boundary for every direct
	// Postgres/BI connection. Without this field a later migration could rewrite one of
	// those bodies — dropping the tenant predicate, say — and `verify` would report
	// every area green, because it would never have looked.
	extraSchemas []string
}

// schemas is every Postgres schema this area's golden covers, area schema first.
func (a area) schemas() []string {
	return append([]string{a.name}, a.extraSchemas...)
}

// areas is every service that owns a migration chain, in a stable order. This is the
// ONE place that imports each service's migration package; the harness runs each
// chain through the real core/rdb path and snapshots its schema. When a service's
// chain is squashed to a single baseline (the GA migration-flatten), nothing here
// changes — the same entry now points at the baseline, and `verify` proves it
// reproduces the golden the incremental chain produced.
var areas = []area{
	{name: "ai-inference", migrations: aiinference.Migrations},
	{name: "command-delivery", migrations: commanddelivery.Migrations},
	{name: "dashboard-management", migrations: dashboardmanagement.Migrations},
	{name: "device-management", migrations: devicemanagement.Migrations},
	{name: "device-state", migrations: devicestate.Migrations},
	// `analytics` is the read-only SQL/BI surface event-management's chain builds
	// (backend/services/event-management/model/analytics.go). It is a schema of its own
	// precisely so a reader granted USAGE on it still has no route to the area schema —
	// which means the golden has to follow it there, or the tenant boundary for every
	// direct Postgres connection would be the one thing this gate never dumps.
	{name: "event-management", migrations: eventmanagement.Migrations, extraSchemas: []string{"analytics"}},
	{name: "event-processing", migrations: eventprocessing.Migrations},
	{name: "notification-management", migrations: notificationmanagement.Migrations},
	{name: "outbound-connectors", migrations: outboundconnectors.Migrations},
	{name: "user-management", migrations: usermanagement.Migrations},
}
