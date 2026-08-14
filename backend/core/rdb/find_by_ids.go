// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import "gorm.io/gorm"

// FindByIds loads the rows whose primary keys appear in ids.
//
// 🔴 THE EMPTY CASE IS THE ENTIRE REASON THIS EXISTS. gorm's inline-condition form —
// `db.Find(&out, ids)` — DROPS an empty id slice rather than rendering a predicate that
// matches nothing, so it degrades into an unfiltered, unpaginated SELECT: a request for
// no rows is answered with every row in scope. Measured against real gorm, not inferred
// from the docs, and pinned by TestGormDropsAnEmptyInlineIdSlice in find_by_ids_test.go.
//
// The trap is invisible at the call site because the SIBLING lookup is safe: the same
// packages' `Find(&out, "token in ?", tokens)` renders a match-nothing predicate on an
// empty slice and returns zero rows. The two sit side by side in every api_*.go and
// behave OPPOSITELY on the same input, so reading one teaches the wrong rule about the
// other. And it is reachable straight from the API — `xById(ids: [])` is a legal GraphQL
// document, and nothing upstream rejects an empty list.
//
// Owning the destination is deliberate. A helper taking `out any` and leaving it alone on
// the empty path would still hand back whatever the caller had already put there; here,
// zero ids means zero rows because there is no other value the function can return.
//
// db is the caller's already-decorated handle, so Preload chains compose:
//
//	rdb.FindByIds[Device](api.RDB.DB(ctx).Preload("DeviceType"), ids)
func FindByIds[T any](db *gorm.DB, ids []uint) ([]*T, error) {
	found := make([]*T, 0, len(ids))
	if len(ids) == 0 {
		return found, nil
	}
	if err := db.Find(&found, ids).Error; err != nil {
		return nil, err
	}
	return found, nil
}
