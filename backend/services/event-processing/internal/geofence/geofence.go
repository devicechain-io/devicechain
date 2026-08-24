// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package geofence is DETECT's containment layer: it turns the FROZEN geofence-set snapshot
// device-management minted at a fence-set version (ADR-078) into an evaluable fence set, and
// answers ONE question about it — "is this position inside the fence named by this token?".
//
// 🔴 ONE PREDICATE, N GEOMETRY KINDS. The geometry kind belongs to the FENCE, never to the rule
// language. A rule says "is this device inside fence X", and that sentence does not change when
// X becomes a polygon with an altitude band or a set of voxels — so a new kind lands as a new
// entry in the `containment` dispatch table below and changes no rule, no compiled predicate,
// and no stored fence. The failure this shape avoids is concrete: had voxel fencing arrived as
// its own `inVoxelRegion()` beside `inFence()`, upgrading a fence from 2D to 2.5D would be a
// RULE migration (every rule naming that fence must be re-authored and re-published) rather than
// a fence edit, and every new kind would widen the DETECT environment separately.
//
// Only POLYGON_2D is implemented — device-management rejects the other kinds on write — but the
// dispatch is kind-agnostic today, which is the only moment it is free.
//
// It is deliberately free of any device-management dependency (it takes the snapshot's fences as
// neutral token+GeoJSON pairs), matching the leaf-layer discipline the predicate package keeps,
// so containment is unit-testable without a database or a resolved event.
package geofence

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// Geometry kind discriminators. They MUST equal device-management's GeoFenceKind* string
// values — the stored envelope carries the kind, so a rename upstream that is not mirrored here
// would make every fence of that kind fail to compile. A test pins the equality.
const (
	// KindPolygon2D is a 2D polygon on the WGS84 ellipsoid: a GeoJSON Polygon, exterior ring
	// first, optional interior rings (holes). The only kind implemented.
	KindPolygon2D = "POLYGON_2D"
	// KindPolygon25D is RESERVED (polygon + altitude band) and has no evaluator yet.
	KindPolygon25D = "POLYGON_2_5D"
	// KindVoxel3D is RESERVED (a set of cuboids) and has no evaluator yet.
	KindVoxel3D = "VOXEL_3D"
)

// ErrUnknownFence is returned when a rule names a fence the frozen snapshot does not hold —
// the fence did not exist at the version the event was stamped with (created later, or deleted
// earlier), or the token is a typo.
//
// 🔴 IT IS AN ERROR, NOT A FALSE, and the difference is the whole reason a rule author ever
// finds out. A false is a positive statement ("the device is not inside that region") which for
// a Duration rule CANCELS an in-flight hold and for every kind reads as a quiet, healthy,
// never-firing rule. An error makes the runtime SKIP the sample (leaving a hold intact), counts
// it on the eval-error counter, and — the surface that matters — shows up on the authoring
// preview attributed to the rule that caused it. A rule naming a deleted fence is broken; it
// should be loud, not silent. It follows the same convention CEL itself uses for an unguarded
// read of an absent map key.
var ErrUnknownFence = errors.New("no such geofence in the fence set")

// ErrNoFenceSet is returned when the fence set of the event's stamped version could not be
// resolved at all — distinct from "the fence is not in it". It means the answer is UNKNOWABLE
// (the projection has evicted that version and the source could not re-read it), not that the
// device is outside.
var ErrNoFenceSet = errors.New("the fence set for this event's stamped version is not available")

// ErrNoPosition is returned when containment is asked of an event that reports no position.
// The runtime normally prevents this by feeding a fence-referencing rule only events that carry
// one (rules.CompiledRule.RequiresPosition), so reaching it means the feed scope was bypassed.
var ErrNoPosition = errors.New("the event carries no position")

// BoundaryToleranceRadians is the half-width of the band around a fence's rings that counts as
// ON the boundary.
//
// 🔴 THE BOUNDARY IS INSIDE. That is the convention, it is held for every ring — a point on a
// hole's ring is inside the fence too, because a hole's ring is part of the fence's boundary —
// and it is not what S2 alone gives you. S2's own point-in-loop predicate is exact and
// deterministic but splits the boundary between the two adjacent regions, so a point sitting
// exactly on one edge of a square answers "inside" while a point on the adjacent edge answers
// "outside". Measured, not assumed: for the unit square [0,1]², s2.Loop.ContainsPoint answers
// false at (0.5, 0.0) and true at (0.5, 1.0). A fence whose answer depends on WHICH edge you
// stand on is not a contract anyone can hold, so containment adds an explicit boundary test.
//
// 1e-9 radians is ~6 mm on Earth's surface — four orders of magnitude below the accuracy of any
// positioning technology a device reports with, so the band is invisible in practice while
// keeping the exactly-on-the-edge case from turning on the last bits of a float64 (an
// on-edge point measures ~1e-16 rad from its edge, not 0).
const BoundaryToleranceRadians = 1e-9

// Position is one reported position, in the platform-wide coordinate contract device-management's
// ResolvedLocationEntry fixes: WGS84 / EPSG:4326 decimal degrees, elevation in metres above the
// ELLIPSOID. Elevation is carried even though no implemented kind reads it — it is what the
// reserved 2.5D/3D branches consume, and dropping it here would make adding them a change to the
// event projection rather than to this package.
type Position struct {
	Lat          float64
	Lon          float64
	Elevation    float64
	HasElevation bool
}

// SnapshotFence is one fence as it appears in a frozen fence-set snapshot: the token a rule names
// it by, and the stored geometry envelope verbatim. It mirrors device-management's
// GeoFenceSnapshotRef without depending on it.
type SnapshotFence struct {
	Token    string
	Geometry json.RawMessage
}

// geometryEnvelope is the stored self-describing document: a kind discriminator and one GeoJSON
// geometry object. It matches device-management's GeoFenceGeometry.
type geometryEnvelope struct {
	Kind     string          `json:"kind"`
	Geometry json.RawMessage `json:"geometry"`
}

// geometry is one compiled, evaluable fence shape. Every kind implements it; the compiler that
// produces one is registered in `containment`.
type geometry interface {
	contains(p Position) (bool, error)
}

// containment maps a geometry kind to the compiler that turns its stored GeoJSON envelope into an
// evaluable geometry. IT IS THE EXTENSION POINT: adding 2.5D or voxel fencing is one entry here
// plus its evaluator, with no change to the CEL environment, to any authored rule, or to any
// fence already stored. It is a package-level var rather than a switch so a test can prove the
// dispatch really is kind-agnostic by registering a kind of its own — a switch can only ever be
// tested with the kinds that are already in it.
var containment = map[string]func(raw json.RawMessage) (geometry, error){
	KindPolygon2D: compilePolygon2D,
}

// Fence is one compiled fence of a frozen fence set.
//
// A fence whose geometry could not be compiled is RETAINED with its error rather than dropped, and
// that is deliberate: dropping it would make it indistinguishable from a fence that was never
// authored (ErrUnknownFence), and failing the whole set would let one malformed fence disable
// containment for every other fence the tenant owns. The error surfaces only when a rule actually
// names this fence.
type Fence struct {
	token string
	kind  string
	geom  geometry
	err   error
}

// Token is the stable per-tenant token a rule names this fence by.
func (f *Fence) Token() string { return f.token }

// Kind is the fence's geometry kind (a Kind* constant, or whatever the stored envelope declared).
func (f *Fence) Kind() string { return f.kind }

// FenceSet is one tenant's fence set FROZEN at a fence-set version — the fences that were live at
// the instant an event stamped with that version was resolved. It is immutable once built and is
// read concurrently only in the sense that the single-writer loop holds it across a fan-out.
type FenceSet struct {
	version int32
	byToken map[string]*Fence
}

// Version is the fence-set version this set is the frozen state of.
func (fs *FenceSet) Version() int32 { return fs.version }

// Len is how many fences the set holds.
func (fs *FenceSet) Len() int { return len(fs.byToken) }

// Fence returns the named fence, or nil when the set does not hold it.
func (fs *FenceSet) Fence(token string) *Fence {
	if fs == nil {
		return nil
	}
	return fs.byToken[token]
}

// NewFenceSet compiles a frozen snapshot into an evaluable fence set. It never fails on a single
// bad fence (see Fence): a fence whose envelope or geometry cannot be compiled is retained with
// its error and reports it only if a rule names it. A duplicate token keeps the first (the mint
// path orders by token and the per-tenant unique index forbids duplicates, so this only guards a
// malformed snapshot).
func NewFenceSet(version int32, fences []SnapshotFence) *FenceSet {
	fs := &FenceSet{version: version, byToken: make(map[string]*Fence, len(fences))}
	for _, sf := range fences {
		if sf.Token == "" {
			continue
		}
		if _, dup := fs.byToken[sf.Token]; dup {
			continue
		}
		fs.byToken[sf.Token] = compileFence(sf)
	}
	return fs
}

// EmptyFenceSet is the KNOWN-EMPTY fence set of a version — what version 0 resolves to (the
// tenant had never created a fence when the event was resolved), and what the deletion of a
// tenant's last fence mints.
//
// 🔴 A KNOWN-EMPTY SET IS NOT AN ABSENT ONE. Containment against this set answers
// ErrUnknownFence ("that fence did not exist"), which is knowledge; containment against a nil
// set answers ErrNoFenceSet ("cannot know"). Collapsing the two is how a projection miss would
// come to read as "the tenant has no fences".
func EmptyFenceSet(version int32) *FenceSet {
	return &FenceSet{version: version, byToken: map[string]*Fence{}}
}

// compileFence compiles one snapshot fence's INLINED geometry envelope and names the result with
// the fence's token.
//
// The decode and the kind dispatch live in CompileGeometry, which the content-addressed delivery
// path also runs. Sharing them is the point: an inlined snapshot and a fetched geometry body are
// the same document travelling by different roads, and two copies of this decode would be two
// places for the kind vocabulary — or the refusal of an unevaluable kind — to drift apart, with
// the divergence visible only on whichever road a given tenant's fence set happened to take.
//
// The token is attached HERE rather than inside CompileGeometry because a document is shared by
// every fence that resolves to its hash, so its failure is not a fact about any one token; this
// wrapper is the point at which the shared fact becomes a per-fence one.
func compileFence(sf SnapshotFence) *Fence {
	c, err := CompileGeometry(sf.Geometry)
	if err != nil {
		// The kind is retained even on failure — Fence.Kind() answers for a broken fence too, and
		// "your POLYGON_2D fence is malformed" is a better sentence than "your fence is malformed".
		return &Fence{token: sf.Token, kind: c.Kind(), err: fmt.Errorf("geofence %q: %w", sf.Token, err)}
	}
	return NewCompiledFence(sf.Token, c)
}

// Contains answers whether the position is inside the fence the token names, dispatching on the
// FENCE's geometry kind. The caller (the CEL `inFence` binding) never learns the kind — that is
// the point of the shape.
//
// A nil receiver means the fence set for the event's stamped version could not be resolved:
// ErrNoFenceSet, not false. A token the set does not hold: ErrUnknownFence, not false. See those
// sentinels for why neither degrades to a boolean.
func (fs *FenceSet) Contains(token string, p Position) (bool, error) {
	if fs == nil {
		return false, ErrNoFenceSet
	}
	f := fs.byToken[token]
	if f == nil {
		return false, fmt.Errorf("%w: %q (fence set version %d holds %d fences)",
			ErrUnknownFence, token, fs.version, len(fs.byToken))
	}
	if f.err != nil {
		return false, f.err
	}
	return f.geom.contains(p)
}

// valid reports whether a position is usable as a point on the sphere. Latitude/longitude ranges
// were enforced at decode (device-management's ResolvedLocationEntry contract), so this guards
// only the values that would silently produce a garbage unit vector rather than re-validating.
func (p Position) valid() bool {
	return !math.IsNaN(p.Lat) && !math.IsInf(p.Lat, 0) && !math.IsNaN(p.Lon) && !math.IsInf(p.Lon, 0)
}
