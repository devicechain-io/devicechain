// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// legacyFenceSetVersionRow is the SNAPSHOT of the two columns this migration reads out of
// geo_fence_set_versions, plus the id it writes back by. It is not model.GeoFenceSetVersion
// and must never be replaced by it — a migration's shapes are a point in time.
type legacyFenceSetVersionRow struct {
	ID       uint
	TenantId string
	Snapshot []byte
}

// legacyFenceSetSnapshot is the ON-DISK fence-set snapshot as it exists on BOTH sides of the
// content-addressing change, which is what lets one struct decide whether a row needs
// rewriting. Before, a fence entry carried its geometry inline and no hash; after, it carries
// a hash and no geometry. A row mid-migration cannot exist — the rewrite is per row — but a
// TABLE holding both shapes is the normal state of an upgraded instance until this runs.
//
// 🔴 EVERY FIELD OF THE SNAPSHOT DOCUMENT MUST APPEAR HERE, because the rewrite re-encodes
// the whole document and anything absent from this struct is DELETED by that round trip.
// positionSum is the one that is easy to miss, and its presence here is deliberately WIDER
// than the shapes any released version can produce: positionSum arrived WITH the archive, so
// a document holding both it and a hash-less fence entry was never written by anything. The
// field is carried because the round trip is over the whole document rather than over the
// entries it rewrites, and a struct that only covers the inputs it expects turns any future
// or hand-repaired row into silent data loss. It is a pointer for the same reason the live
// type makes it one — absent and zero are different answers, and re-encoding an absent one as
// 0 would charge the tenant's whole set as new geometry on its next edit.
type legacyFenceSetSnapshot struct {
	Version     int32              `json:"version"`
	Fences      []legacyFenceEntry `json:"fences"`
	PositionSum *int               `json:"positionSum,omitempty"`
}

// legacyFenceEntry carries both forms deliberately. Geometry is `omitempty` so a rewritten
// entry drops it, and Hash is `omitempty` so an entry this migration did not touch is
// re-encoded exactly as it arrived.
type legacyFenceEntry struct {
	Token    string          `json:"token"`
	Hash     string          `json:"hash,omitempty"`
	Geometry json.RawMessage `json:"geometry,omitempty"`
}

// NewGeoFenceSnapshotBackfill rewrites every fence-set snapshot written before the geometry
// archive existed, moving each fence's inlined geometry into geo_fence_geometry_blobs and
// replacing it with the content address that names it.
//
// # Why this exists, and why its absence was not theoretical
//
// NewGeoFenceGeometryBlobsSchema created the archive and said no backfill was possible to
// want, on the grounds that an existing instance is recreated rather than migrated. That is
// true of a BASELINE and false of an APPENDED migration, which is what both it and this are:
// v0.11.0 and v0.12.0 were each reached with `helm upgrade`, the published upgrade guide
// promises it, and the release that introduced the archive promises it too.
//
// What that left, measured rather than reasoned about: a snapshot written before the change
// has entries of the form {token, geometry} and no hash at all. Decoding one into the new
// stored form yields an entry whose hash is the EMPTY STRING, which no archived document can
// ever be stored under. So on an upgraded instance holding geofences,
//
//   - CurrentGeoFenceSetSnapshot fails outright — `names geometry "" for fence "yard", which
//     is not in the archive` — which is hydration correctly refusing to hand back a short
//     fence set, and
//   - CurrentGeoFenceSetManifest SUCCEEDS and hands out entries addressed by "", so the
//     detection side resolves every one of them to nothing and each fence becomes a fence
//     carrying an error.
//
// Geofence evaluation is therefore dead for that tenant until somebody edits a fence, which
// mints a version in the new form and heals it by accident. Nothing announces this.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure, so this
// must be individually re-runnable. It is, and by construction rather than by a flag: the
// only rows it selects are those holding an entry with no hash, the archive insert skips a
// document the tenant already holds, and a second run therefore selects nothing. Running it
// against a fully-migrated instance is a no-op that reads one page and stops.
//
// # Why it is not one SQL statement
//
// It could be — Postgres has sha256(), jsonb_agg() and WITH ORDINALITY, and the whole rewrite
// fits in an UPDATE. What that form cannot do is FAIL on the one input that must not be
// silently accepted: an entry carrying neither a hash nor a geometry. In SQL that entry's
// hash expression evaluates to NULL and the row is rewritten with `"hash": null`, turning a
// corrupt snapshot into a differently-corrupt one with no signal. Here it is an error naming
// the version and the token.
func NewGeoFenceSnapshotBackfill() *gormigrate.Migration {
	const versions = `"device-management"."geo_fence_set_versions"`
	const blobs = `"device-management"."geo_fence_geometry_blobs"`

	return &gormigrate.Migration{
		ID: "20260826000000",
		Migrate: func(tx *gorm.DB) error {
			return backfillGeoFenceSnapshots(tx, versions, blobs)
		},
		// 🔴 NOT A NO-OP, DELIBERATELY. The forward step replaces a document with an address,
		// and a rollback that quietly did nothing would report a revert it did not perform to
		// a caller who then trusts the old code to read the new rows. Reconstructing the
		// inline form from the archive is possible; it is not written because nothing in this
		// repo rolls a migration back, and an untested reconstruction is worse than an honest
		// refusal.
		Rollback: func(tx *gorm.DB) error {
			return fmt.Errorf("geofence snapshot backfill cannot be rolled back: the inline " +
				"geometry it replaced lives only in geo_fence_geometry_blobs now")
		},
	}
}

// backfillGeoFenceSnapshotBatch is how many version rows are read at a time. The table is a
// full history — one row per fence-set mint, never pruned — so a tenant that has edited
// fences for a year has a lot of them, and reading the whole table into memory to rewrite a
// handful is the shape that works on every developer database and dies on the one instance
// that needed the migration most.
const backfillGeoFenceSnapshotBatch = 500

func backfillGeoFenceSnapshots(tx *gorm.DB, versions string, blobs string) error {
	var lastId uint
	for {
		rows := make([]legacyFenceSetVersionRow, 0, backfillGeoFenceSnapshotBatch)
		// A KEYSET cursor rather than OFFSET, because this scan rewrites the very rows it is
		// walking: OFFSET over a table under write is both unstable and quadratic, while
		// `id > last` is neither.
		//
		// The SELECT deliberately does NOT filter to rows that need work. Deciding that means
		// parsing the document, which is what the loop below does — expressing it in SQL would
		// take a jsonb predicate whose only effect is to move the same decision earlier.
		if err := tx.Raw(`SELECT id, tenant_id, snapshot FROM `+versions+`
			WHERE id > ? ORDER BY id LIMIT ?`, lastId, backfillGeoFenceSnapshotBatch).
			Scan(&rows).Error; err != nil {
			return fmt.Errorf("read geofence set versions: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for i := range rows {
			lastId = rows[i].ID
			if err := backfillOneGeoFenceSnapshot(tx, versions, blobs, &rows[i]); err != nil {
				return err
			}
		}
	}
}

// backfillOneGeoFenceSnapshot rewrites one version row, or leaves it exactly as it is.
func backfillOneGeoFenceSnapshot(tx *gorm.DB, versions string, blobs string, row *legacyFenceSetVersionRow) error {
	if len(row.Snapshot) == 0 {
		return nil
	}
	snapshot := &legacyFenceSetSnapshot{}
	if err := json.Unmarshal(row.Snapshot, snapshot); err != nil {
		return fmt.Errorf("parse snapshot of geofence set version row %d: %w", row.ID, err)
	}

	rewritten := false
	for i := range snapshot.Fences {
		entry := &snapshot.Fences[i]
		if entry.Hash != "" {
			continue
		}
		if len(entry.Geometry) == 0 {
			return fmt.Errorf("geofence set version row %d names fence %q with neither a "+
				"geometry nor a content address; the snapshot is corrupt and this migration "+
				"will not invent an address for it", row.ID, entry.Token)
		}
		// 🔴 THE HASH IS OVER THE DOCUMENT AS THE DATABASE HANDS IT BACK, matching what the
		// mint path does. The snapshot column is jsonb, so this sub-document is already in
		// Postgres' canonical rendering rather than in whatever text the author typed, and
		// storing these same bytes is what makes the address and the document agree.
		sum := sha256.Sum256(entry.Geometry)
		hash := hex.EncodeToString(sum[:])
		if err := archiveOneLegacyGeometry(tx, blobs, row.TenantId, hash, entry.Geometry); err != nil {
			return fmt.Errorf("archive geometry for fence %q of geofence set version row %d: %w",
				entry.Token, row.ID, err)
		}
		entry.Hash = hash
		entry.Geometry = nil
		rewritten = true
	}
	if !rewritten {
		return nil
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("re-encode snapshot of geofence set version row %d: %w", row.ID, err)
	}
	// The ::jsonb cast is required, not decoration: the driver sends a []byte as bytea, and
	// assigning that to a jsonb column is an error rather than a coercion.
	if err := tx.Exec(`UPDATE `+versions+` SET snapshot = ?::jsonb WHERE id = ?`,
		string(encoded), row.ID).Error; err != nil {
		return fmt.Errorf("rewrite snapshot of geofence set version row %d: %w", row.ID, err)
	}
	return nil
}

// archiveOneLegacyGeometry stores one geometry document under its content address, unless the
// tenant already holds it.
//
// ON CONFLICT DO NOTHING against the (tenant_id, hash) unique index is the whole mechanism,
// and it is exactly right here for the reason the model layer's archive states: a conflict
// means the other row holds the identical bytes, since the address IS the content. Unlike
// that path, this one does not need a preceding read — it is not exercised by the sqlite unit
// tests, where the unique index does not exist.
func archiveOneLegacyGeometry(tx *gorm.DB, blobs string, tenantId string, hash string, document []byte) error {
	return tx.Exec(`INSERT INTO `+blobs+
		` (created_at, updated_at, tenant_id, hash, document)
		  VALUES (now(), now(), ?, ?, ?::jsonb)
		  ON CONFLICT (tenant_id, hash) DO NOTHING`,
		tenantId, hash, string(document)).Error
}
