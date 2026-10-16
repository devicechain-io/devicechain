// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "gorm.io/gorm"

// compressedRelationsSQL lists, as schema-qualified quoted names, the internal relations
// that hold the compressed rows of one "event-management" hypertable's chunks: one per
// chunk that currently has a compressed counterpart, none for a table that is not a
// hypertable or does not exist.
//
// 🔴 It must parse on every TimescaleDB this code runs against, and the obvious query
// does not. Up to 2.28 a chunk's compressed counterpart was
// _timescaledb_catalog.chunk.compressed_chunk_id, with the names in schema_name and
// table_name; 2.29.0 removed all three columns (a chunk is identified by relid instead).
// A query naming them fails to PLAN on 2.29 and later — not an empty result, a 42703 —
// and the re-keying migrations that read it then fail on every start, on a fresh install
// too, because the read runs before the first LOCK whether or not anything is compressed.
//
// So it reads only what every one of those catalogs shares:
//
//   - pg_inherits, for the hypertable's chunks: a chunk is an inheritance child of its
//     hypertable in every version;
//   - _timescaledb_catalog.compression_settings, whose per-chunk rows map relid (the
//     chunk) to compress_relid (its compressed relation) on 2.28, 2.29 and 2.30 alike;
//   - pg_class + pg_namespace, for the name, so a stale compress_relid whose relation is
//     gone drops out rather than being locked.
//
// Measured against the 2.28.3, 2.29.0, 2.29.2 and 2.30.2 catalogs with a compressed
// chunk, a decompressed one and an uncompressed one, it returns exactly the relations the
// 2.28 query returned; on 2.28.3 it also matched it for partially compressed, frozen,
// recompressed and merged chunks, dropped chunks, and chunks in another schema. It holds
// for chunks compressed by an OLDER TimescaleDB too: with chunks compressed under 2.13.0
// through 2.27.0 and the extension then updated in place (ALTER EXTENSION timescaledb
// UPDATE), it matched the old query on 2.28.3 and found every compressed chunk on 2.30.2,
// because the update scripts write the per-chunk rows it reads. to_regclass keeps the old query's answer for a missing table — no
// rows — rather than an error.
const compressedRelationsSQL = `SELECT format('%I.%I', n.nspname, r.relname)
	FROM pg_inherits i
	JOIN _timescaledb_catalog.compression_settings cs ON cs.relid = i.inhrelid
	JOIN pg_class r ON r.oid = cs.compress_relid
	JOIN pg_namespace n ON n.oid = r.relnamespace
	WHERE i.inhparent = to_regclass(format('%I.%I', 'event-management', ?::text))`

// compressedRelations returns the compressed relations behind table's chunks (see
// compressedRelationsSQL), for a migration to lock alongside the table itself.
func compressedRelations(db *gorm.DB, table string) ([]string, error) {
	var rels []string
	if err := db.Raw(compressedRelationsSQL, table).Scan(&rels).Error; err != nil {
		return nil, err
	}
	return rels, nil
}
