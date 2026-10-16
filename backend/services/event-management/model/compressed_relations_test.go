// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A server that cannot plan the lookup must surface that as an error, never as an empty
// list. The failure this helper replaced was exactly that — a catalog query that stopped
// planning on a newer TimescaleDB — and an empty list is the one answer the migrations
// cannot tell from "nothing is compressed": they would lock events alone and meet a writer
// to a compressed relation inside the build instead of at the bounded LOCK. SQLite has none
// of the catalogs the query reads, so it stands in for a server whose catalogs moved.
func TestCompressedRelationsSurfacesAQueryThatCannotPlan(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	rels, err := compressedRelations(db, "events")
	require.Error(t, err, "a lookup the server cannot run is an error, not an empty answer")
	assert.Nil(t, rels)
}
