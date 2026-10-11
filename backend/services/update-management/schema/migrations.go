// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package schema owns the update-management migration chain.
package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

// Migrations run in slice order (not by ID), so a new migration must be appended last.
//
// CHANGING THE SCHEMA: append a migration here that declares its own snapshot structs of
// just what it touches. Never edit the baseline, and never point a migration at a live
// model — a migration is a snapshot of a point in time, and one that tracks the live
// models is silently rewritten for fresh installs while every existing database keeps
// what it already applied. Anything appended must be individually re-runnable, since
// migrations run with UseTransaction:false (core/rdb) and replay from the top after a
// failure.
var Migrations = []*gormigrate.Migration{
	NewBaselineSchema(),
}
