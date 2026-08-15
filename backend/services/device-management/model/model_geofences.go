// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"math"
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

// Bounds enforced at AUTHORING time. Both exist for one reason:
//
// 🔴 THE PUBLISH-TIME CEL COST GATE CANNOT SEE EITHER OF THESE NUMBERS. It estimates an
// expression's cost STATICALLY, from the expression tree. A containment call's real cost
// is data-dependent — how many vertices the named fence has, and how many fences are in
// scope — and neither is in the tree. A static estimator therefore prices `inFence(…)`
// as one call whether it tests a 4-vertex yard or a 40,000-vertex coastline. The cost
// has to be bounded where the data is written, which is here.
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
	// It also bounds the frozen fence-set snapshot minted on every fence change: 100
	// fences × 512 positions is ~1 MB of JSON in the worst case, written once per
	// authoring action, never on the hot path.
	MaxGeoFencesPerTenant = 100
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
// GeoFenceSetVersion.Snapshot. Like ProfileSnapshot it is never SQL-built, so the encoding
// need only be self-consistent — but unlike ProfileSnapshot it is no longer Go-internal:
// it is READ BACK on two seams, the geoFenceSetSnapshot / currentGeoFenceSet GraphQL doors
// and the fence-set fact (GeoFenceSetMintedEvent), because event-processing's containment
// projection is a live cache and this service is the archive it re-seeds from. Both seams
// hand out this document's CONTENT (token + geometry), never its bytes, so the encoding
// itself is still an implementation detail.
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

// validateGeoFenceGeometry parses and validates an authored geometry document,
// returning the decoded envelope. Everything it enforces is authoring-time validation
// of the FENCE — none of it ever runs against a device's reported position, which is
// stored whatever it says (the same discipline LocationDeclaration records).
func validateGeoFenceGeometry(raw string) (*GeoFenceGeometry, error) {
	if raw == "" {
		return nil, fmt.Errorf("a geofence geometry is required")
	}
	if !json.Valid([]byte(raw)) {
		return nil, fmt.Errorf("geofence geometry is not valid JSON")
	}
	// Reject an array/scalar/null before decoding into the envelope. `null` unmarshals
	// into a struct as a silent no-op, so it would otherwise arrive as an envelope with
	// an empty kind and produce a confusing "unsupported kind" error for what is really
	// a malformed document.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil || probe == nil {
		return nil, fmt.Errorf("geofence geometry must be a JSON object")
	}

	geom := &GeoFenceGeometry{}
	if err := json.Unmarshal([]byte(raw), geom); err != nil {
		return nil, fmt.Errorf("unable to parse geofence geometry: %w", err)
	}
	switch geom.Kind {
	case GeoFenceKindPolygon2D:
		if err := validatePolygon2D(geom.Geometry); err != nil {
			return nil, err
		}
	case "":
		return nil, fmt.Errorf("geofence geometry must declare a kind (%q)", GeoFenceKindPolygon2D)
	case GeoFenceKindPolygon25D, GeoFenceKindVoxel3D:
		// Named, reserved, and NOT accepted. Storing one would mean a rule could name a
		// fence whose containment nothing can evaluate — worse than refusing it, because
		// the rule would silently never fire.
		return nil, fmt.Errorf("geofence geometry kind %q is reserved but not yet supported; use %q",
			geom.Kind, GeoFenceKindPolygon2D)
	default:
		return nil, fmt.Errorf("unsupported geofence geometry kind %q (supported: %q)",
			geom.Kind, GeoFenceKindPolygon2D)
	}
	return geom, nil
}

// validatePolygon2D enforces the GeoJSON Polygon contract and the vertex bound. The
// coordinate ranges are the platform-wide contract ResolvedLocationEntry states: WGS84 /
// EPSG:4326 decimal degrees, longitude in [-180, 180], latitude in [-90, 90].
func validatePolygon2D(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("geofence geometry is missing its GeoJSON geometry object")
	}
	var g geoJSONGeometry
	if err := json.Unmarshal(raw, &g); err != nil {
		return fmt.Errorf("unable to parse the GeoJSON geometry object: %w", err)
	}
	if g.Type != "Polygon" {
		return fmt.Errorf("geometry kind %s requires a GeoJSON Polygon, got %q", GeoFenceKindPolygon2D, g.Type)
	}
	var rings [][][]float64
	if err := json.Unmarshal(g.Coordinates, &rings); err != nil {
		return fmt.Errorf("unable to parse Polygon coordinates: %w", err)
	}
	if len(rings) == 0 {
		return fmt.Errorf("a Polygon requires at least an exterior ring")
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
		return fmt.Errorf("geofence has %d positions across its rings; the limit is %d (containment is "+
			"O(vertices) per location event and the publish-time cost gate cannot see this number)",
			total, MaxGeoFenceVertices)
	}

	for i, ring := range rings {
		// Four positions is the GeoJSON minimum for a closed linear ring: a triangle
		// plus the repeated closing position.
		if len(ring) < 4 {
			return fmt.Errorf("polygon ring %d has %d positions; a closed ring needs at least 4", i, len(ring))
		}
		for j, pos := range ring {
			if len(pos) < 2 {
				return fmt.Errorf("polygon ring %d position %d needs at least [longitude, latitude]", i, j)
			}
			lon, lat := pos[0], pos[1]
			if math.IsNaN(lon) || math.IsInf(lon, 0) || lon < -180 || lon > 180 {
				return fmt.Errorf("polygon ring %d position %d longitude %v is outside [-180, 180]", i, j, lon)
			}
			if math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 {
				return fmt.Errorf("polygon ring %d position %d latitude %v is outside [-90, 90]", i, j, lat)
			}
		}
		first, last := ring[0], ring[len(ring)-1]
		if first[0] != last[0] || first[1] != last[1] {
			return fmt.Errorf("polygon ring %d is not closed: first position %v != last %v", i, first, last)
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
			return fmt.Errorf("polygon ring %d: %w", i, err)
		}
	}
	return nil
}
