// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// internalTables lists the ordinary tables in _timescaledb_internal, schema-qualified the
// way compressedRelations returns them.
func internalTables(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var rels []string
	require.NoError(t, db.Raw(`SELECT format('%I.%I', n.nspname, c.relname)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = '_timescaledb_internal' AND c.relkind = 'r'`).Scan(&rels).Error)
	return rels
}

// TestIntegrationCompressedRelationsFindWhatCompressionCreated checks compressedRelations
// by VALUE against an oracle that reads no TimescaleDB catalog at all: the tables that
// compress_chunk added to _timescaledb_internal. The catalog is the thing that moves
// between releases (2.29.0 dropped the chunk columns the old lookup read), so an oracle
// built on any catalog column would agree with whichever version it was written for.
//
// It runs on whatever server DC_IT_PGPORT points at, which is how it covers both
// catalogs: the pinned image in the `migrations` and `integration` jobs, the newer one in
// `migrations-timescaledb-next`.
func TestIntegrationCompressedRelationsFindWhatCompressionCreated(t *testing.T) {
	inst := freshInstance(t, "itcompressedrels")
	sys := systemDB(newPostgresManager(t, inst))
	twoChunkSeed(t, sys)

	none, err := compressedRelations(sys, "events")
	require.NoError(t, err)
	assert.Empty(t, none, "nothing is compressed yet")

	require.NoError(t, sys.Exec(enableCompressionStmt("events")).Error)
	var chunks []string
	require.NoError(t, sys.Raw(`SELECT format('%I.%I', chunk_schema, chunk_name)
		FROM timescaledb_information.chunks
		WHERE hypertable_schema = 'event-management' AND hypertable_name = 'events'
		ORDER BY range_start`).Scan(&chunks).Error)
	require.Len(t, chunks, 2, "twoChunkSeed gives events two chunks")

	// compress compresses one chunk and returns the one relation that made.
	compress := func(chunk string) string {
		t.Helper()
		before := internalTables(t, sys)
		require.NoError(t, sys.Exec(`SELECT compress_chunk(?::regclass)`, chunk).Error)
		var created []string
		for _, rel := range internalTables(t, sys) {
			if !slices.Contains(before, rel) {
				created = append(created, rel)
			}
		}
		require.Len(t, created, 1, "compress_chunk creates one relation for one chunk")
		return created[0]
	}

	first := compress(chunks[0])
	got, err := compressedRelations(sys, "events")
	require.NoError(t, err)
	require.Equal(t, []string{first}, got, "the compressed relation of the one compressed chunk")
	var rows int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM `+got[0]).Scan(&rows).Error)
	assert.Positive(t, rows, "%s holds the chunk's compressed batches", got[0])

	// Every compressed chunk, not one of them: a relation the lookup misses is one the
	// migrations do not lock up front, and the build then meets its holder inside the
	// build instead of at the bounded LOCK.
	second := compress(chunks[1])
	both, err := compressedRelations(sys, "events")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{first, second}, both, "both compressed chunks' relations")

	other, err := compressedRelations(sys, "measurement_events")
	require.NoError(t, err)
	assert.Empty(t, other, "another hypertable's chunks are not this one's")

	missing, err := compressedRelations(sys, "no_such_table")
	require.NoError(t, err)
	assert.Empty(t, missing, "a table that does not exist has no compressed relations, and is not an error")

	require.NoError(t, sys.Exec(`SELECT decompress_chunk(?::regclass)`, chunks[0]).Error)
	left, err := compressedRelations(sys, "events")
	require.NoError(t, err)
	assert.Equal(t, []string{second}, left, "decompressing one chunk drops its relation and only its relation")

	require.NoError(t, sys.Exec(`SELECT decompress_chunk(?::regclass)`, chunks[1]).Error)
	after, err := compressedRelations(sys, "events")
	require.NoError(t, err)
	assert.Empty(t, after, "a decompressed chunk has no compressed relation to lock")
}
