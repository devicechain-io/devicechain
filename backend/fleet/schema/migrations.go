// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package schema owns the fleet manager's migration chain. Fleet is a different instance type
// from the tenant areas: none of its tables is tenant-scoped, and nothing here is served yet.
package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

// Migrations run in slice order (not by ID), so a new migration must be appended last.
//
// CHANGING THE SCHEMA: append a migration here that declares its own snapshot structs of just
// what it touches. Never edit an existing migration, and never point a migration at a live
// model: a migration is a snapshot of a point in time, and one that tracks the live models is
// silently rewritten for fresh installs while every existing database keeps what it already
// applied. Anything appended must be individually re-runnable, since migrations run with
// UseTransaction:false (core/rdb) and replay from the top after a failure. A CHECK constraint
// carries a version in its name; widening one is a new migration that adds the next version NOT
// VALID, validates it, then drops the old one, each step behind its own catalog probe.
var Migrations = []*gormigrate.Migration{
	NewWriterExclusionSchema(),
}
