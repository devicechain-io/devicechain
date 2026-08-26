// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The happy path of manifest delivery is covered next door. This file is the other half: every
// way a resolve can FAIL, and the decision taken at each one. Those decisions are not obvious —
// installing a fence that carries its error rather than omitting it, refusing the WHOLE version
// when the failure is about the read, acking a fact the archive would not answer, never
// negative-caching — and an untested decision is a decision nobody can change safely.
//
// 🔴 THE FAILURE TAXONOMY IS THE DESIGN, AND ITS TWO HALVES MUST NOT COLLAPSE INTO EACH OTHER:
//
//   - A failure ABOUT THE READ — transport, request budget, version skew, or a document that
//     did not hash to the address it was requested under — installs NOTHING. None of those
//     causes is per-fence: if the archive cannot answer for one body it cannot answer for any,
//     and installing a partial set would spend a retention slot on something uniformly useless.
//     The version simply does not arrive, which is what a failed archive read has always meant,
//     and the reconcile sweep retries it.
//   - A body the archive does not HOLD is per-fence, and that fence is installed CARRYING ITS
//     ERROR. This is the one failure mode that only exists because geometry stopped travelling
//     with the fact, and it must never be expressed as a MISSING fence: an absent fence reads
//     downstream as "no such fence", which is a rule naming something that does not exist and
//     is indistinguishable from a healthy rule that never fires. An errored fence reports that
//     it could not be evaluated and lands on the eval-error counter. Same hole, opposite
//     visibility, and only one of them is discoverable.

// ── a transport the test dictates ────────────────────────────────────────────────────────────

// scriptedTransport is a fenceSetExec whose every response is decided by the test. It stands in
// for device-management at the ONE seam the client depends on, which is what lets a test produce
// wire conditions a real server would not produce on request — a response the cap refuses, a
// document served under the wrong address, a peer that does not know the query.
//
// It answers from a FUNCTION rather than from a list of canned steps because the interesting
// behaviour here is a RETRY WITH A DIFFERENT REQUEST: a refused geometry batch is split and
// re-asked as two smaller ones, so a positional script would have to encode the split it is
// supposed to be measuring.
type scriptedTransport struct {
	t       *testing.T
	respond func(call int, query string, vars map[string]any) (string, error)

	calls   int
	queries []string
	// asked records the address list of every geometry request, in order. It is what makes the
	// chunking and the split observable: a test asserting only on the assembled set cannot tell
	// one request of four from the seven a full split costs.
	asked [][]string
}

func (x *scriptedTransport) exec(_ context.Context, _ string, query string, vars map[string]any, out any) error {
	x.calls++
	x.queries = append(x.queries, query)
	if hashes, ok := vars["hashes"].([]string); ok {
		x.asked = append(x.asked, append([]string(nil), hashes...))
	}
	data, err := x.respond(x.calls, query, vars)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), out)
}

// askedSizes reports the address count of each geometry request, in order.
func (x *scriptedTransport) askedSizes() []int {
	sizes := make([]int, 0, len(x.asked))
	for _, req := range x.asked {
		sizes = append(sizes, len(req))
	}
	return sizes
}

// testFenceGeometryMetrics builds the archive-seam recorder on PRIVATE collectors rather than
// through newFenceGeometryMetrics, which registers on the service's registry and would collide
// on the second test to build one.
//
// Every field is filled. The recorders are nil-safe against a nil RECEIVER, not against a nil
// field, so a future change that made a resolve touch another metric would panic here —
// loudly, which is the right failure. A partially-filled struct that silently absorbed the call
// would be the wrong one.
func testFenceGeometryMetrics() *fenceGeometryMetrics {
	counter := func(name string) prometheus.Counter {
		return prometheus.NewCounter(prometheus.CounterOpts{Name: name})
	}
	return &fenceGeometryMetrics{
		fenceGeometryUnresolved:     counter("test_fence_geometry_unresolved_total"),
		fenceGeometryHashMismatch:   counter("test_fence_geometry_hash_mismatch_total"),
		fenceArchiveSkew:            counter("test_fence_archive_skew_total"),
		fenceGeometryCacheHits:      counter("test_fence_geometry_cache_hits_total"),
		fenceGeometryCacheMisses:    counter("test_fence_geometry_cache_misses_total"),
		fenceGeometryCacheEvictions: counter("test_fence_geometry_cache_evictions_total"),
		fenceGeometryCacheVertices: prometheus.NewGauge(
			prometheus.GaugeOpts{Name: "test_fence_geometry_cache_vertices"}),
	}
}

// scriptedClient builds the REAL fence-set client over a scripted transport, its own cold
// compiled-geometry cache and private metrics.
func scriptedClient(t *testing.T,
	respond func(call int, query string, vars map[string]any) (string, error)) (*fenceSetClient, *scriptedTransport) {
	t.Helper()
	x := &scriptedTransport{t: t, respond: respond}
	c := &fenceSetClient{
		transport: x.exec,
		cache:     geofence.NewGeometryCache(geofence.DefaultMaxCachedVertices),
		metrics:   testFenceGeometryMetrics(),
	}
	return c, x
}

// ── wire-shape helpers ───────────────────────────────────────────────────────────────────────

// docHash is the content address of a geometry document — device-management's own function, so
// a test can never disagree with the archive about what an address is.
func docHash(document string) string { return dmmodel.GeoFenceGeometryHash([]byte(document)) }

// manifestData builds a manifest "data" object for whichever manifest door is being answered.
func manifestData(t *testing.T, field string, version int32, entries ...dmmodel.GeoFenceManifestEntry) string {
	t.Helper()
	if entries == nil {
		entries = []dmmodel.GeoFenceManifestEntry{}
	}
	raw, err := json.Marshal(map[string]any{
		field: map[string]any{"version": version, "fences": entries},
	})
	if err != nil {
		t.Fatalf("marshal manifest data: %v", err)
	}
	return string(raw)
}

// geometryDoc is one entry of a geoFenceGeometry response, in the shape the door serves: the
// document as a JSON STRING, escaped exactly as the wire escapes it.
type geometryDoc struct {
	Hash     string `json:"hash"`
	Geometry string `json:"geometry"`
}

// geometryData builds a geoFenceGeometry "data" object holding the given documents.
func geometryData(t *testing.T, docs ...geometryDoc) string {
	t.Helper()
	if docs == nil {
		docs = []geometryDoc{}
	}
	raw, err := json.Marshal(map[string]any{"geoFenceGeometry": docs})
	if err != nil {
		t.Fatalf("marshal geometry data: %v", err)
	}
	return string(raw)
}

// held builds a geometryDoc filed under its OWN address — a correctly stored document.
func held(document string) geometryDoc {
	return geometryDoc{Hash: docHash(document), Geometry: document}
}

// tooLarge is the error the production transport raises for a response over the read cap, and
// the ONLY error a geometry batch splits on.
func tooLarge() error { return fmt.Errorf("%w: synthetic", svcclient.ErrResponseTooLarge) }

// ── 1. failures ABOUT THE READ: nothing is installed ─────────────────────────────────────────

// A manifest read that fails installs nothing and reports the transport's error unchanged.
//
// Returning an empty set here instead would be the worst available answer: "this tenant has no
// fences" is a legitimate state that containment answers "outside" from, so a read failure
// dressed as one is a rule that quietly stops firing.
func TestAManifestReadFailureResolvesToNothing(t *testing.T) {
	boom := errors.New("device-management is down")
	c, x := scriptedClient(t, func(int, string, map[string]any) (string, error) {
		return "", boom
	})

	set, err := c.CurrentFenceSet(context.Background(), "acme")
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the transport error unchanged so the operator sees the real cause", err)
	}
	if set != nil {
		t.Errorf("a failed manifest read returned a set of %d fences; the caller turns nil into "+
			"'unknowable' and a set into truth", set.Len())
	}
	if x.calls != 1 {
		t.Errorf("the read spent %d requests on a terminal error, want 1", x.calls)
	}
}

// A geometry fetch that fails installs nothing, even though the manifest itself arrived fine.
//
// The manifest is not "most of the answer": a version whose bodies could not be fetched is a
// version with no evaluable geometry at all, and the reconcile sweep exists to retry it.
func TestAGeometryFetchFailureResolvesToNothing(t *testing.T) {
	yard := fenceBox(0, 0, 1, 1)
	boom := errors.New("device-management is down")
	c, x := scriptedClient(t, func(_ int, query string, _ map[string]any) (string, error) {
		if query == geoFenceGeometryQuery {
			return "", boom
		}
		return manifestData(t, "currentGeoFenceSetManifest", 4,
			dmmodel.GeoFenceManifestEntry{Token: "yard", Hash: docHash(yard)}), nil
	})

	set, err := c.CurrentFenceSet(context.Background(), "acme")
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the transport error unchanged", err)
	}
	if set != nil {
		t.Errorf("a failed geometry fetch returned a set of %d fences, want nothing", set.Len())
	}
	if x.calls != 2 {
		t.Errorf("the read spent %d requests, want 2 (the manifest, then one refused batch)", x.calls)
	}
}

// A response the peer refuses for being too large makes the batch SPLIT and re-ask, all the way
// down to a single address — and nothing already fetched is thrown away.
//
// 🔴 THIS IS WHAT MAKES THE READ TOTAL, AND IT IS TRIVIAL HERE IN A WAY IT WAS NOT FOR THE PAGED
// READ IT REPLACES. That walk addressed its work as (pageNumber, pageSize), which cannot be
// re-expressed at a different size, so a refusal forced it to restart the whole walk from page
// one. A chunk is a SET OF ADDRESSES, so half of it is still a well-formed request for exactly
// the items that have not been answered.
//
// The transport refuses everything above a single address, which is the worst case a reader has
// to survive: rows written before MaxGeoFenceGeometryBytes existed can be larger than the bound.
func TestARefusedGeometryBatchIsSplitDownToOneAddress(t *testing.T) {
	docs := []string{fenceBox(0, 0, 1, 1), fenceBox(2, 2, 3, 3), fenceBox(4, 4, 5, 5), fenceBox(6, 6, 7, 7)}
	entries := make([]dmmodel.GeoFenceManifestEntry, 0, len(docs))
	for i, d := range docs {
		entries = append(entries, dmmodel.GeoFenceManifestEntry{
			Token: fmt.Sprintf("f%d", i), Hash: docHash(d)})
	}
	byHash := map[string]string{}
	for _, d := range docs {
		byHash[docHash(d)] = d
	}

	c, x := scriptedClient(t, func(_ int, query string, vars map[string]any) (string, error) {
		if query != geoFenceGeometryQuery {
			return manifestData(t, "geoFenceSetManifest", 9, entries...), nil
		}
		hashes := vars["hashes"].([]string)
		if len(hashes) > 1 {
			return "", tooLarge()
		}
		return geometryData(t, held(byHash[hashes[0]])), nil
	})

	set, err := c.FenceSetAt(context.Background(), "acme", 9)
	if err != nil {
		t.Fatalf("the fetch gave up instead of splitting: %v", err)
	}
	if set.Len() != len(docs) {
		t.Fatalf("the split fetch produced %d fences, want %d", set.Len(), len(docs))
	}
	for i := range docs {
		token := fmt.Sprintf("f%d", i)
		if set.Fence(token) == nil {
			t.Errorf("fence %q is missing after the split", token)
		}
	}
	// The split is a BINARY subdivision of the same address set, so the sequence is a property
	// of the algorithm, not of the fixture: 4 refused, [0:2] refused, then its two halves, then
	// [2:4] refused, then its two halves.
	want := []int{4, 2, 1, 1, 2, 1, 1}
	got := x.askedSizes()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the fetch asked for address counts %v, want %v", got, want)
	}
}

// When even a SINGLE address is refused, the fetch stops and says which one. There is nothing
// left to subdivide, so a reader cannot recover — which is the argument for bounding a single
// fence's stored bytes at authoring, and this test is what would notice if that argument were
// ever quietly dropped.
func TestAGeometryTooLargeForOneResponseOnItsOwnIsAnError(t *testing.T) {
	doc := fenceBox(0, 0, 1, 1)
	hash := docHash(doc)
	c, x := scriptedClient(t, func(_ int, query string, _ map[string]any) (string, error) {
		if query == geoFenceGeometryQuery {
			return "", tooLarge()
		}
		return manifestData(t, "geoFenceSetManifest", 3,
			dmmodel.GeoFenceManifestEntry{Token: "yard", Hash: hash}), nil
	})

	_, err := c.FenceSetAt(context.Background(), "acme", 3)
	if !errors.Is(err, svcclient.ErrResponseTooLarge) {
		t.Fatalf("got %v, want the cap error unchanged so the operator sees the real cause", err)
	}
	if !strings.Contains(err.Error(), hash) {
		t.Errorf("the error does not name the offending document (%v); an operator cannot find "+
			"the row without it", err)
	}
	// One manifest read and exactly one geometry attempt: a single address has no halves, so a
	// reader that kept trying would loop forever on a row it can never read.
	if got := x.askedSizes(); len(got) != 1 || got[0] != 1 {
		t.Errorf("the fetch asked for address counts %v, want exactly one request of 1", got)
	}
}

// An error that is NOT the cap is terminal: the fetch does not subdivide its way through a
// transport outage or a refused authority, which would turn one failure into many.
func TestAGeometryFetchDoesNotSplitOnAnUnrelatedError(t *testing.T) {
	boom := errors.New("device-management refused the authority")
	doc := fenceBox(0, 0, 1, 1)
	c, x := scriptedClient(t, func(_ int, query string, _ map[string]any) (string, error) {
		if query == geoFenceGeometryQuery {
			return "", boom
		}
		return manifestData(t, "geoFenceSetManifest", 3,
			dmmodel.GeoFenceManifestEntry{Token: "a", Hash: docHash(doc)},
			dmmodel.GeoFenceManifestEntry{Token: "b", Hash: docHash(fenceBox(2, 2, 3, 3))}), nil
	})

	_, err := c.FenceSetAt(context.Background(), "acme", 3)
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the transport error unchanged", err)
	}
	if got := x.askedSizes(); len(got) != 1 {
		t.Errorf("the fetch spent %d geometry requests on a non-cap error, want 1", len(got))
	}
}

// A peer that refuses every batch above a single address is stopped by the REQUEST BUDGET
// rather than spinning this goroutine forever.
//
// 🔴 THE FIXTURE HAS TO LET A SINGLE ADDRESS SUCCEED, OR IT MEASURES THE WRONG STOP. A peer
// that refuses even a size-1 request terminates the fetch on the FIRST leaf with "too large on
// its own" — a different, already-tested error — after five requests, and a test written that
// way would report the budget as working while never approaching it. What the budget defends
// against is a peer that makes progress expensively: a chunk that is refused and split all the
// way down costs 2*geometryChunkSize-1 requests (every leaf, plus every refused interior node).
//
// 🔴 THE FIXTURE SIZE IS DERIVED FROM THE BUDGET, AND IT USED TO BE A LITERAL 300 AGAINST A
// LITERAL 512. Both moved: maxGeometryRequests is now derived from governance.MaxGeoFenceCeiling,
// because the fence count became a tier setting and a runaway stop below what an operator may
// legitimately grant is an outage rather than a stop. A hardcoded fixture would have gone on
// passing until the budget rose past it and then reported the stop as broken — which is exactly
// what it did, in this test, on the commit that raised it.
//
// The budget is a runaway stop, not a second size limit, so it sits well above anything a
// legitimate fetch reaches — which is why provoking it also needs a manifest larger than any
// tenant can author. That is exactly the input it defends against: the manifest is
// caller-supplied, and nothing on this side bounds what a fact may claim.
func TestTheGeometryReadStopsAtItsRequestBudget(t *testing.T) {
	// Every leaf plus every refused interior node of one fully-split chunk.
	const requestsPerRefusedChunk = 2*geometryChunkSize - 1
	// Two chunks past the budget, so the stop has to fire rather than the fetch merely ending.
	chunks := maxGeometryRequests/requestsPerRefusedChunk + 2
	n := chunks * geometryChunkSize
	if chunks*requestsPerRefusedChunk <= maxGeometryRequests {
		t.Fatalf("the fixture spends only %d requests against a budget of %d; it cannot reach the "+
			"stop it exists to test", chunks*requestsPerRefusedChunk, maxGeometryRequests)
	}

	entries := make([]dmmodel.GeoFenceManifestEntry, 0, n)
	byHash := map[string]string{}
	for i := 0; i < n; i++ {
		doc := fenceBox(float64(i), 0, float64(i)+1, 1)
		byHash[docHash(doc)] = doc
		entries = append(entries, dmmodel.GeoFenceManifestEntry{
			Token: fmt.Sprintf("f%03d", i), Hash: docHash(doc)})
	}
	c, x := scriptedClient(t, func(_ int, query string, vars map[string]any) (string, error) {
		if query == geoFenceGeometryQuery {
			hashes := vars["hashes"].([]string)
			if len(hashes) > 1 {
				return "", tooLarge()
			}
			return geometryData(t, held(byHash[hashes[0]])), nil
		}
		return manifestData(t, "geoFenceSetManifest", 5, entries...), nil
	})

	set, err := c.FenceSetAt(context.Background(), "acme", 5)
	if err == nil {
		t.Fatalf("a peer that refuses every batch was read to completion (%d fences)", set.Len())
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("the read failed with %v, which does not say the budget stopped it; an operator "+
			"reading this cannot tell it from an ordinary outage", err)
	}
	if set != nil {
		t.Errorf("a budget-exhausted read returned a set of %d fences; it must return what it "+
			"has to NOBODY", set.Len())
	}
	if got := len(x.asked); got != maxGeometryRequests {
		t.Errorf("the read spent %d geometry requests, want exactly the %d-request budget",
			got, maxGeometryRequests)
	}
}

// A document that does not hash to the address it was requested under is FATAL to the whole
// fetch, and is counted separately from every other failure.
//
// 🔴 IT IS FATAL RATHER THAN PER-FENCE BECAUSE NONE OF ITS CAUSES IS PER-FENCE. A peer serving
// the wrong row, or anything on the path re-encoding the JSON, applies to every document in the
// response — and a verified body is about to enter a cache that outlives every version, where
// no reconcile sweep would ever revisit it. It is counted separately because it is the one
// failure here that is never transient and never benign.
//
// The control is the same fetch with the document served under its own address, which succeeds
// and counts nothing: without it, "the read failed" would pass against a client that refuses
// every document.
func TestADocumentThatDoesNotMatchItsAddressIsFatalAndCounted(t *testing.T) {
	yard := fenceBox(0, 0, 1, 1)
	impostor := fenceBox(10, 10, 11, 11)
	entry := dmmodel.GeoFenceManifestEntry{Token: "yard", Hash: docHash(yard)}

	serve := impostor
	c, x := scriptedClient(t, func(_ int, query string, _ map[string]any) (string, error) {
		if query == geoFenceGeometryQuery {
			// Filed under the address that was ASKED for, carrying different bytes — the shape
			// a wrong row or a re-encode produces, and the one no schema check can catch.
			return geometryData(t, geometryDoc{Hash: entry.Hash, Geometry: serve}), nil
		}
		return manifestData(t, "geoFenceSetManifest", 2, entry), nil
	})

	set, err := c.FenceSetAt(context.Background(), "acme", 2)
	if !errors.Is(err, errGeometryHashMismatch) {
		t.Fatalf("a document served under the wrong address resolved with err=%v; it would have "+
			"answered containment confidently and wrongly, from a cache entry nothing revisits", err)
	}
	if set != nil {
		t.Errorf("a mismatched document returned a set of %d fences, want nothing", set.Len())
	}
	if got := testutil.ToFloat64(c.metrics.fenceGeometryHashMismatch); got != 1 {
		t.Errorf("the mismatch was counted %v times, want 1 — it is the one geofence failure "+
			"that is never transient, and it needs its own number", got)
	}
	if got := testutil.ToFloat64(c.metrics.fenceGeometryUnresolved); got != 0 {
		t.Errorf("a mismatch was counted as %v unresolved entries; that counter means 'the "+
			"archive did not hold this body', which is a different and repairable condition", got)
	}

	// CONTROL: the correct document under the same address resolves, and counts nothing.
	serve = yard
	before := x.calls
	good, err := c.FenceSetAt(context.Background(), "acme", 2)
	if err != nil {
		t.Fatalf("control: the correct document would not resolve: %v", err)
	}
	if good.Len() != 1 || good.Fence("yard") == nil {
		t.Fatalf("control: the correct document produced %d fences", good.Len())
	}
	if got := testutil.ToFloat64(c.metrics.fenceGeometryHashMismatch); got != 1 {
		t.Errorf("a correct document moved the mismatch counter to %v", got)
	}
	if x.calls <= before {
		t.Error("control: the retry was served from cache, so the mismatched document WAS " +
			"retained — nothing may be negative-cached, and a poisoned entry least of all")
	}
}

// A peer that does not serve a manifest-delivery door is reported as VERSION SKEW, not as an
// unreachable archive — at every door, because a rollback removes all three at once and which
// one this service asks first depends only on which road it is on.
//
// The two symptoms are identical and their causes and cures are nothing alike: an operator
// seeing "cannot read the fence archive" goes and looks at the network, where "the peer does
// not serve this query" says to look at what is deployed. The condition repairs itself the
// moment device-management rolls forward, and nothing here should try to work around it.
func TestAPeerThatDoesNotServeTheManifestDoorsIsReportedAsVersionSkew(t *testing.T) {
	yard := fenceBox(0, 0, 1, 1)
	for _, tc := range []struct {
		name    string
		missing string
	}{
		// The current-manifest door, which the reconcile sweep and the startup seed ask first.
		{"current manifest door", "currentGeoFenceSetManifest"},
		// The geometry door, which a fact-borne manifest reaches without asking a manifest door
		// at all — so a rollback surfaces here rather than above on the fact road.
		{"geometry door", "geoFenceGeometry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := scriptedClient(t, func(_ int, query string, _ map[string]any) (string, error) {
				if strings.Contains(query, tc.missing) {
					return "", fmt.Errorf(`graphql: Cannot query field %q on type "Query"`, tc.missing)
				}
				return manifestData(t, "currentGeoFenceSetManifest", 4,
					dmmodel.GeoFenceManifestEntry{Token: "yard", Hash: docHash(yard)}), nil
			})

			set, err := c.CurrentFenceSet(context.Background(), "acme")
			if !errors.Is(err, errArchiveSkew) {
				t.Fatalf("got %v, want errArchiveSkew — an operator cannot tell a rollback from "+
					"an outage without it", err)
			}
			if set != nil {
				t.Errorf("a skewed peer returned a set of %d fences, want nothing", set.Len())
			}
		})
	}
}

// Version skew is COUNTED on EVERY door it can surface on, so a rollback shows up on a
// dashboard rather than only in a log line.
//
// 🔴 THE MANIFEST-DOOR CASES ARE THE POINT OF THIS TEST, AND THEY ARE WHERE THE COUNTER USED TO
// BE UNREACHABLE. Skew was recorded in assemble, which a manifest-door failure never reaches —
// fetchManifest classifies the error and returns it straight to FenceSetAt/CurrentFenceSet. So
// the reconcile and startup roads, which ask a MANIFEST door first, are exactly the roads a
// rolled-back device-management is met on, and exactly the ones on which the condition was
// reported in a log and counted nowhere. An alert had already been written against the counter.
//
// The fix was to count where the error is RAISED rather than where it is handled, which is why
// all three doors can be asserted from one table now: there is a single place an unknown-field
// error becomes errArchiveSkew.
func TestVersionSkewIsCountedOnEveryDoor(t *testing.T) {
	yard := fenceBox(0, 0, 1, 1)
	for _, tc := range []struct {
		name    string
		missing string
		// call is the entry point that actually reaches the door under test. It differs per
		// case and that is the point: the version-addressed door is reached only by FenceSetAt,
		// so driving all three through CurrentFenceSet would leave one case asserting nothing.
		call func(c runtime.FenceSetSource, cur runtime.CurrentFenceSetSource) error
	}{
		{"version manifest door", "geoFenceSetManifest", func(c runtime.FenceSetSource, _ runtime.CurrentFenceSetSource) error {
			_, err := c.FenceSetAt(context.Background(), "acme", 4)
			return err
		}},
		{"current manifest door", "currentGeoFenceSetManifest", func(_ runtime.FenceSetSource, cur runtime.CurrentFenceSetSource) error {
			_, err := cur.CurrentFenceSet(context.Background(), "acme")
			return err
		}},
		{"geometry door", "geoFenceGeometry", func(_ runtime.FenceSetSource, cur runtime.CurrentFenceSetSource) error {
			_, err := cur.CurrentFenceSet(context.Background(), "acme")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := scriptedClient(t, func(_ int, query string, _ map[string]any) (string, error) {
				if strings.Contains(query, tc.missing) {
					return "", fmt.Errorf(`graphql: Cannot query field %q on type "Query"`, tc.missing)
				}
				name := "currentGeoFenceSetManifest"
				if strings.Contains(query, "geoFenceSetManifest(") {
					name = "geoFenceSetManifest"
				}
				return manifestData(t, name, 4,
					dmmodel.GeoFenceManifestEntry{Token: "yard", Hash: docHash(yard)}), nil
			})

			if err := tc.call(c, c); !errors.Is(err, errArchiveSkew) {
				t.Fatalf("got %v, want errArchiveSkew", err)
			}
			if got := testutil.ToFloat64(c.metrics.fenceArchiveSkew); got != 1 {
				t.Errorf("the skew was counted %v times, want 1 — an alert is written against "+
					"this counter, so a road that does not move it is an alert that cannot fire", got)
			}
			// It is NOT counted as an unresolvable entry: that counter means "the archive did
			// not hold this body", which is per-fence and repairable, where this is a
			// whole-deployment condition.
			if got := testutil.ToFloat64(c.metrics.fenceGeometryUnresolved); got != 0 {
				t.Errorf("version skew was counted as %v unresolved entries", got)
			}
		})
	}
}

// NEGATIVE CONTROL for the skew match. An unrelated validation error is NOT version skew.
//
// Without this, the test above passes just as well against a matcher that calls every GraphQL
// error a rollback — which would send an operator to look at deployments over a typo, and would
// mean the skew counter reported on nothing in particular.
func TestAnUnrelatedValidationErrorIsNotReportedAsVersionSkew(t *testing.T) {
	c, _ := scriptedClient(t, func(int, string, map[string]any) (string, error) {
		return "", errors.New(`graphql: Cannot query field "widgets" on type "Query"`)
	})

	_, err := c.CurrentFenceSet(context.Background(), "acme")
	if err == nil {
		t.Fatal("an unknown-field error was accepted")
	}
	if errors.Is(err, errArchiveSkew) {
		t.Errorf("an unrelated validation error was reported as version skew (%v)", err)
	}
	if got := testutil.ToFloat64(c.metrics.fenceArchiveSkew); got != 0 {
		t.Errorf("an unrelated validation error moved the skew counter to %v", got)
	}
}

// A version-addressed read that answers about a DIFFERENT version is a fault, not a surprise to
// absorb. The whole point of addressing by version is that the answer is deterministic, and
// filing another version's fences under the number asked for is the failure the stamp exists to
// prevent.
func TestAnArchiveThatAnswersAboutAnotherVersionIsAFault(t *testing.T) {
	c, _ := scriptedClient(t, func(int, string, map[string]any) (string, error) {
		return manifestData(t, "geoFenceSetManifest", 8), nil
	})

	set, err := c.FenceSetAt(context.Background(), "acme", 7)
	if err == nil {
		t.Fatalf("version 7 resolved to a set the archive filed under another version (%d fences)",
			set.Len())
	}
	if set != nil {
		t.Errorf("a version mismatch returned a set of %d fences, want nothing", set.Len())
	}
	if !strings.Contains(err.Error(), "7") || !strings.Contains(err.Error(), "8") {
		t.Errorf("the error does not name both versions (%v)", err)
	}
}

// A manifest entry with no token or no address is a fault. Neither half is optional: an entry
// with no address names geometry nothing can resolve, and an entry with no token names a fence
// no rule can reach — and both would otherwise install as a silently short fence set.
func TestAManifestEntryMissingATokenOrAnAddressIsAFault(t *testing.T) {
	doc := fenceBox(0, 0, 1, 1)
	for _, tc := range []struct {
		name  string
		entry dmmodel.GeoFenceManifestEntry
	}{
		{"no address", dmmodel.GeoFenceManifestEntry{Token: "yard"}},
		{"no token", dmmodel.GeoFenceManifestEntry{Hash: docHash(doc)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := scriptedClient(t, func(int, string, map[string]any) (string, error) {
				return manifestData(t, "geoFenceSetManifest", 6, tc.entry), nil
			})
			set, err := c.FenceSetAt(context.Background(), "acme", 6)
			if err == nil {
				t.Fatalf("a manifest entry with %s resolved to a set of %d fences", tc.name, set.Len())
			}
			if set != nil {
				t.Errorf("a malformed manifest returned a set of %d fences, want nothing", set.Len())
			}
		})
	}
}

// ── 2. the failure that is PER-FENCE: installed carrying its error ───────────────────────────

// 🔴 THE SAFETY PROPERTY OF THE WHOLE DESIGN. A fence whose body the archive does not HOLD is
// installed CARRYING ITS ERROR — never omitted, and never answered "outside".
//
// The three readings are three different sentences and only one of them is true. Omitting the
// fence makes containment answer ErrUnknownFence — "that fence did not exist at this version" —
// which is a claim about AUTHORING history that is simply false, and it points the author at
// their rule instead of at the delivery that failed. Answering false makes it "the device is
// not in that region", which for a Duration rule CANCELS an in-flight hold and, for every kind,
// reads as a quiet healthy never-firing rule. An error makes the runtime SKIP the sample and
// counts it.
//
// The control is the OTHER fence in the same set, whose body did arrive: it answers containment
// normally, so "the set installed" is not being confused with "the set is broken".
func TestAFenceWhoseBodyTheArchiveDoesNotHoldIsInstalledCarryingItsError(t *testing.T) {
	yard := fenceBox(0, 0, 1, 1)
	dock := fenceBox(10, 10, 11, 11)
	entries := []dmmodel.GeoFenceManifestEntry{
		{Token: "yard", Hash: docHash(yard)},
		{Token: "dock", Hash: docHash(dock)},
	}
	c, _ := scriptedClient(t, func(_ int, query string, _ map[string]any) (string, error) {
		if query == geoFenceGeometryQuery {
			// The archive answered, and simply does not hold "dock"'s body. That is not an
			// error on this door — the question is "which of these do you have?" — so the
			// caller is the only thing standing between a missing body and a missing fence.
			return geometryData(t, held(yard)), nil
		}
		return manifestData(t, "geoFenceSetManifest", 11, entries...), nil
	})

	set, err := c.FenceSetAt(context.Background(), "acme", 11)
	if err != nil {
		t.Fatalf("one unresolvable body failed the WHOLE version (%v); the rest of the tenant's "+
			"fences are perfectly evaluable and must not be denied for it", err)
	}
	if set.Len() != 2 {
		t.Fatalf("the set holds %d fences, want 2 — an unresolvable fence must be PRESENT", set.Len())
	}

	// The unresolvable fence reports an ERROR, and specifically NOT the "no such fence" one.
	in, err := set.Contains("dock", geofence.Position{Lat: 10.5, Lon: 10.5})
	if err == nil {
		t.Fatalf("an unresolvable fence answered containment (inside=%v) instead of reporting "+
			"that it could not be evaluated", in)
	}
	if errors.Is(err, geofence.ErrUnknownFence) {
		t.Errorf("an unresolvable fence reports %v — 'that fence did not exist at this version', "+
			"which is a false claim about authoring history and points the author at their rule "+
			"instead of at the delivery that failed", err)
	}
	if in {
		t.Error("an unresolvable fence answered inside=true alongside its error")
	}

	// CONTROL: the fence whose body DID arrive answers normally, both directions.
	if in, err := set.Contains("yard", geofence.Position{Lat: 0.5, Lon: 0.5}); err != nil || !in {
		t.Errorf("control: the resolvable yard reports inside=%v err=%v for a point within it", in, err)
	}
	if in, err := set.Contains("yard", geofence.Position{Lat: 40, Lon: 40}); err != nil || in {
		t.Errorf("control: the resolvable yard reports inside=%v err=%v for a point far outside", in, err)
	}

	if got := testutil.ToFloat64(c.metrics.fenceGeometryUnresolved); got != 1 {
		t.Errorf("the unresolvable entry was counted %v times, want 1 — without it the only "+
			"signal is a containment eval error, which cannot say why", got)
	}
}

// NOTHING IS NEGATIVE-CACHED: a body that was missing and then arrives resolves normally.
//
// The compiled-geometry cache lives as long as the process, so retaining a failure would turn
// one unavailable body into a fence that stops working until a restart, for a reason that has
// long since gone away — and no reconcile sweep would ever repair it, because a cache hit does
// not fetch.
//
// The first resolve is the control: it really did fail, so the success afterwards is
// attributable to the retry rather than to a body that was there all along.
func TestAMissingBodyIsNotRememberedSoTheNextResolveRepairsIt(t *testing.T) {
	ctx := context.Background()
	dock := fenceBox(10, 10, 11, 11)
	entry := dmmodel.GeoFenceManifestEntry{Token: "dock", Hash: docHash(dock)}

	holds := false
	c, _ := scriptedClient(t, func(_ int, query string, _ map[string]any) (string, error) {
		if query == geoFenceGeometryQuery {
			if !holds {
				return geometryData(t), nil
			}
			return geometryData(t, held(dock)), nil
		}
		return manifestData(t, "geoFenceSetManifest", 12, entry), nil
	})

	// CONTROL: while the archive does not hold it, the fence carries its error.
	first, err := c.FenceSetAt(ctx, "acme", 12)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if _, err := first.Contains("dock", geofence.Position{Lat: 10.5, Lon: 10.5}); err == nil {
		t.Fatal("control: the missing body produced a fence that answers; the repair below " +
			"would prove nothing")
	}

	// The body arrives, and the very next resolve is whole.
	holds = true
	second, err := c.FenceSetAt(ctx, "acme", 12)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	in, err := second.Contains("dock", geofence.Position{Lat: 10.5, Lon: 10.5})
	if err != nil || !in {
		t.Fatalf("after the body arrived the fence still reports inside=%v err=%v; a transient "+
			"failure has become permanent, and only a restart clears it", in, err)
	}
}

// ── 3. the consumer's decision when a manifest fact cannot be resolved ────────────────────────

// nilManifestResolver returns neither a set nor an error — a broken implementation of the
// contract, and the one shape that would install nil as a fence set if it were not caught.
type nilManifestResolver struct{ calls int }

func (r *nilManifestResolver) ResolveManifest(context.Context, string,
	*dmmodel.GeoFenceSetManifest) (*geofence.FenceSet, error) {
	r.calls++
	return nil, nil
}

// failingManifestResolver answers every resolve with the same error.
type failingManifestResolver struct {
	err   error
	calls int
}

func (r *failingManifestResolver) ResolveManifest(context.Context, string,
	*dmmodel.GeoFenceSetManifest) (*geofence.FenceSet, error) {
	r.calls++
	return nil, r.err
}

var (
	_ runtime.FenceManifestResolver = (*nilManifestResolver)(nil)
	_ runtime.FenceManifestResolver = (*failingManifestResolver)(nil)
)

// manifestFactFor mints a real fence set and returns the manifest fact device-management
// published for it, so the consumer tests below run on a real fact rather than a hand-written
// one.
func manifestFactFor(t *testing.T) []byte {
	t.Helper()
	_, facts := ceilingFenceSet(t)
	raw, _ := lastFact(t, facts)
	return raw
}

// A manifest fact whose resolve FAILS is acked, and installs nothing.
//
// Both of those are decisions. Acking rather than leaving it to redeliver, because redelivery
// re-runs the same read against the same unavailable peer on the broker's schedule while the
// five-minute reconcile sweep already exists to repair exactly this — and because a permanently
// unreadable version would otherwise park the stream behind it. Installing nothing, because the
// alternative — an empty set — reads as "this tenant has no fences" and never fires again.
func TestAManifestFactWhoseResolveFailsIsAckedAndInstallsNothing(t *testing.T) {
	raw := manifestFactFor(t)
	resolver := &failingManifestResolver{err: errors.New("device-management is down")}

	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, nil)
	rp.fenceView = runtime.NewFenceSetView()
	rp.FenceManifests = resolver

	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", raw, ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if resolver.calls != 1 {
		t.Errorf("the resolver was consulted %d times, want 1", resolver.calls)
	}
	if ack.acks != 1 {
		t.Errorf("the fact was acked %d times, want 1 — an unresolvable version left unacked "+
			"redelivers forever and parks the stream behind it", ack.acks)
	}
	if n := drainFenceUpdates(rp); n != 0 {
		t.Errorf("%d fence sets were marshalled onto the loop after a failed resolve; installing "+
			"anything here would present a read failure as the tenant's fences", n)
	}
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 0 {
		t.Errorf("the projection holds %v after a failed resolve, want nothing — the version must "+
			"stay unresolvable, not become empty", held)
	}
}

// A resolver that returns neither a set nor an error is treated as a failure, not installed.
func TestAManifestFactWithAnEmptyResolverAnswerInstallsNothing(t *testing.T) {
	raw := manifestFactFor(t)
	resolver := &nilManifestResolver{}

	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, nil)
	rp.fenceView = runtime.NewFenceSetView()
	rp.FenceManifests = resolver

	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", raw, ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if resolver.calls != 1 {
		t.Errorf("the resolver was consulted %d times, want 1", resolver.calls)
	}
	if n := drainFenceUpdates(rp); n != 0 {
		t.Errorf("%d fence sets were installed from a nil resolver answer", n)
	}
	if ack.acks != 1 {
		t.Errorf("the fact was acked %d times, want 1", ack.acks)
	}
}

// With NO archive seam wired at all, a manifest fact is reported through the same failure path
// rather than through a branch of its own.
//
// The branch it used to have was unreachable in production — fenceView is built only when the
// seam exists, and buildFenceSetSeam returns every half or none — so its own log line and its
// own metric increment read as coverage of a state nothing could reach. Folded into the
// ordinary failure path it is reachable, reported once, and tested here.
func TestAManifestFactWithNoArchiveSeamIsAckedAndInstallsNothing(t *testing.T) {
	raw := manifestFactFor(t)

	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, nil)
	rp.fenceView = runtime.NewFenceSetView()
	rp.FenceManifests = nil

	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", raw, ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if ack.acks != 1 {
		t.Errorf("the fact was acked %d times, want 1", ack.acks)
	}
	if n := drainFenceUpdates(rp); n != 0 {
		t.Errorf("%d fence sets were installed with no archive to resolve them from", n)
	}
}

// With the projection DISABLED entirely (no fenceView), a manifest fact is acked and installs
// nothing. That is the counterweight to the three tests above: this deployment does no
// containment at all, so there is nothing degraded here to report.
func TestAManifestFactWithNoProjectionIsAcked(t *testing.T) {
	raw := manifestFactFor(t)

	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, nil)
	rp.fenceView = nil

	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", raw, ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if ack.acks != 1 {
		t.Errorf("the fact was acked %d times, want 1", ack.acks)
	}
	if n := drainFenceUpdates(rp); n != 0 {
		t.Errorf("a disabled projection marshalled %d fence updates, want 0", n)
	}
}

// A fact that is not a manifest at all is dropped and acked, never installed. The control is
// the well-formed fact next door, which IS installed.
func TestAnUnparseableManifestFactIsDroppedAndAcked(t *testing.T) {
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, nil)
	rp.fenceView = runtime.NewFenceSetView()
	rp.FenceManifests = &failingManifestResolver{err: errors.New("must not be reached")}

	ack := &fakeAck{}
	if !rp.handleFenceSetFact(fenceFactMsg("acme", []byte("{not json"), ack)) {
		t.Fatal("handleFenceSetFact reported shutdown")
	}
	if ack.acks != 1 {
		t.Errorf("a poison fact must be acked so it stops redelivering; acks=%d", ack.acks)
	}
	if n := drainFenceUpdates(rp); n != 0 {
		t.Errorf("%d fence sets were installed from an unparseable fact", n)
	}
}

// 🔴 AN ENTRY EVICTED BETWEEN THE PLAN AND THE FETCH IS REFETCHED, NEVER REPORTED AS ABSENT.
//
// assemble sizes its batch from GeometryCache.Held, which is advisory — it deliberately does not
// take the lock for the whole assembly. So an address reported as held can be gone by the time
// Get asks for it, and the first version of this code answered that with "device-management
// holds no geometry under X": a fence installed carrying an error that is factually FALSE, and
// counted as unresolved, for a body the archive holds perfectly well.
//
// It is not a rare race. assemble's own admits evict, so a tenant whose fence set does not fit
// the cache bound evicts entries THIS CALL planned as held — every rebuild manufactures
// unresolvable fences, and the reconcile sweep hits the same wall forever rather than repairing
// anything.
//
// The fixture forces exactly that: a cache bounded well below the set, primed with the first
// fence, so resolving the rest evicts it before its turn comes.
func TestAnEntryEvictedAfterThePlanIsRefetchedNotReportedAbsent(t *testing.T) {
	rings := []string{fenceBox(0, 0, 1, 1), fenceBox(10, 10, 11, 11), fenceBox(20, 20, 21, 21)}
	entries := make([]dmmodel.GeoFenceManifestEntry, 0, len(rings))
	docs := make([]geometryDoc, 0, len(rings))
	for i, r := range rings {
		entries = append(entries, dmmodel.GeoFenceManifestEntry{
			Token: fmt.Sprintf("fence-%d", i), Hash: docHash(r)})
		docs = append(docs, held(r))
	}

	c, x := scriptedClient(t, func(_ int, query string, vars map[string]any) (string, error) {
		if query == geoFenceGeometryQuery {
			asked := map[string]bool{}
			for _, h := range vars["hashes"].([]string) {
				asked[h] = true
			}
			want := make([]geometryDoc, 0, len(docs))
			for _, d := range docs {
				if asked[d.Hash] {
					want = append(want, d)
				}
			}
			return geometryData(t, want...), nil
		}
		return manifestData(t, "currentGeoFenceSetManifest", 3, entries...), nil
	})
	// A bound that cannot hold the whole set: each box is 4 compiled vertices, so two fit and
	// the third evicts the least recently used — which is the one the plan called held.
	c.cache = geofence.NewGeometryCache(8)

	// Prime the cache with the first fence, so Held reports it and it is left out of the batch.
	if _, err := c.cache.Get(context.Background(), "acme", docHash(rings[0]),
		func(context.Context) ([]byte, error) { return []byte(rings[0]), nil }); err != nil {
		t.Fatalf("prime the cache: %v", err)
	}

	set, err := c.CurrentFenceSet(context.Background(), "acme")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	for i := range rings {
		token := fmt.Sprintf("fence-%d", i)
		fence := set.Fence(token)
		if fence == nil {
			t.Fatalf("%s is absent from the set entirely", token)
		}
		if _, err := set.Contains(token, geofence.Position{Lat: 0.5, Lon: 0.5}); err != nil &&
			errors.Is(err, geofence.ErrUnknownFence) {
			t.Fatalf("%s resolved to an unknown fence", token)
		}
	}
	// The real assertion: nothing was reported unresolvable. Before the fix, fence-0 was —
	// evicted by the admits of fences 1 and 2, then declared absent without being asked for.
	if got := testutil.ToFloat64(c.metrics.fenceGeometryUnresolved); got != 0 {
		t.Fatalf("%v manifest entries were reported unresolvable, but the archive holds every "+
			"one of them; an entry evicted after the plan must be refetched, not declared absent", got)
	}
	// The control: the cache really is too small to hold the set, so eviction genuinely
	// happened and the assertion above is not passing because nothing was ever evicted.
	if evicted := c.cache.Stats().Evictions; evicted == 0 {
		t.Fatal("nothing was evicted, so this test did not exercise the stale-plan path at all")
	}
	if x.calls == 0 {
		t.Fatal("no geometry was fetched")
	}
}

// 🔴 THE REQUEST BUDGET IS A BAND, AND BOTH EDGES ARE LOAD-BEARING. Too low and the stop fires
// on a legitimate fetch, which is an outage for whichever tenant an operator packaged
// generously. Too high and it never fires on the runaway it exists for — present, funded, inert.
// Only asserting both catches both, and each edge has already been got wrong once:
//
//   - the LOWER edge was a literal 512 justified by a fence ceiling of 100, which survived that
//     ceiling becoming a tier setting because the only test of it derives its FIXTURE from the
//     budget. Shrink the budget, the fixture shrinks with it, and it passes. A mutant setting the
//     budget to an eighth survived that test untouched.
//   - the UPPER edge was lost by raising the budget to 3N on a mis-priced worst case (see
//     maxGeometryRequests). 12,000 sits above the pathological 11,833 too, so a runaway archive
//     would have spent every request and completed.
//
// So this asserts a RELATION between constants in different modules, with literal arithmetic on
// each side rather than a fixture that follows either one.
func TestTheRequestBudgetSitsAboveTheLargestLegitimateFetch(t *testing.T) {
	documents := governance.MaxGeoFenceCeiling

	// One fully-split chunk costs every leaf plus every refused interior node: 2*size-1. The
	// trailing partial chunk costs less than a full one, so this is computed rather than
	// approximated as chunks*(2c-1) — that overcounts, and a margin this test nominates as
	// load-bearing must be the true one.
	splitCost := func(n int) int {
		full, rem := n/geometryChunkSize, n%geometryChunkSize
		cost := full * (2*geometryChunkSize - 1)
		if rem > 0 {
			cost += 2*rem - 1
		}
		return cost
	}

	// 🔴 THE LOWER EDGE. The worst LEGITIMATE fetch: the archive holds every body its own
	// manifest names. An address is either planned as missing (and split-fetched) or planned as
	// held (and late-fetched at one request if it was evicted meanwhile) — never both — so the
	// total is split(M) + (N-M), which is maximised when nothing is held.
	legitimate := 0
	for held := 0; held <= documents; held++ {
		if cost := splitCost(documents-held) + held; cost > legitimate {
			legitimate = cost
		}
	}
	if legitimate > maxGeometryRequests {
		t.Fatalf("a legitimate fetch of %d geometry documents costs up to %d requests against a "+
			"budget of %d. A tenant whose tier grants the maximum fence count would have its "+
			"geometry read ABORTED by the runaway stop — a stop that fires on correct work is an "+
			"outage, not a defence", documents, legitimate, maxGeometryRequests)
	}

	// 🔴 THE UPPER EDGE, AND IT IS THE HALF THAT ACTUALLY BITES. The pathological run: the
	// archive answers every batch and holds nothing, so every address is split-fetched AND then
	// late-fetched one at a time. This is the runaway the stop exists for, so the budget must be
	// BELOW it. Without this, an arbitrarily large budget passes the check above while stopping
	// nothing.
	pathological := splitCost(documents) + documents
	if maxGeometryRequests >= pathological {
		t.Fatalf("the budget is %d and a runaway archive — answering every batch while holding no "+
			"bodies — spends %d. The stop would never fire on the exact case it exists for: the "+
			"read completes, having paid every request, and returns a fence set of error fences",
			maxGeometryRequests, pathological)
	}

	t.Logf("budget %d sits between a legitimate worst of %d (+%d) and a runaway worst of %d",
		maxGeometryRequests, legitimate, maxGeometryRequests-legitimate, pathological)
}
