// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import "strings"

// RECOGNISING A UNIQUE-INDEX COLLISION, so a service can answer it in its own words.
//
// # 🔴 WHY THIS IS NEEDED AT ALL: A `SELECT` CANNOT LOCK A ROW THAT DOES NOT EXIST
//
// The shape every rename on this platform uses is a lookup for the requested token
// followed by a write, inside one transaction. That reads as though the transaction
// closes the race, and it does not. At READ COMMITTED — the default on Postgres and the
// only thing SQLite offers here — a `SELECT` that matches NOTHING takes no lock, because
// there is no row to lock. Two concurrent renames onto one free token therefore both see
// it free, both proceed, and the loser is stopped by the UNIQUE INDEX rather than by the
// check.
//
// That is the correct outcome — the index is the authority and nothing is corrupted — but
// what the loser RECEIVES is a raw driver message:
//
//	Postgres: duplicate key value violates unique constraint "uix_connectors_tenant_token" (SQLSTATE 23505)
//	SQLite:   UNIQUE constraint failed: connectors.tenant_id, connectors.token (2067)
//
// Neither is something a client can act on, and neither is what these APIs promise. A
// caller cannot be asked to write two error handlers for one condition that differ only by
// which of two callers got there first, so the losing path has to be translated into the
// same sentence the uncontended refusal produces.
//
// # Why string matching, and why it is not as fragile as it looks
//
// GORM's TranslateError is not enabled anywhere in this codebase, so what arrives is the
// driver's own error. Matching a driver ERROR TYPE would mean importing a Postgres driver
// into a package that the maintainer-only tools and every service already depend on, to
// recognise a condition whose SQLite spelling that type cannot describe anyway. The
// message text is the only thing both drivers agree to expose.
//
// The narrowness comes from the arguments rather than from the matching. A caller names
// the INDEX (which Postgres prints) and the qualified COLUMNS (which SQLite prints
// instead), so a match is a statement about one specific constraint on one specific table
// — not about unique violations in general.
//
// # 🔴 IT IS ONE FUNCTION IN core RATHER THAN ONE PER SERVICE, AND THAT IS THE POINT
//
// Four rename mutations need this, and the first of them was written with the matcher
// inlined. Three more copies is how four call sites come to disagree about what counts as
// a collision — and the failure mode of disagreeing is that one service hands a caller
// `SQLSTATE 23505` while the other three hand it a sentence, which is exactly the
// inconsistency the translation exists to remove.

// IsUniqueViolation reports whether err is the database's report that a write collided
// with the named unique index.
//
// indexName is what Postgres prints (the index or constraint name). qualifiedColumns are
// `table.column` strings, of which SQLite prints the whole list; passing the ONE column
// that distinguishes this index is enough and is more robust than passing all of them,
// because it does not depend on the order the index declares them in.
//
// 🔴 AT LEAST ONE COLUMN IS REQUIRED, AND AN EMPTY LIST RETURNS FALSE RATHER THAN
// MATCHING LOOSELY. "UNIQUE constraint failed" on its own appears in every SQLite
// uniqueness error on every table, so a caller who passed no columns would translate an
// unrelated collision — a duplicate version number, a repeated grant — into "that token is
// already in use", and the wrong sentence is worse than the raw one. The same reasoning
// applies to indexName: an empty one is not matched against, so a caller supplying neither
// gets false rather than a function that answers yes to everything.
func IsUniqueViolation(err error, indexName string, qualifiedColumns ...string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()

	// Postgres names the index, which is unambiguous on its own.
	if indexName != "" && strings.Contains(msg, indexName) {
		return true
	}

	// SQLite names the columns instead, and only ever the columns — so the marker has to
	// be paired with something table-specific to mean anything.
	if len(qualifiedColumns) == 0 || !strings.Contains(msg, "UNIQUE constraint failed") {
		return false
	}
	for _, column := range qualifiedColumns {
		if column == "" || !strings.Contains(msg, column) {
			return false
		}
	}
	return true
}
