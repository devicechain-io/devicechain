// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geofence

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Compiled is one geometry document after compilation: the KIND the stored envelope declared, and
// the evaluable shape that kind's compiler produced.
//
// 🔴 THE PAIR IS THE UNIT, AND SPLITTING IT LOSES Fence.Kind(). A bare compiled shape answers
// containment perfectly well and cannot say what it is, so a fence assembled from one would report
// an empty kind — which is the value a fence whose envelope carried NO kind reports, i.e. the two
// become indistinguishable on the surfaces that exist to tell an author what their fence actually
// is (the authoring UI, the eval-error message). Carrying the discriminator alongside the shape
// costs one string per distinct document and removes the choice.
//
// A Compiled is IMMUTABLE and safe to share: copying one shares the same underlying compiled
// geometry rather than duplicating it, which is the entire point of caching it (a fence set built
// for version 9 and one built for version 7 reference the same loops when the fence did not
// change). Sharing is safe for concurrent readers only because the geometry's lazily-built spatial
// index has been forced before the value is published — see Prebuild.
//
// The zero Compiled carries no geometry. It is what a failed compilation returns, and every
// constructor that consumes one refuses it rather than building a fence that would nil-dereference
// on its first answer.
type Compiled struct {
	kind string
	geom geometry
}

// compiledDetail is the OPTIONAL half of a geometry's contract: what a kind must implement to be
// CACHEABLE, as opposed to merely evaluable.
//
// 🔴 IT IS A SEPARATE, OPTIONAL INTERFACE RATHER THAN TWO MORE METHODS ON `geometry`, and the
// reason is the dispatch table's own test. `containment` is a package-level var precisely so a
// test can register a kind of its own and prove the dispatch is genuinely kind-agnostic; widening
// the mandatory interface would make that fake — and every future one — carry index-building
// machinery it has no index for, which is a tax on the one test that keeps the extension point
// honest.
//
// The cost of optionality is that a real kind could silently skip it, so two things close that
// gap: Vertices charges an uncounted geometry a non-zero floor (it must never be free to cache),
// and a test walks `containment` and requires every REGISTERED kind to implement this, so a new
// kind that forgets fails out loud instead of quietly becoming unbounded.
type compiledDetail interface {
	// vertexCount is the geometry's total vertex count across every ring or part, in COMPILED
	// vertices — one fewer per ring than the authored position count, since a GeoJSON ring's
	// repeated closing position is dropped at compile (see loopFromRing).
	vertexCount() int
	// prebuildIndex forces any lazily-built spatial index the shape holds, so that the first
	// concurrent reader is not the one that builds it. See Compiled.Prebuild for why.
	prebuildIndex()
}

// uncountedGeometryCost is what Vertices charges a geometry that does not implement
// compiledDetail. It is 1 rather than 0 because a cache bounded by vertices would treat 0 as FREE:
// unboundedly many uncountable entries would fit inside any bound, which is the one way a bound
// stops being a bound. 1 is the smallest charge that keeps the entry count finite.
const uncountedGeometryCost = 1

// CompileGeometry compiles one STORED GEOMETRY DOCUMENT — the canonical {kind, geometry} envelope
// device-management archives under its content address — into a reusable compiled value.
//
// It runs the same decode and the same kind dispatch NewFenceSet runs; the difference is only that
// this door is addressed to a DOCUMENT rather than to a fence, so its errors do not name a token.
// That distinction is not cosmetic: one document is shared by every fence and every fence-set
// version that resolves to its hash, so attributing its compile failure to whichever token
// happened to ask first would report a shared fact as a per-fence one.
//
// 🔴 ON ERROR THE RETURNED VALUE MAY STILL CARRY THE KIND, and never carries a geometry. The kind
// is known as soon as the envelope parses, i.e. before any of the ways compilation can fail, and
// keeping it lets a caller say "this POLYGON_2D document is broken" rather than "this document is
// broken". Callers must not read a non-empty Kind as success: the error is the only success
// signal, and NewCompiledFence turns such a value into an error fence rather than a live one.
func CompileGeometry(document []byte) (Compiled, error) {
	if len(document) == 0 {
		return Compiled{}, errors.New("the geometry document is empty")
	}
	env := geometryEnvelope{}
	if err := json.Unmarshal(document, &env); err != nil {
		return Compiled{}, fmt.Errorf("unreadable geometry envelope: %w", err)
	}
	c := Compiled{kind: env.Kind}
	compile, ok := containment[env.Kind]
	if !ok {
		// A kind with no evaluator is an ERROR, never a false: answering "not inside" for a shape
		// nothing can evaluate is the one outcome indistinguishable from a correct negative.
		// device-management refuses to store such a fence, so reaching this means the two sides
		// disagree about the kind vocabulary — which is exactly what must be loud.
		return c, fmt.Errorf("geometry kind %q cannot be evaluated by this engine", env.Kind)
	}
	geom, err := compile(env.Geometry)
	if err != nil {
		return c, fmt.Errorf("geometry kind %q could not be compiled: %w", env.Kind, err)
	}
	c.geom = geom
	return c, nil
}

// Kind is the geometry kind the document declared (a Kind* constant, or whatever the envelope
// carried). It is empty only when the envelope itself could not be read.
func (c Compiled) Kind() string { return c.kind }

// Vertices is the compiled geometry's total vertex count — the cost unit a geometry cache is
// bounded by.
//
// 🔴 VERTEX COUNT, NOT ENTRY COUNT, IS THE QUANTITY THAT MEANS ANYTHING HERE. Authoring admits
// fences from 3 compiled vertices to 511, so two entries can differ in cost by more than two
// orders of magnitude; a cache holding "1000 entries" therefore holds anywhere between a few
// kilobytes and most of a tenant's authoring ceiling, and the number that was supposed to bound it
// predicts neither. Containment cost is O(vertices) too, so the same unit prices both the memory
// and the work.
//
// The zero Compiled — and any value returned alongside a compile error — reports 0, which makes
// "was this worth storing" and "did this compile" the same question. A geometry that does not
// implement compiledDetail is charged uncountedGeometryCost rather than nothing; see it.
func (c Compiled) Vertices() int {
	if c.geom == nil {
		return 0
	}
	if d, ok := c.geom.(compiledDetail); ok {
		return d.vertexCount()
	}
	return uncountedGeometryCost
}

// Prebuild forces any lazily-built spatial index the compiled geometry holds, by running one
// throwaway containment query per compiled loop.
//
// 🔴 IT IS RUN BY WHOEVER PUBLISHES THE VALUE, ON ITS OWN GOROUTINE, BEFORE ANYONE ELSE CAN SEE
// IT — and it is not a micro-optimization. s2 builds a Loop's shape index on first use for loops
// above its brute-force threshold, and three separate things make that lazy build the wrong thing
// to leave in the hot path once a compiled geometry is SHARED:
//
//   - Concurrent query on a shared s2.Loop is safe in the pinned version — the lazy build is
//     mutex-guarded with an atomic status flag — but golang/geo publishes no tags, so the pin is a
//     pseudo-version and every bump re-opens that analysis. Pre-building means the answer stops
//     mattering.
//   - That lock is not double-checked: a waiter that blocks on the build re-runs it in full after
//     acquiring the mutex, so N simultaneous first readers do N builds rather than one.
//   - The single-writer evaluation loop could otherwise block inside s2 behind an authoring
//     preview's goroutine, which is precisely the "one tenant's preview stalls every tenant's
//     event processing" coupling the loop's whole design exists to prevent.
//
// It is idempotent, and safe to call on a geometry that has no index (small loops never build one)
// and on the zero Compiled.
func (c Compiled) Prebuild() {
	if c.geom == nil {
		return
	}
	if d, ok := c.geom.(compiledDetail); ok {
		d.prebuildIndex()
	}
}

// NewCompiledFence builds the fence a rule names by token, over geometry that is ALREADY compiled.
//
// It is the assembly door for a fence set built from a content-addressed geometry cache rather
// than from inlined snapshot bytes: one document is compiled once, and the fences of every version
// that references it share the result.
//
// A Compiled carrying no geometry — the zero value, or one returned alongside a compile error —
// produces an ERROR FENCE, not a live one. That refusal is structural on purpose: a Fence with a
// nil geometry AND a nil error is the one shape Contains cannot survive (it dereferences the
// geometry), so the way to be sure it is never constructed is to make the constructor unable to
// construct it.
func NewCompiledFence(token string, c Compiled) *Fence {
	if c.geom == nil {
		return NewErrorFence(token, fmt.Errorf("geofence %q was assembled from a geometry that did not compile", token))
	}
	return &Fence{token: token, kind: c.kind, geom: c.geom}
}

// NewErrorFence builds the fence a rule names by token carrying err instead of geometry — how a
// fence whose body could not be resolved is represented.
//
// 🔴 AN UNRESOLVED FENCE MUST READ AS AN ERROR, NEVER AS ABSENT AND NEVER AS OUTSIDE. Those are
// three different sentences and only one of them is true:
//
//   - Omitting it from the set makes Contains answer ErrUnknownFence — "that fence did not exist
//     at this version" — which is a claim about AUTHORING history that is simply false, and it
//     points the author at their rule instead of at the delivery that failed.
//   - Answering false makes it "the device is not in that region", which for a Duration rule
//     CANCELS an in-flight hold and, for every kind, reads as a quiet, healthy, never-firing rule.
//
// An error makes the runtime SKIP the sample, leaving a hold intact, and counts it on the
// eval-error counter — so an unresolvable geometry body surfaces as the delivery fault it is
// rather than as an answer. A nil err is replaced rather than honoured: a Fence with neither
// geometry nor error is exactly the absent-reading fence this constructor exists to make
// unrepresentable.
func NewErrorFence(token string, err error) *Fence {
	if err == nil {
		err = fmt.Errorf("geofence %q could not be resolved (no reason was recorded)", token)
	}
	return &Fence{token: token, err: err}
}

// NewFenceSetFromFences assembles an evaluable fence set from fences that are ALREADY built —
// the manifest road, where a version's geometry arrives as content addresses resolved one body
// at a time rather than as one inlined snapshot.
//
// It is a sibling of NewFenceSet, not a replacement, and the split is on where the geometry
// comes from rather than on what the result is. NewFenceSet takes raw documents and compiles
// them itself, which is right when a single message carried the whole set. Here the caller has
// already resolved each fence through a cache and knows, per fence, whether it got a body — so
// it arrives holding compiled fences and errored ones mixed together, and this must not try to
// re-derive that distinction.
//
// 🔴 A FENCE THE CALLER COULD NOT RESOLVE MUST ARRIVE AS NewErrorFence, NEVER BE OMITTED, and
// this function cannot check that for you — an omitted fence is indistinguishable here from a
// fence the version genuinely does not contain. That is the one failure splitting geometry out
// of the fact introduces: a missing fence reads downstream as "no such fence", which containment
// answers by reporting a rule naming something that does not exist, while an errored fence
// reports that it could not be evaluated. The first looks like a healthy rule that never fires;
// the second lands on the eval-error counter. Same hole in the set, opposite visibility.
//
// Duplicate tokens keep the first and nil entries are skipped, matching NewFenceSet: the mint
// path orders by token and the per-tenant unique index forbids duplicates, so both only guard a
// malformed manifest.
func NewFenceSetFromFences(version int32, fences []*Fence) *FenceSet {
	fs := &FenceSet{version: version, byToken: make(map[string]*Fence, len(fences))}
	for _, f := range fences {
		if f == nil || f.token == "" {
			continue
		}
		if _, dup := fs.byToken[f.token]; dup {
			continue
		}
		fs.byToken[f.token] = f
	}
	return fs
}
