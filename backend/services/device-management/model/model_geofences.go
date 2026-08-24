// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/geo"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// GeoFence is an authored, tenant-scoped region a device's reported position can be
// tested against (ADR-078). It sits alongside the other authored tenant resources —
// device profiles and the detection rules declared on them — and is referenced from a
// rule by its stable per-tenant token, never by row id.
//
// 🔴 GEOFENCING IS A DETECT FEATURE WRITTEN IN GO, NEVER A DATABASE FEATURE. Nothing
// joins against the geometry, so it is stored as an opaque GeoJSON document in a plain
// jsonb column: no spatial column, no PostGIS, no spatial index. Two reasons, and the
// second is the decisive one:
//
//   - A spatial round-trip per location event is a different machine from the
//     in-memory keyed-streaming engine that evaluates every other DETECT predicate.
//   - It would break replay determinism. A containment test issued at replay time
//     answers against the fences that exist NOW, not the fences that existed when the
//     event happened, so replaying a week-old event through a since-edited fence set
//     produces a different answer than the live run did. Determinism is restored by
//     stamping the tenant's ACTIVE FENCE-SET VERSION onto each resolved LOCATION event
//     at resolve time — exactly the mechanism that already carries ProfileVersionToken
//     and ScopeMemberships — so the fence set an event is evaluated against is frozen
//     into the immutable event rather than re-queried.
//
// Two alternatives were considered and both fail. Pinning the fence version at
// RULE-PUBLISH time makes a fence edit a rule migration (edit one fence, republish
// every profile that references it). Letting fence edits take effect live requires a
// time-travel lookup PLUS fence versions in the engine's checkpoint — machinery that
// does not exist and that the stamp makes unnecessary.
type GeoFence struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	// Geometry is the fence's shape as one self-describing JSON document (required).
	// It is stored whole, never SQL-built, and is opaque to every query in this area.
	// Its schema is GeoFenceGeometry below; the KIND discriminator lives inside the
	// document rather than in a column of its own, which is what lets a later geometry
	// kind land with no migration and no second geometry column.
	Geometry datatypes.JSON `gorm:"not null"`
}

// DefaultOrder implements rdb.Sortable with the registry default: newest first, token
// as the unique tiebreak. Nothing orders by the geometry — it is opaque to every query
// in this area — so the fence list is ordered by authoring recency like any other
// registry. Note the table is geo_fences, not geofences: gorm's naming strategy splits
// the Go name on its word boundary.
func (GeoFence) DefaultOrder() string {
	return "geo_fences.created_at DESC, geo_fences.token ASC"
}

// Geometry kind discriminators. The kind belongs to the FENCE, never to the rule
// language: a rule says "is this device inside fence X", and that sentence does not
// change when X becomes a 3D volume. So a new kind here must never require a new rule
// function — only a new branch in the containment evaluator.
const (
	// GeoFenceKindPolygon2D is a 2D polygon on the WGS84 ellipsoid: a GeoJSON Polygon,
	// exterior ring first, optional interior rings (holes). The only kind implemented.
	GeoFenceKindPolygon2D = "POLYGON_2D"

	// GeoFenceKindPolygon25D is RESERVED, not implemented: the same GeoJSON Polygon
	// plus an altitude band, carried as an additive `altitude` member of the same
	// document — {"minMeters": <float>, "maxMeters": <float>}, metres above the
	// ELLIPSOID, matching the platform-wide coordinate contract that ResolvedLocationEntry
	// documents. Accepting it is a validator branch and an evaluator branch; it needs no
	// column, no migration, and no change to any already-stored fence.
	GeoFenceKindPolygon25D = "POLYGON_2_5D"

	// GeoFenceKindVoxel3D is RESERVED, not implemented: a set of cuboids, carried as a
	// GeoJSON MultiPolygon in the same `geometry` member plus an `altitudes` array
	// parallel to its parts, each {"minMeters", "maxMeters"}. Same additive story.
	GeoFenceKindVoxel3D = "VOXEL_3D"
)

// GeoFenceGeometry is the stored geometry document — a self-describing envelope
// carrying a kind discriminator and one GeoJSON geometry object.
//
// 🔴 THE ENVELOPE IS THE RESERVATION, and it is deliberately an envelope rather than a
// bare GeoJSON document. A bare GeoJSON Polygon has nowhere to say "and between 0 and
// 30 metres"; adding an altitude band later would then need either a second column or a
// GeoJSON foreign member whose absence and whose zero value are indistinguishable. With
// the discriminator present from the first fence, a 2.5D or 3D fence is a new VALUE of
// an existing field plus new sibling members — no DDL, and every already-stored
// POLYGON_2D document keeps parsing unchanged.
//
// Altitude and Voxels are deliberately absent as Go fields: a field that is settable but
// unenforced is worse than no field, because it reads as support. They are named in the
// kind constants above so the shape is on record, and they arrive with the branch that
// honours them.
type GeoFenceGeometry struct {
	// Kind is one of the GeoFenceKind* constants. Required — a document with no kind is
	// rejected rather than defaulted, because defaulting is how a 3D fence authored
	// against a future console would silently be evaluated as its 2D footprint.
	Kind string `json:"kind"`
	// Geometry is the GeoJSON geometry object (RFC 7946), e.g.
	// {"type":"Polygon","coordinates":[[[lon,lat],…]]}. Carried raw so the authored
	// bytes reach storage unmodified.
	Geometry json.RawMessage `json:"geometry"`
}

// Bounds enforced at AUTHORING time. The first two exist for one reason:
//
// 🔴 THE PUBLISH-TIME CEL COST GATE CANNOT SEE EITHER OF THESE NUMBERS. It estimates an
// expression's cost STATICALLY, from the expression tree. A containment call's real cost
// is data-dependent — how many vertices the named fence has, and how many fences are in
// scope — and neither is in the tree. A static estimator therefore prices `inFence(…)`
// as one call whether it tests a 4-vertex yard or a 40,000-vertex coastline. The cost
// has to be bounded where the data is written, which is here.
//
// The third bounds a different quantity for a different consumer; see it for why counting
// positions does not bound bytes.
const (
	// MaxGeoFenceVertices bounds a single fence's total position count across every
	// ring (the closing position of each ring counts; it is cheaper to be conservative
	// than to explain the exception).
	//
	// Containment is O(vertices) per fence per LOCATION event, run in-process by the
	// DETECT engine. 512 sits about two orders of magnitude above real geofences — a
	// site boundary, a yard, a loading zone are tens of vertices, and a simplified
	// administrative boundary is low hundreds — while keeping the worst case
	// computable rather than unbounded.
	MaxGeoFenceVertices = 512

	// MaxGeoFencesPerTenant bounds the other multiplicand. Worst case per location
	// event is MaxGeoFenceVertices × MaxGeoFencesPerTenant = 51,200 edge tests, which
	// is a fraction of a millisecond in Go — the same order as the resolve path's
	// existing per-event work rather than something that dominates it. A bounding-box
	// prefilter makes the realistic cost one fence, but a prefilter is an optimization
	// and this is a bound, so the number is justified without assuming it.
	//
	// 🔴 IT DOES NOT BOUND THE FENCE SET'S SIZE, AND THE PRODUCT OF THESE TWO NUMBERS IS
	// NOT ONE EITHER. Both seams that carry a whole fence set have a 1 MiB budget — the
	// broker's per-message ceiling and the cross-service response cap — and the set is
	// over BOTH at these limits, at every coordinate precision anyone would use.
	// Measured, 100 × 512 as stored GeoJSON text: 672 KB at 2 decimal places, 980 KB at
	// 5, 1.18 MB at 7, 1.39 MB at 9. Five decimal places is about 1.1 m, which is below
	// useful geofencing precision, and the whole-set GraphQL response at 5 dp measures
	// 1,105,765 bytes against a 1,048,576-byte cap. So the set has been over the wire's
	// budget at its own documented limits the whole time.
	//
	// That is why the delivery paths PAGE and why an oversized set is announced by a
	// pointer fact rather than carried. Neither is tidiness: no bound on a single fence
	// can make 100 of them fit in one message while MaxGeoFenceVertices stays at 512, so
	// the aggregate has to be split. See MaxGeoFenceGeometryBytes.
	MaxGeoFencesPerTenant = 100

	// MaxGeoFenceGeometryBytes bounds a single fence's stored geometry DOCUMENT.
	//
	// 🔴 IT IS IN BYTES, AND THAT IS THE ENTIRE POINT: COUNTING POSITIONS DOES NOT BOUND
	// SIZE. MaxGeoFenceVertices bounds how many positions a fence has, which is what
	// containment's O(vertices) cost depends on. It says nothing about how many bytes
	// those positions occupy, because validatePolygon2D checks only that a position has
	// at least two ordinates in the WGS84 ranges — a position may carry EXTRA ordinates
	// beyond [lon, lat], and each ordinate is a JSON number of unbounded length. So a
	// document that satisfies every other rule here can be any size at all: 512 positions
	// of 700 ordinates each was accepted and stored at 1.4 MB, and 512 two-ordinate
	// positions written at 40 decimal places is over 1 MB by itself.
	//
	// That mattered the moment anything downstream had a byte budget. Both seams that
	// carry a fence set have one — the broker's per-message ceiling and the cross-service
	// client's response cap — and neither can be defended by a page size counted in
	// FENCES, because a single fence can exceed either. This bound is what makes "one
	// fence always fits" true, which is what lets a paging reader be total: with it, a
	// page of one is always readable.
	//
	// 🔴 IT BOUNDS THE STORED FORM, NOT THE REQUEST TEXT, AND THAT DISTINCTION IS THE WHOLE
	// POINT OF THE FUNCTION THAT ENFORCES IT. What every downstream seam carries — the
	// snapshot, the fact, the GraphQL response — is what PostgreSQL hands back out of a jsonb
	// column, and jsonb is not a byte store. It parses each number into `numeric` and reprints
	// it with numeric_out, which never uses exponent notation, so the request text and the
	// stored text are different lengths and the difference is unbounded. Measured on
	// PostgreSQL 16:
	//
	//	"1e308"     5 bytes in ->     309 out
	//	"1e131071"  8 bytes in -> 131,072 out   (16,384x)
	//	"1e-300"    6 bytes in ->     302 out   AND IT IS INSIDE [-180, 180]
	//
	// That last row is why range-checking a coordinate is not a size check: 1e-300 is a
	// perfectly in-range longitude. So the bound is applied to the CANONICAL document — the
	// one validateGeoFenceGeometry rebuilds from the parsed values, in the same
	// non-exponent form numeric_out will print — plus jsonb's separator spacing. See
	// jsonbRenderedLen.
	//
	// 🔴 IT IS DELIBERATELY GENEROUS, AND TIGHTENING IT WOULD NOT BUY WHAT IT LOOKS LIKE
	// IT WOULD BUY. Its job is NOT to make a whole fence set fit in one message or one
	// response — that is arithmetically unavailable. A bound tight enough for 100 fences
	// to fit in 1 MiB would have to be about 10 KB per fence, which is BELOW a
	// 512-position fence at five decimal places, i.e. below useful precision at the
	// vertex ceiling this package already promises. Keeping MaxGeoFenceVertices,
	// MaxGeoFencesPerTenant and a single-response read all three is not a choice anyone
	// has; the aggregate is handled by PAGING, and always will be while coordinates are
	// text.
	//
	// What this bound is for is narrower and still necessary: keeping a SINGLE unit of
	// delivery satisfiable. Whatever carries fences — a page of a snapshot read, a batch of
	// geometry documents — terminates on a shrinking retry only if ONE fence fits on its
	// own, and without this, one does not have to.
	//
	// 32 KiB is chosen from measurement: a fence at the vertex ceiling written at nine
	// decimal places (about a millimetre at the equator, more precision than any real
	// editor emits) stores at ~15 KB, so this is ~2.2x the largest realistic authored
	// fence while refusing the pathological documents by orders of magnitude.
	//
	// 🔴 THE AGGREGATE PROBLEM THIS COMMENT USED TO DESCRIBE HAS BEEN SOLVED, AND NOT BY THE
	// FIX IT PREDICTED. It said the root cause was coordinates being JSON text of unbounded
	// length, and that packing them as int32 degrees x 10^7 would shrink the whole ceiling
	// set into one message and let the pointer fact and the paged read be deleted. That is
	// arithmetic about a CONSTANT FACTOR against a product constraint, and it does not hold
	// once the caps move: at a 1024-vertex cap the packed set is over the ceiling again, and
	// raising the caps was the reason to pack. What actually deleted them is changing the
	// UNIT OF DELIVERY from the set to the fence — the fact carries a GeoFenceSetManifest of
	// {token, hash} pairs and the bodies travel separately — which makes the announcement's
	// size independent of what any fence contains, permanently and at any cap. This bound
	// survives that change unaltered, because "one fence must fit one message" is the single
	// size rule per-fence delivery still needs.
	MaxGeoFenceGeometryBytes = 32 << 10

	// MaxGeoFenceGeometryHashesPerRequest bounds how many geometry documents one
	// GeoFenceGeometryDocuments call may ask for. Over the limit is an ERROR, never a
	// partial answer: a caller that asked for 40 addresses and silently got 24 cannot tell
	// that from a tenant holding only 24 of them, and this door's whole contract is that
	// absence means absence.
	//
	// 🔴 IT IS ARITHMETIC, NOT A ROUND NUMBER, AND THE ARITHMETIC IS THE ONLY REASON A
	// COUNT CAN DEFEND A BYTE CAP AT ALL. A limit counted in documents is a guess about
	// bytes unless each document has a known byte ceiling — which is exactly what
	// MaxGeoFenceGeometryBytes provides, applied to the jsonb-rendered form the archive
	// actually returns. 24 x 32 KiB is 786,432 bytes against svcclient.MaxResponseBytes
	// (1 MiB), leaving room for the hashes, the field names and the GraphQL envelope's
	// string escaping. TestGeometryBatchFitsOneResponse measures a worst-case response
	// rather than trusting that estimate, because two numbers in this tree disagree about
	// what escaping costs and a comment cannot settle it.
	//
	// It does not have to defend against documents already stored ABOVE that ceiling —
	// rows predating the bound exist — so a client is still expected to split its request
	// on a too-large response. Splitting is trivial here in a way it was not for the paged
	// read this replaces: a request names a SET OF ADDRESSES, so half of it is still a
	// well-formed request, where a (pageNumber, pageSize) offset could not be re-expressed
	// at a different size and forced the whole walk to restart.
	MaxGeoFenceGeometryHashesPerRequest = 24

	// MaxGeoFencePositionOrdinates bounds how many numbers one position may carry.
	//
	// 🔴 IT IS EXACTLY TWO BECAUSE THE KIND IS EXACTLY 2D, and until it existed the check
	// was `len(pos) < 2` — a MINIMUM, which admitted a position of arbitrary width. 512
	// positions of 7 "1e308" ordinates is 30 KB of request text and 1.1 MB of stored jsonb,
	// accepted, over the response cap on its own. Nothing reads a third ordinate: the 2.5D
	// and voxel kinds are named, reserved and refused, and the console's own reader already
	// requires exactly two (it returns null for anything else rather than open an editor
	// that would drop the rest on save). A maximum, not a minimum, is what makes a position
	// count a size bound again.
	MaxGeoFencePositionOrdinates = 2

	// maxGeoFenceRequestBytes is a cheap intake guard on the AUTHORED text, refusing an
	// absurd request body before anything parses it. It is not the real bound —
	// MaxGeoFenceGeometryBytes is, and it is applied to the canonical form — because the
	// two quantities are not proportional in either direction: canonicalisation SHRINKS a
	// document padded with whitespace and GROWS one written in exponent notation. This
	// number therefore only has to be comfortably above any legitimate request, which at
	// the vertex ceiling is ~14 KB.
	maxGeoFenceRequestBytes = 256 << 10
)

// GeoFenceSetVersion is one immutable, tenant-wide version of the WHOLE fence set,
// minted whenever any fence changes — created, edited, or deleted. Its Version is what
// a resolved LOCATION event is stamped with.
//
// Why the whole set and not per-fence versions: an event is evaluated against every
// fence a rule might name, so what has to be frozen is the SET. Per-fence versions
// would make an event carry a version vector whose length is the tenant's fence count,
// and would still not say which fences existed at that instant.
//
// Snapshot is the frozen fence set (a GeoFenceSetSnapshot document). It is what makes a
// replay preview CORRECT rather than vacuous: previewing a rule against last week's
// events evaluates them against the fences that were live then, because each of those
// events names the version whose snapshot holds them. Without it the stamp would be a
// number pointing at nothing, and the history could never be recovered afterwards —
// edited geometry is gone.
//
// Append-only: nothing updates a row here. Version is unique per tenant, so two
// concurrent fence edits cannot mint the same number (the loser's insert fails and its
// transaction rolls back), mirroring DeviceProfileVersion.
type GeoFenceSetVersion struct {
	gorm.Model
	rdb.TenantScoped

	// Version is monotonic per tenant, starting at 1. It is never reused, including
	// across a fence delete.
	//
	// 🔴 THE (tenant_id, version) UNIQUE INDEX IS DECLARED BY THE MIGRATION, NOT BY A TAG
	// HERE, and the difference is not cosmetic. tenant_id lives in the embedded
	// rdb.TenantScoped, which cannot carry this index's priority-1 tag, so a
	// `uniqueIndex` tag on this field alone would build the index over VERSION ONLY —
	// making version globally unique across tenants and letting the second tenant to
	// exist fail to mint version 1. The migration declares both columns explicitly and is
	// the authority for the schema regardless; see migration_geofences.go.
	Version int32 `gorm:"not null"`

	// Snapshot is the serialized GeoFenceSetSnapshot frozen at mint time.
	Snapshot datatypes.JSON `gorm:"not null"`

	// MintedAt is when the change that minted this version committed.
	MintedAt time.Time `gorm:"not null"`
}

// GeoFenceSetSnapshot is the serialized fence set frozen into a
// GeoFenceSetVersion.Snapshot, HYDRATED — every fence carrying its geometry document. Like
// ProfileSnapshot it is never SQL-built, so the encoding need only be self-consistent, and
// it is not what any row holds: a stored snapshot names geometry by content address (see
// storedGeoFenceSetSnapshot), and hydrateGeoFenceSetSnapshot resolves those addresses
// through the archive to produce this.
//
// It is read back on the geoFenceSetSnapshot / currentGeoFenceSet GraphQL doors, which serve
// the whole fence set in one document. The fence-set FACT no longer takes this shape — it
// carries a GeoFenceSetManifest, whose size is a function of the fence count alone.
type GeoFenceSetSnapshot struct {
	Version int32                 `json:"version"`
	Fences  []GeoFenceSnapshotRef `json:"fences"`
}

// GeoFenceSnapshotRef is one fence as of a fence-set version: its token (how a rule
// names it) and its geometry document (what containment is evaluated against). Name and
// description are deliberately omitted — they are presentation, they do not change what
// a rule computes, and freezing them would make a typo fix mint a version that changes
// no answer.
type GeoFenceSnapshotRef struct {
	Token    string          `json:"token"`
	Geometry json.RawMessage `json:"geometry"`
}

// GeoFenceSetManifest names WHICH fences a fence-set version holds and the CONTENT ADDRESS
// of each one's geometry. It carries no geometry itself, which is the entire point: its
// size is a function of the fence COUNT and of nothing an author can write.
//
// 🔴 THE SIZE PROPERTY IS WHY AN ENTIRE APPARATUS OF BROKER-CEILING MACHINERY IS GONE, so it
// is MEASURED rather than asserted. A token is bounded by core.MaxTokenLen under a grammar
// containing no character JSON has to escape, and a hash is exactly 64 hex characters, so an
// entry has a fixed worst case and a whole manifest has one too — roughly a forty-eighth of the
// default 1 MiB per-message ceiling at MaxGeoFencesPerTenant fences, still inside it at several
// thousand.
//
// 🔑 NO BYTE COUNT IS QUOTED HERE ON PURPOSE. MaxGeoFenceSetManifestBytes() builds the worst
// case and measures it, and that function is the authority; an earlier draft of this comment
// named a figure derived by hand and was wrong by six bytes within a day of being written. A
// number in prose cannot be re-derived when a constant moves, which is exactly when it matters.
//
// It is ONE type serving two seams — the fence-set fact and the geoFenceSetManifest /
// currentGeoFenceSetManifest doors — deliberately, and the reasoning is the opposite of the
// one that keeps GeoFenceSetSnapshot and storedGeoFenceSetSnapshot apart. Those two are
// separate because they describe the same fence set at DIFFERENT levels of resolution, so a
// caller could marshal the wrong one and produce a document that looks correct until the
// archive it should have referenced is needed. A manifest is the same information on both
// seams; splitting it would create exactly the drift the split is supposed to prevent.
type GeoFenceSetManifest struct {
	// Version is the fence-set version this manifest describes. It is what a resolved
	// location event is stamped with, and the key a consumer files the set under.
	Version int32 `json:"version"`
	// Fences names each fence and the content address of its geometry, ordered by token
	// (the mint path orders them, so a manifest is a function of the fence set alone).
	Fences []GeoFenceManifestEntry `json:"fences"`
	// MintedAt is when the change that minted this version committed. It is carried for
	// operator diagnosis — how old is the set this engine is holding? — and containment
	// never reads it.
	MintedAt time.Time `json:"mintedAt"`
}

// GeoFenceManifestEntry is one fence inside a manifest: the token a rule names it by, and
// the content address under which its geometry is stored.
//
// 🔴 AN ENTRY WHOSE HASH THE HOLDER CANNOT RESOLVE MUST BECOME A FENCE CARRYING AN ERROR,
// NEVER A FENCE THAT IS ABSENT. Absence is already meaningful — a rule naming a fence the
// set does not contain is a distinct, reported condition — so a body that failed to arrive,
// dropped silently, reads downstream as "this fence does not exist here" and containment
// answers "outside" for a device that is inside. That is the one failure mode splitting the
// geometry out of the fact introduces, and it is the consumer's job to close.
type GeoFenceManifestEntry struct {
	Token string `json:"token"`
	Hash  string `json:"hash"`
}

// GeoFenceGeometryDocument is one archived geometry document and the content address it is
// filed under — what the geoFenceGeometry door answers with, and what a holder of a manifest
// resolves its entries through.
//
// Document is a json.RawMessage and must stay one, END TO END, from the archive row to the
// consumer that re-hashes it. The address is the SHA-256 of these exact bytes, so a single
// convenience Unmarshal/Marshal anywhere along that path re-orders keys that PostgreSQL's
// jsonb ordering (length, then bytewise) does not lay out the way a Go struct does — and
// every hash then mismatches, permanently, for every fence. Unit tests run on SQLite, whose
// JSON columns round-trip bytes verbatim and therefore cannot see it.
type GeoFenceGeometryDocument struct {
	Hash     string          `json:"hash"`
	Document json.RawMessage `json:"document"`
}

// GeoFenceGeometryBlob is one geometry document in the content-addressed archive: the
// SHA-256 of a canonical geometry envelope, and the envelope itself. It is immutable by
// construction — the address IS the content — and it is written once per distinct
// document no matter how many fence-set versions go on to name it.
//
// 🔴 IT IS NOT A SECOND HOME FOR GeoFence.Geometry. That column is the CURRENT authored
// geometry of a fence: mutable, and what the console and the authoring API read. This is
// the frozen archive of what fence sets were made of, which outlives any edit. Collapsing
// the two would mean either losing history on every edit or making the authoring read a
// join, and the distinction is the same one GeoFenceSetVersion already draws against
// GeoFence.
type GeoFenceGeometryBlob struct {
	gorm.Model
	rdb.TenantScoped

	// Hash is the lowercase hex SHA-256 of Document, as produced by
	// GeoFenceGeometryHash. The (tenant_id, hash) unique index is declared by the
	// migration, not by a tag here, for the reason GeoFenceSetVersion.Version records:
	// tenant_id lives in the embedded rdb.TenantScoped and cannot carry a priority-1 tag,
	// so a uniqueIndex tag on this field alone would build the index over HASH ONLY —
	// making a document globally unique across tenants, so the second tenant to store the
	// same geometry would fail. See migration_geofence_geometry_blobs.go.
	Hash string `gorm:"not null;size:64"`

	// Document is the canonical geometry envelope (kind + GeoJSON geometry), verbatim.
	Document datatypes.JSON `gorm:"not null"`
}

// GeoFenceGeometryHash is the content address of a canonical geometry document.
//
// 🔴 HASH WHAT THE DATABASE HANDS BACK, NEVER WHAT THE AUTHOR WROTE. jsonb is not a byte
// store: it parses each number and reprints it with numeric_out, so the request text and
// the stored text are different lengths and the difference is unbounded (see
// MaxGeoFenceGeometryBytes for the measurements). Canonicalising before storing is what
// makes the stored form stable and predictable, but it does not make it EQUAL to the
// request text — separator spacing alone differs. The mint path hashes the geometry it
// just read out of geo_fences, and stores the blob from those same bytes, so an address
// and the document it names can never disagree. Hashing authored text instead would
// compute an address nothing is stored at, and the miss would look like a lost fence.
func GeoFenceGeometryHash(document []byte) string {
	sum := sha256.Sum256(document)
	return hex.EncodeToString(sum[:])
}

// storedGeoFenceSetSnapshot is the ON-DISK shape of GeoFenceSetVersion.Snapshot: fences
// as CONTENT REFERENCES, never as inlined geometry.
//
// 🔴 IT IS A SEPARATE TYPE FROM GeoFenceSetSnapshot ON PURPOSE, AND THE SEPARATION IS
// WHAT KEEPS THE TWO FORMS FROM BLEEDING INTO EACH OTHER. GeoFenceSetSnapshot is the
// HYDRATED form every caller sees — the GraphQL doors, the fence-set fact, the replay
// preview — and it carries geometry because that is what containment is evaluated
// against. This is what is written to the column, and it carries hashes because that is
// what makes a version's stored size a function of its fence count alone. One struct
// with both fields would be marshalled by whichever caller reached it first, and a
// snapshot written with geometry inline is indistinguishable from a correct one until
// the archive it should have referenced is needed and is not there.
type storedGeoFenceSetSnapshot struct {
	Version int32               `json:"version"`
	Fences  []storedGeoFenceRef `json:"fences"`
}

// storedGeoFenceRef is one fence as of a version, on disk: the token a rule names it by
// and the content address of the geometry it was frozen with.
type storedGeoFenceRef struct {
	Token string `json:"token"`
	Hash  string `json:"hash"`
}

// Data required to create or update a geofence. Geometry is the GeoFenceGeometry JSON
// document as a string, validated on write (kind, GeoJSON shape, coordinate ranges, and
// the vertex bound).
type GeoFenceCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Geometry    string
	Metadata    *string
}

// Search criteria for locating geofences.
type GeoFenceSearchCriteria struct {
	rdb.Pagination
}

// Results for geofence search.
type GeoFenceSearchResults struct {
	Results    []GeoFence
	Pagination rdb.SearchResultsPagination
}

// Kind decodes the fence's geometry kind, or "" when the document is unreadable. It is
// the only supported way to ask a stored fence what shape it is; the kind lives inside
// the document precisely so callers do not grow a second, drift-prone copy of it.
func (f *GeoFence) Kind() string {
	geom, err := f.DecodedGeometry()
	if err != nil {
		return ""
	}
	return geom.Kind
}

// DecodedGeometry parses the stored geometry envelope.
func (f *GeoFence) DecodedGeometry() (*GeoFenceGeometry, error) {
	if len(f.Geometry) == 0 {
		return nil, fmt.Errorf("geofence %q has no geometry", f.Token)
	}
	geom := &GeoFenceGeometry{}
	if err := json.Unmarshal(f.Geometry, geom); err != nil {
		return nil, fmt.Errorf("unable to parse geofence %q geometry: %w", f.Token, err)
	}
	return geom, nil
}

// geoJSONGeometry is the minimal RFC 7946 geometry-object shape this package needs:
// a type discriminator and raw coordinates decoded per type.
type geoJSONGeometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// jsonbRenderedLen reports how many bytes PostgreSQL will hand back for a canonical
// (whitespace-free) JSON document stored in a jsonb column.
//
// 🔴 IT EXISTS SO THE BOUND MEASURES WHAT DOWNSTREAM ACTUALLY CARRIES. jsonb does not
// round-trip bytes: it reprints the document, emitting ", " after every structural comma
// and ": " after every structural key colon. Measured against PostgreSQL 16, for a
// whitespace-free input the rendered length is exactly len(doc) + one byte per structural
// comma + one byte per structural colon — verified on five shapes including a real fence
// envelope (93 -> 106, 145 -> 158).
//
// Separators INSIDE string literals get no space, which is why this tracks quoting rather
// than counting bytes with strings.Count. The canonical documents this package builds
// contain no comma or colon inside any string, so the difference is unobservable today —
// but a scanner that is right for the wrong reason is one schema change away from being
// wrong, and the whole finding this guards against was a length measured on the wrong text.
func jsonbRenderedLen(doc []byte) int {
	n := len(doc)
	inString, escaped := false, false
	for _, c := range doc {
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case !inString && (c == ',' || c == ':'):
			n++
		}
	}
	return n
}

// geoFenceEnvelopeKeys / geoJSONGeometryKeys are the ONLY keys each object may carry.
//
// 🔴 AN UNPARSED KEY IS A STORAGE-AMPLIFICATION VECTOR, not a harmless extension point.
// json.Unmarshal into a struct silently ignores anything it does not know, so a document
// could carry any key at all — and one holding ten "1e131071" tokens is 1,256 bytes of
// request text and 1.3 MB of stored jsonb, a 1,045x amplification, accepted. Filling the
// intake guard with such tokens is hundreds of megabytes in a single row, re-marshalled
// into a new snapshot on every subsequent fence edit. A geometry document is a CONTRACT
// with exactly two readers (this validator and the DETECT compiler); accepting keys
// neither of them reads never bought anything.
var (
	geoFenceEnvelopeKeys = map[string]bool{"kind": true, "geometry": true}
	geoJSONGeometryKeys  = map[string]bool{"type": true, "coordinates": true}
)

// rejectUnknownKeys refuses any key outside the allowed set, naming the offender.
func rejectUnknownKeys(obj map[string]json.RawMessage, allowed map[string]bool, what string) error {
	for k := range obj {
		if !allowed[k] {
			return fmt.Errorf("%s carries unknown key %q; only %s are accepted (an unread key is "+
				"stored, snapshotted and published like any other, so it is storage that nothing "+
				"can ever use)", what, k, allowedKeyList(allowed))
		}
	}
	return nil
}

// allowedKeyList renders an allowed-key set for an error message, in a stable order so the
// message does not change between runs.
func allowedKeyList(allowed map[string]bool) string {
	keys := make([]string, 0, len(allowed))
	for k := range allowed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// canonicalGeoFenceGeometry / canonicalGeoJSONPolygon are the shapes the canonical document
// is re-emitted through. Marshalling a struct rather than pasting strings together is what
// keeps the output well-formed and escaped without this file owning an encoder.
type canonicalGeoFenceGeometry struct {
	Kind     string          `json:"kind"`
	Geometry json.RawMessage `json:"geometry"`
}

type canonicalGeoJSONPolygon struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// validateGeoFenceGeometry parses and validates an authored geometry document, returning
// the decoded envelope and the CANONICAL document to store. Everything it enforces is
// authoring-time validation of the FENCE — none of it ever runs against a device's reported
// position, which is stored whatever it says (the same discipline LocationDeclaration
// records).
//
// 🔴 IT RETURNS A DOCUMENT TO STORE, AND CALLERS MUST STORE THAT ONE. Validating one text
// and persisting another is how a size check stops meaning anything: the authored form can
// carry exponent notation, insignificant whitespace and key orderings that the stored form
// does not, so a bound applied to the request is a statement about a document nobody keeps.
// Canonicalising closes the gap by making "what was validated" and "what is stored" the
// same bytes — every coordinate re-emitted from its parsed float64 in the same
// non-exponent form PostgreSQL's numeric_out will print.
func validateGeoFenceGeometry(raw string) (*GeoFenceGeometry, string, error) {
	if raw == "" {
		return nil, "", fmt.Errorf("a geofence geometry is required")
	}
	// The intake guard, first, because it is an O(1) read of a length where everything
	// below walks the document. It is NOT the size bound — see maxGeoFenceRequestBytes.
	if len(raw) > maxGeoFenceRequestBytes {
		return nil, "", fmt.Errorf("geofence geometry request is %d bytes; the intake limit is %d",
			len(raw), maxGeoFenceRequestBytes)
	}
	if !json.Valid([]byte(raw)) {
		return nil, "", fmt.Errorf("geofence geometry is not valid JSON")
	}
	// Reject an array/scalar/null before decoding into the envelope. `null` unmarshals
	// into a struct as a silent no-op, so it would otherwise arrive as an envelope with
	// an empty kind and produce a confusing "unsupported kind" error for what is really
	// a malformed document.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil || probe == nil {
		return nil, "", fmt.Errorf("geofence geometry must be a JSON object")
	}
	if err := rejectUnknownKeys(probe, geoFenceEnvelopeKeys, "geofence geometry"); err != nil {
		return nil, "", err
	}

	geom := &GeoFenceGeometry{}
	if err := json.Unmarshal([]byte(raw), geom); err != nil {
		return nil, "", fmt.Errorf("unable to parse geofence geometry: %w", err)
	}
	var canonicalGeometry json.RawMessage
	switch geom.Kind {
	case GeoFenceKindPolygon2D:
		c, err := canonicalizePolygon2D(geom.Geometry)
		if err != nil {
			return nil, "", err
		}
		canonicalGeometry = c
	case "":
		return nil, "", fmt.Errorf("geofence geometry must declare a kind (%q)", GeoFenceKindPolygon2D)
	case GeoFenceKindPolygon25D, GeoFenceKindVoxel3D:
		// Named, reserved, and NOT accepted. Storing one would mean a rule could name a
		// fence whose containment nothing can evaluate — worse than refusing it, because
		// the rule would silently never fire.
		return nil, "", fmt.Errorf("geofence geometry kind %q is reserved but not yet supported; use %q",
			geom.Kind, GeoFenceKindPolygon2D)
	default:
		return nil, "", fmt.Errorf("unsupported geofence geometry kind %q (supported: %q)",
			geom.Kind, GeoFenceKindPolygon2D)
	}

	canonical, err := json.Marshal(&canonicalGeoFenceGeometry{Kind: geom.Kind, Geometry: canonicalGeometry})
	if err != nil {
		return nil, "", fmt.Errorf("unable to canonicalize geofence geometry: %w", err)
	}
	// The real bound, applied to the STORED size of the CANONICAL document.
	if stored := jsonbRenderedLen(canonical); stored > MaxGeoFenceGeometryBytes {
		return nil, "", fmt.Errorf("geofence geometry stores as %d bytes; the limit is %d (a fence set "+
			"is carried whole over seams with byte budgets, and neither a position count nor the "+
			"request's own length bounds what a database stores — an exponent-notation coordinate "+
			"expands when it is written)", stored, MaxGeoFenceGeometryBytes)
	}
	geom.Geometry = canonicalGeometry
	return geom, string(canonical), nil
}

// canonicalizePolygon2D enforces the GeoJSON Polygon contract and the vertex bound, and
// returns the CANONICAL geometry object to store. The coordinate ranges are the
// platform-wide contract ResolvedLocationEntry states: WGS84 / EPSG:4326 decimal degrees,
// longitude in [-180, 180], latitude in [-90, 90].
func canonicalizePolygon2D(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("geofence geometry is missing its GeoJSON geometry object")
	}
	var geomProbe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &geomProbe); err != nil || geomProbe == nil {
		return nil, fmt.Errorf("the GeoJSON geometry must be a JSON object")
	}
	if err := rejectUnknownKeys(geomProbe, geoJSONGeometryKeys, "the GeoJSON geometry object"); err != nil {
		return nil, err
	}
	var g geoJSONGeometry
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, fmt.Errorf("unable to parse the GeoJSON geometry object: %w", err)
	}
	if g.Type != "Polygon" {
		return nil, fmt.Errorf("geometry kind %s requires a GeoJSON Polygon, got %q", GeoFenceKindPolygon2D, g.Type)
	}
	var rings [][][]float64
	if err := json.Unmarshal(g.Coordinates, &rings); err != nil {
		return nil, fmt.Errorf("unable to parse Polygon coordinates: %w", err)
	}
	if len(rings) == 0 {
		return nil, fmt.Errorf("a Polygon requires at least an exterior ring")
	}

	// 🔴 THE BUDGET IS ENFORCED FIRST, BEFORE ANYTHING QUADRATIC RUNS, and that
	// ordering is the whole point of this block being separate from the loop below.
	//
	// ValidateClosedRing does an O(V²) crossing scan. Counting positions is O(V).
	// With the scan ahead of the budget, a single authenticated create carrying an
	// oversized ring burns CPU proportional to the square of whatever fits in the
	// request body — measured on this predicate: 512 positions 11.6ms, 4k 0.72s,
	// 16k 11.5s, 32k 48.7s, and the 4 MiB GraphQL body limit admits ~170k, i.e.
	// tens of minutes per request, repeatable. Refusing on the cheap count first
	// makes the expensive scan reachable only for rings small enough to store.
	//
	// An earlier version of this function had the two the other way round while
	// carrying a comment claiming this exact virtue.
	total := 0
	for _, ring := range rings {
		total += len(ring)
	}
	if total > MaxGeoFenceVertices {
		return nil, fmt.Errorf("geofence has %d positions across its rings; the limit is %d (containment is "+
			"O(vertices) per location event and the publish-time cost gate cannot see this number)",
			total, MaxGeoFenceVertices)
	}

	for i, ring := range rings {
		// Four positions is the GeoJSON minimum for a closed linear ring: a triangle
		// plus the repeated closing position.
		if len(ring) < 4 {
			return nil, fmt.Errorf("polygon ring %d has %d positions; a closed ring needs at least 4", i, len(ring))
		}
		for j, pos := range ring {
			// EXACTLY two ordinates, not "at least". See MaxGeoFencePositionOrdinates: the
			// minimum this used to be is what let a position be arbitrarily wide, and a wide
			// position is unbounded storage that nothing ever reads.
			if len(pos) != MaxGeoFencePositionOrdinates {
				return nil, fmt.Errorf("polygon ring %d position %d has %d ordinates; a %s position is "+
					"exactly [longitude, latitude] (%d)", i, j, len(pos), GeoFenceKindPolygon2D,
					MaxGeoFencePositionOrdinates)
			}
			lon, lat := pos[0], pos[1]
			if math.IsNaN(lon) || math.IsInf(lon, 0) || lon < -180 || lon > 180 {
				return nil, fmt.Errorf("polygon ring %d position %d longitude %v is outside [-180, 180]", i, j, lon)
			}
			if math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 {
				return nil, fmt.Errorf("polygon ring %d position %d latitude %v is outside [-90, 90]", i, j, lat)
			}
		}
		first, last := ring[0], ring[len(ring)-1]
		if first[0] != last[0] || first[1] != last[1] {
			return nil, fmt.Errorf("polygon ring %d is not closed: first position %v != last %v", i, first, last)
		}
		// Everything above is STRUCTURE — the ring is well-formed JSON describing
		// positions in range. This asks the separate question of whether the shape
		// it describes bounds an area at all, and it is the same predicate the
		// detection engine applies when it compiles the fence.
		//
		// 🔴 Placed HERE deliberately: after the range checks above, which is what
		// keeps out-of-range degrees away from the spherical conversion, and after
		// the position budget above, which is what keeps the quadratic scan off
		// rings too big to store.
		//
		// Until this existed, a bow-tie saved cleanly and failed only later, when a
		// rule named the fence — so the author learned at detection time about a
		// mistake made at draw time, and the fence sat in the registry looking
		// healthy while answering nothing.
		if err := geo.ValidateClosedRing(ring); err != nil {
			return nil, fmt.Errorf("polygon ring %d: %w", i, err)
		}
	}
	return json.Marshal(&canonicalGeoJSONPolygon{
		Type:        "Polygon",
		Coordinates: canonicalRingsJSON(rings),
	})
}

// canonicalRingsJSON re-emits parsed rings as JSON, every ordinate in NON-EXPONENT decimal
// form.
//
// 🔴 IT DOES NOT USE json.Marshal ON THE FLOATS, AND THAT IS THE POINT. encoding/json
// formats a float with 'g'-like rules, so 1e-300 marshals back as "1e-300" — five bytes
// here and 302 in the jsonb column, which is precisely the gap that made the previous byte
// bound measure the wrong document. FormatFloat with 'f' and precision -1 emits the
// shortest decimal that round-trips exactly, in the same notation numeric_out uses, so the
// length measured here is the length PostgreSQL will store. No value is rounded: an author's
// coordinate survives this unchanged, and one written so small that its decimal expansion is
// enormous is refused by the size bound rather than silently truncated.
func canonicalRingsJSON(rings [][][]float64) json.RawMessage {
	var b strings.Builder
	b.WriteByte('[')
	for i, ring := range rings {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('[')
		for j, pos := range ring {
			if j > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('[')
			for k, ord := range pos {
				if k > 0 {
					b.WriteByte(',')
				}
				b.WriteString(strconv.FormatFloat(ord, 'f', -1, 64))
			}
			b.WriteByte(']')
		}
		b.WriteByte(']')
	}
	b.WriteByte(']')
	return json.RawMessage(b.String())
}
