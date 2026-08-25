// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestResolveGeoFenceCap pins the wire fold, which has THREE outcomes rather than the two the
// other scalars have: inherit, pass through, and CLAMP. The clamp is the one worth a test — it
// is the read-side defence against a value that got past both write doors, and it is silent
// when it works.
func TestResolveGeoFenceCap(t *testing.T) {
	const def, max = 512, 1024
	cases := []struct {
		name string
		in   *int32
		want int
	}{
		{"null inherits the platform default", nil, def},
		{"a usable cap passes through", i32(700), 700},
		{"one is a legal (if useless) cap", i32(1), 1},
		{"the maximum itself passes through", i32(max), max},
		{"above the maximum clamps", i32(max + 1), max},
		{"far above the maximum clamps", i32(1_000_000), max},
		{"zero is not a cap → inherit", i32(0), def},
		{"negative is not a cap → inherit", i32(-5), def},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveGeoFenceCap(c.in, def, max); got != c.want {
				t.Errorf("resolveGeoFenceCap(%v, %d, %d) = %d, want %d", c.in, def, max, got, c.want)
			}
		})
	}
	// The counterweight to every case above: NO input resolves to a non-positive cap, and
	// none resolves above the maximum. A zero would mean the tenant may author no fence at
	// all; a value above the maximum would mean the write-door validators are the only thing
	// between an operator's typo and the shared DETECT process.
	for _, in := range []*int32{nil, i32(0), i32(-1), i32(max), i32(max + 1), i32(1 << 30)} {
		got := resolveGeoFenceCap(in, def, max)
		if got <= 0 {
			t.Fatalf("resolveGeoFenceCap(%v) = %d — a cap must never resolve non-positive", in, got)
		}
		if got > max {
			t.Fatalf("resolveGeoFenceCap(%v) = %d — a cap must never resolve above its maximum %d", in, got, max)
		}
	}
}

// TestTheGeoFenceDefaultsSitInsideTheirMaxima is a structural guard on the six constants. It
// looks trivial and is not: the pair are edited for different reasons in different reviews —
// a default is tuned from what tenants need, a maximum from what the platform survives — and a
// default above its maximum would be CLAMPED by resolveGeoFenceCap into a bound smaller than
// the one every tenant is documented to get, silently, on the read side.
func TestTheGeoFenceDefaultsSitInsideTheirMaxima(t *testing.T) {
	for _, c := range []struct {
		name     string
		def, max int
	}{
		{"vertex ceiling", DefaultGeoFenceVertexCeiling, MaxGeoFenceVertexCeiling},
		{"fence ceiling", DefaultGeoFenceCeiling, MaxGeoFenceCeiling},
		{"vertex budget", DefaultTenantGeometryVertices, MaxTenantGeometryVertices},
	} {
		if c.def <= 0 {
			t.Errorf("%s: the default is %d — a cap of zero admits no fence at all", c.name, c.def)
		}
		if c.def > c.max {
			t.Errorf("%s: the default %d is above the maximum %d, so every tenant is silently clamped below it",
				c.name, c.def, c.max)
		}
	}
	// DefaultGeoFenceCaps must agree with the constants it is built from — it is what a
	// service with no resolver meters at, so a field crossed with another would meter every
	// such deployment at the wrong bound. Six interchangeable ints is exactly the shape that
	// swap goes unnoticed in.
	d := DefaultGeoFenceCaps()
	if d.VertexCeiling != DefaultGeoFenceVertexCeiling || d.FenceCeiling != DefaultGeoFenceCeiling ||
		d.VertexBudget != DefaultTenantGeometryVertices {
		t.Errorf("DefaultGeoFenceCaps() = %+v, want {%d %d %d}", d,
			DefaultGeoFenceVertexCeiling, DefaultGeoFenceCeiling, DefaultTenantGeometryVertices)
	}
}

// TestGeoFenceCapsQueryAndResponseAgree is the seam a unit test cannot otherwise reach:
// svcclient.Client is concrete, so nothing in fetchGeoFenceCaps is injectable. A field name
// the schema does not define, or a json tag that does not match the field, both fail SILENTLY
// — the query errors (every resolve on the instance fails behind one message) or the field
// decodes to nil, which reads as "inherit the platform default" and ignores every cap an
// operator set.
func TestGeoFenceCapsQueryAndResponseAgree(t *testing.T) {
	for _, field := range []string{"tenantGovernance", "geoFenceVertexCeiling", "geoFenceCeiling", "geoFenceVertexBudget"} {
		if !strings.Contains(geoFenceCapsQuery, field) {
			t.Fatalf("the query does not select %s: %q", field, geoFenceCapsQuery)
		}
	}

	var out geoFenceCapsResponse
	body := `{"tenantGovernance":{"geoFenceVertexCeiling":700,"geoFenceCeiling":250,"geoFenceVertexBudget":90000}}`
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decoding a well-formed response failed: %v", err)
	}
	g := out.TenantGovernance
	// Read back by VALUE, not merely non-nil: three same-typed pointers decoded off three
	// similarly-named wire fields is precisely where a copy-pasted json tag lands one field's
	// value in another, and every "is it nil?" check passes while the caps are crossed.
	for _, c := range []struct {
		name string
		got  *int32
		want int32
	}{
		{"geoFenceVertexCeiling", g.GeoFenceVertexCeiling, 700},
		{"geoFenceCeiling", g.GeoFenceCeiling, 250},
		{"geoFenceVertexBudget", g.GeoFenceVertexBudget, 90000},
	} {
		if c.got == nil {
			t.Fatalf("%s decoded to nil from a response that carries it — the json tag does not match the wire field", c.name)
		}
		if *c.got != c.want {
			t.Errorf("%s decoded to %d, want %d — the tags are crossed", c.name, *c.got, c.want)
		}
	}

	// A null is the ordinary "neither the tenant nor its tier declares one" case and must
	// decode to nil rather than to zero, which the fold would then read as "not a cap".
	var null geoFenceCapsResponse
	nullBody := `{"tenantGovernance":{"geoFenceVertexCeiling":null,"geoFenceCeiling":null,"geoFenceVertexBudget":null}}`
	if err := json.Unmarshal([]byte(nullBody), &null); err != nil {
		t.Fatalf("decoding a null response failed: %v", err)
	}
	n := null.TenantGovernance
	if n.GeoFenceVertexCeiling != nil || n.GeoFenceCeiling != nil || n.GeoFenceVertexBudget != nil {
		t.Fatal("a null cap must decode to nil (inherit), not to a value")
	}
}

// capsFetcher is a scriptable fetch: it counts calls, can be made to fail, and can be held
// open so a test can observe what concurrent callers do while one fetch is in flight.
type capsFetcher struct {
	calls atomic.Int64
	mu    sync.Mutex
	caps  GeoFenceCaps
	err   error
	gate  chan struct{} // when non-nil, the fetch blocks on it
}

func (f *capsFetcher) fetch(ctx context.Context, tenant string) (GeoFenceCaps, error) {
	f.calls.Add(1)
	f.mu.Lock()
	gate, caps, err := f.gate, f.caps, f.err
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return GeoFenceCaps{}, ctx.Err()
		}
	}
	return caps, err
}

func (f *capsFetcher) set(caps GeoFenceCaps, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.caps, f.err = caps, err
}

func testCaps(v int) GeoFenceCaps {
	return GeoFenceCaps{VertexCeiling: v, FenceCeiling: v, VertexBudget: v}
}

// TestResolveBlocksThenServesFromCache pins the property that separates this resolver from
// every other one in the package: the FIRST call returns a fetched value, not a default. A
// non-blocking resolver would answer the platform default here and refresh out of band, which
// is what would let an operator's lowered cap be defeated by a restart.
func TestResolveBlocksThenServesFromCache(t *testing.T) {
	f := &capsFetcher{}
	f.set(testCaps(700), nil)
	r := newGeoFenceCapsResolver(f.fetch)

	got, err := r.Resolve(context.Background(), "acme")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if got.VertexCeiling != 700 {
		t.Errorf("the first resolve returned %+v — it served a default instead of blocking on the fetch", got)
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("the first resolve made %d fetches, want 1", n)
	}

	// Inside the TTL, no second fetch: a low-rate authoring path still must not add a
	// round trip to every mutation.
	for i := 0; i < 5; i++ {
		if _, err := r.Resolve(context.Background(), "acme"); err != nil {
			t.Fatalf("cached resolve: %v", err)
		}
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("%d fetches after five cached resolves, want 1", n)
	}
}

// TestAColdMissReturnsTheErrorRatherThanTheDefaults is the direct kill for the tempting
// simplification. Falling back to DefaultGeoFenceCaps() on an unresolvable tenant looks safe —
// they are real bounds — but a TIER MAY DECLARE CAPS BELOW THE DEFAULTS, so it would hand
// exactly the tenant whose tier could not be read a cap LARGER than the operator granted it.
func TestAColdMissReturnsTheErrorRatherThanTheDefaults(t *testing.T) {
	boom := errors.New("user-management is unreachable")
	f := &capsFetcher{}
	f.set(GeoFenceCaps{}, boom)
	r := newGeoFenceCapsResolver(f.fetch)

	got, err := r.Resolve(context.Background(), "acme")
	if err == nil {
		t.Fatalf("a cold miss against an unreachable authority returned %+v and no error", got)
	}
	if !errors.Is(err, boom) {
		t.Errorf("the error was %v, want the fetch's own error", err)
	}
	if got != (GeoFenceCaps{}) {
		t.Errorf("a failed resolve returned %+v — a caller that ignores the error must not find usable caps", got)
	}
	// The counterweight, and the reason this test can be trusted: the same resolver DOES
	// answer once the authority recovers. Without it, "returns an error" would pass for a
	// resolver that never resolves anything.
	f.set(testCaps(300), nil)
	if got, err := r.Resolve(context.Background(), "acme"); err != nil || got.FenceCeiling != 300 {
		t.Errorf("after recovery the resolve returned (%+v, %v), want the fetched caps", got, err)
	}
}

// TestAStaleEntryIsServedWhenARefreshFails pins the staged degradation. The alternative —
// refusing once an entry goes stale — would take fence authoring down platform-wide on a
// user-management blip, including the DELETES a tenant uses to get back under a cap.
func TestAStaleEntryIsServedWhenARefreshFails(t *testing.T) {
	f := &capsFetcher{}
	f.set(testCaps(700), nil)
	r := newGeoFenceCapsResolver(f.fetch)

	if _, err := r.Resolve(context.Background(), "acme"); err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	// Age the entry past the TTL and break the authority.
	r.now = func() time.Time { return time.Now().Add(2 * defaultCacheTTL) }
	f.set(GeoFenceCaps{}, errors.New("user-management is unreachable"))

	got, err := r.Resolve(context.Background(), "acme")
	if err != nil {
		t.Fatalf("a stale entry with a failing refresh returned an error: %v", err)
	}
	if got.VertexCeiling != 700 {
		t.Errorf("the stale resolve returned %+v, want the last-known caps", got)
	}
	if n := f.calls.Load(); n != 2 {
		t.Errorf("%d fetches, want 2 — a stale entry must still ATTEMPT a refresh, not be served forever", n)
	}
	// And the last-known value must not be a default in disguise: a different tenant, never
	// fetched, still errors rather than borrowing this one's entry.
	if _, err := r.Resolve(context.Background(), "other"); err == nil {
		t.Error("an unknown tenant resolved without error while the authority was down — one tenant's stale caps served another")
	}
}

// TestConcurrentFirstResolvesCollapseToOneFetch pins the singleflight. Without it, N
// concurrent authoring calls during an outage each burn the full fetch timeout independently
// — the amplification the non-blocking resolvers avoid with their inflight set, which a
// blocking resolver cannot reuse because its callers need the RESULT.
func TestConcurrentFirstResolvesCollapseToOneFetch(t *testing.T) {
	gate := make(chan struct{})
	f := &capsFetcher{gate: gate}
	f.set(testCaps(700), nil)
	r := newGeoFenceCapsResolver(f.fetch)

	const callers = 16
	var wg sync.WaitGroup
	errs := make([]error, callers)
	got := make([]GeoFenceCaps, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = r.Resolve(context.Background(), "acme")
		}(i)
	}
	// Let them pile up behind the held fetch, then release it.
	for f.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()

	if n := f.calls.Load(); n != 1 {
		t.Errorf("%d callers produced %d fetches, want 1", callers, n)
	}
	for i := range got {
		if errs[i] != nil {
			t.Errorf("caller %d: %v", i, errs[i])
		}
		if got[i].VertexCeiling != 700 {
			t.Errorf("caller %d got %+v, want the fetched caps — a waiter must get the RESULT, not a default", i, got[i])
		}
	}
}

// TestACancelledCallerDoesNotAbortTheSharedFetch pins the detached context. If the fetch ran
// on the caller's context, one caller giving up would cancel a round trip the other fifteen
// are waiting on — and during an outage, when every caller is on a deadline, that turns one
// timeout into a cascade of them.
func TestACancelledCallerDoesNotAbortTheSharedFetch(t *testing.T) {
	gate := make(chan struct{})
	f := &capsFetcher{gate: gate}
	f.set(testCaps(700), nil)
	r := newGeoFenceCapsResolver(f.fetch)

	quitting, cancel := context.WithCancel(context.Background())
	staying := make(chan struct {
		caps GeoFenceCaps
		err  error
	}, 1)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := r.Resolve(quitting, "acme"); !errors.Is(err, context.Canceled) {
			t.Errorf("the cancelled caller returned %v, want context.Canceled", err)
		}
	}()
	go func() {
		caps, err := r.Resolve(context.Background(), "acme")
		staying <- struct {
			caps GeoFenceCaps
			err  error
		}{caps, err}
	}()

	for f.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	wg.Wait()
	close(gate)

	res := <-staying
	if res.err != nil {
		t.Fatalf("the remaining caller got %v after its neighbour cancelled — the fetch rode the caller's context", res.err)
	}
	if res.caps.VertexCeiling != 700 {
		t.Errorf("the remaining caller got %+v, want the fetched caps", res.caps)
	}
}

// TestAnEmptyTenantIsRefused. An untenanted resolve has no cascade to walk, so answering it
// would mean answering with a number no operator chose, for a caller that has lost track of
// whose fences it is bounding. It mirrors the geometry cache's refusal of an untenanted key.
func TestAnEmptyTenantIsRefused(t *testing.T) {
	f := &capsFetcher{}
	f.set(testCaps(700), nil)
	r := newGeoFenceCapsResolver(f.fetch)

	if _, err := r.Resolve(context.Background(), ""); err == nil {
		t.Fatal("an empty tenant resolved without error")
	}
	if n := f.calls.Load(); n != 0 {
		t.Errorf("an empty tenant reached the authority %d times — it must be refused before the fetch", n)
	}
}

// TestTheFenceCeilingMaximumIsStatedInBytesSomewhereReal is the guard on the one maximum whose
// justification lives in another module: MaxGeoFenceCeiling is a WIRE bound, chosen so
// device-management's worst-case manifest fits the chart's default 1 MiB per-message ceiling
// with headroom. This package cannot import device-management, so it re-derives the arithmetic
// from the same three facts the manifest is built out of.
//
// 🔴 THIS IS A WEAKER INSTRUMENT THAN THE REAL MEASUREMENT AND SAYS SO. It re-states a shape
// rather than marshalling one, so it would not catch a fourth field being added to a manifest
// entry. What it does catch is the failure that actually happened before: a constant raised
// without anyone re-deriving the size it implies.
func TestTheFenceCeilingMaximumIsStatedInBytesSomewhereReal(t *testing.T) {
	// One entry as JSON: {"token":"<=128>","hash":"<64 hex>"} plus the comma between entries.
	const worstEntryBytes = len(`{"token":"","hash":""},`) + 128 + 64
	const defaultBrokerCeiling = 1 << 20

	worst := MaxGeoFenceCeiling * worstEntryBytes
	if worst >= defaultBrokerCeiling {
		t.Fatalf("a worst-case manifest at MaxGeoFenceCeiling=%d is ~%d bytes, at or above the chart's "+
			"default %d-byte per-message ceiling: every default deployment would log the publisher's "+
			"oversized-manifest warning as soon as a tier granted the maximum",
			MaxGeoFenceCeiling, worst, defaultBrokerCeiling)
	}
	// Headroom, not merely fit — the reason 4,000 was chosen over the 4,876 break-even.
	if headroom := defaultBrokerCeiling - worst; headroom < defaultBrokerCeiling/10 {
		t.Errorf("the worst-case manifest leaves %d bytes of headroom under the default ceiling (<10%%); "+
			"the maximum was chosen to leave room for the shape to grow, not to sit at the break-even",
			headroom)
	}
	// The counterweight: the arithmetic must be tight enough to fail. A maximum ten times
	// larger has to be over the ceiling, or the check above is satisfied by a bound so loose
	// it could never fire.
	if 10*MaxGeoFenceCeiling*worstEntryBytes < defaultBrokerCeiling {
		t.Fatal("ten times the fence maximum still fits one message — this test cannot detect a raised cap")
	}
}

// TestTheTenantGeometryBudgetIsHalfAShareOfSomething documents, and pins, the relation the
// budget's two constants encode: the default is a FIFTH of the shared geometry cache (five
// tenants resident at once) and the maximum is a HALF of it (no single tenant may evict every
// other). The cache constant itself lives in event-processing, which this package must not
// import, so the relation is pinned there by a compile-time equality; what is checkable HERE
// is that the two numbers stand in the ratio those two sentences claim.
//
// Without this, "a fifth" and "a half" are prose, and the arc's recurring defect has been a
// comment asserting an invariant that nothing enforces.
func TestTheTenantGeometryBudgetIsHalfAShareOfSomething(t *testing.T) {
	// max = cache/2 and def = cache/5  ⇒  max/def = 5/2.
	if got, want := 2*MaxTenantGeometryVertices, 5*DefaultTenantGeometryVertices; got != want {
		t.Errorf("2×max = %d and 5×default = %d: the constants no longer say that the default is a "+
			"fifth of the geometry cache and the maximum is half of it. Re-derive both, and "+
			"event-processing's DefaultMaxCachedVertices with them", got, want)
	}
	if fmt.Sprint(2*MaxTenantGeometryVertices) != "250000" {
		t.Errorf("the implied geometry cache is %d vertices, not the 250,000 event-processing is built "+
			"around — DefaultMaxCachedVertices derives from MaxTenantGeometryVertices, so this "+
			"is a change to the cache", 2*MaxTenantGeometryVertices)
	}
}
