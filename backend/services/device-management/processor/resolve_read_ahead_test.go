// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dmproto "github.com/devicechain-io/dc-device-management/proto"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/nats-io/nats.go/jetstream"
	"gorm.io/gorm"
)

// inFlight counts calls in progress across every store sharing it, and the most seen at
// once.
type inFlight struct {
	now, peak atomic.Int32
}

func (f *inFlight) enter() {
	n := f.now.Add(1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			return
		}
	}
}

func (f *inFlight) leave() { f.now.Add(-1) }

func (f *inFlight) reset() { f.peak.Store(0) }

// slowKV holds each Get for hold once armed, and counts the Gets in flight at once in
// flight (shared across stores) and in own (this store's alone). Put and Delete are not
// delayed: only reads are what an event waits on.
type slowKV struct {
	*msgtest.MemoryKV
	hold   time.Duration
	armed  *atomic.Bool
	flight *inFlight
	own    inFlight
}

func (s *slowKV) Get(ctx context.Context, key string) (jetstream.KeyValueEntry, error) {
	if s.armed.Load() {
		s.flight.enter()
		s.own.enter()
		time.Sleep(s.hold)
		s.own.leave()
		s.flight.leave()
	}
	return s.MemoryKV.Get(ctx, key)
}

// slowRig is a resolve rig whose caches all ask a slowKV for every read (the in-process
// tier off), so each cache read in an event is one delayed round trip.
type slowRig struct {
	*resolveRig
	armed  atomic.Bool
	flight inFlight
	slow   map[string]*slowKV
}

func newSlowRig(t *testing.T, scoped bool, hold time.Duration) *slowRig {
	t.Helper()
	sr := &slowRig{slow: map[string]*slowKV{}}
	sr.resolveRig = newCountingResolveRig(t, scoped, func(field string, kv *msgtest.MemoryKV) *messaging.Cache {
		s := &slowKV{MemoryKV: kv, hold: hold, armed: &sr.armed, flight: &sr.flight}
		sr.slow[field] = s
		return messaging.NewCacheOver(s, messaging.WithoutLocalCache())
	})
	return sr
}

// The instrument first: three Gets from three goroutines are seen as three at once, and
// three in a row as one. Without this, a counter stuck at 1 (or at 3) would read as a
// verdict on the resolver.
func TestTheInFlightCounterSeesOverlap(t *testing.T) {
	var armed atomic.Bool
	armed.Store(true)
	var flight inFlight
	kv := &slowKV{MemoryKV: msgtest.NewMemoryKV(), hold: 30 * time.Millisecond, armed: &armed, flight: &flight}

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = kv.Get(context.Background(), "k")
		}()
	}
	wg.Wait()
	if got := flight.peak.Load(); got != 3 {
		t.Fatalf("three concurrent Gets peaked at %d in flight, want 3", got)
	}
	flight.reset()
	for i := 0; i < 3; i++ {
		_, _ = kv.Get(context.Background(), "k")
	}
	if got := flight.peak.Load(); got != 1 {
		t.Fatalf("three sequential Gets peaked at %d in flight, want 1", got)
	}
}

// 🔑 THE PROFILE, RELATIONSHIPS AND SCOPED-GROUP READS OF ONE EVENT ARE IN FLIGHT AT ONCE.
// They are independent of each other (each keys on the device, its type or its tenant), so
// an event that misses the in-process copy for all three waits for one key-value round
// trip, not three.
//
// Every cache read goes to a store that holds it 40 ms. The device lookup comes first on
// its own (everything else keys on the device), so the peak is 3, and on the tree that
// read them one after another it is 1.
func TestAnEventReadsItsProfileRelationshipsAndScopeAtTheSameTime(t *testing.T) {
	rig := newSlowRig(t, false, 40*time.Millisecond)
	rig.resolve(tempEvent("21")) // fills the stores
	rig.resetCounters()

	rig.armed.Store(true)
	start := time.Now()
	resolved := rig.resolve(tempEvent("21"))
	elapsed := time.Since(start)
	rig.armed.Store(false)

	if got := stampedUnit(t, resolved); got != "Cel" || resolved.ProfileVersionToken != "p@1" {
		t.Fatalf("resolved unit %q at %q, want Cel at p@1: the reads did not serve the event", got, resolved.ProfileVersionToken)
	}
	if got := rig.flight.peak.Load(); got != 3 {
		t.Errorf("cache reads in flight at once = %d, want 3 (profile, relationships, scoped groups); gets by cache: %v",
			got, rig.gets())
	}
	t.Logf("4 reads held 40 ms each resolved in %v", elapsed)

	// Still one read of each, as before: reading at once must not read twice.
	want := map[string]int{"DeviceByToken": 1, "ProfileResolutionByType": 1, "RelationshipsBySource": 1,
		"ScopedGroupsExist": 1, "MembershipsByEntity": 0}
	for name, w := range want {
		if got := rig.stores[name].Gets; got != w {
			t.Errorf("%s gets = %d, want %d", name, got, w)
		}
	}
	if stmts := rig.statements(); len(stmts) != 0 {
		t.Errorf("an event the caches answered reached the database %d times: %v", len(stmts), stmts)
	}
}

// addAnchor tracks the rig's device to one more area.
func addAnchor(t testing.TB, rig *resolveRig, n int) {
	t.Helper()
	var device model.Device
	if err := rig.db.WithContext(rig.ctx).Where("token = ?", "dev").First(&device).Error; err != nil {
		t.Fatalf("read device: %v", err)
	}
	var relType model.EntityRelationshipType
	if err := rig.db.WithContext(rig.ctx).Where("token = ?", "tracks").First(&relType).Error; err != nil {
		t.Fatalf("read relationship type: %v", err)
	}
	rel := &model.EntityRelationship{
		SourceType:         string(entity.TypeDevice),
		SourceId:           device.ID,
		TargetType:         string(entity.TypeArea),
		TargetId:           uint(n),
		TargetToken:        fmt.Sprintf("area-%d", n),
		RelationshipTypeId: relType.ID,
	}
	rel.Token = fmt.Sprintf("rel%d", n)
	mustCreate(t, rig.db.WithContext(rig.ctx), rel, "relationship")
}

// In a tenant with a rule-scoped group, the membership reads of the device and each of its
// anchors are in flight at once too, once the scoped-group read has said there is a group.
// Three targets (the device and two anchors) peak at 3 on the membership store; read one
// after another they peak at 1.
func TestAnEventReadsItsMembershipsAtTheSameTime(t *testing.T) {
	rig := newSlowRig(t, true, 40*time.Millisecond)
	addAnchor(t, rig.resolveRig, 2)
	rig.resolve(tempEvent("21"))
	rig.resetCounters()

	rig.armed.Store(true)
	resolved := rig.resolve(tempEvent("21"))
	rig.armed.Store(false)

	if len(resolved.Anchors) != 2 {
		t.Fatalf("resolved %d anchors, want 2", len(resolved.Anchors))
	}
	memberships := rig.slow["MembershipsByEntity"]
	if got := memberships.own.peak.Load(); got != 3 {
		t.Errorf("membership reads in flight at once = %d, want 3 (the device and its two anchors)", got)
	}
	if got := rig.stores["MembershipsByEntity"].Gets; got != 3 {
		t.Errorf("membership reads = %d, want 3: one per target, as before", got)
	}
}

// The membership reads of one event are bounded: a device tracked to 20 areas has 21
// targets, and no more than 8 of their reads are in flight at once.
func TestMembershipReadsAreBoundedPerEvent(t *testing.T) {
	rig := newSlowRig(t, true, 20*time.Millisecond)
	for n := 2; n <= 20; n++ {
		addAnchor(t, rig.resolveRig, n)
	}
	rig.resolve(tempEvent("21"))
	rig.resetCounters()

	rig.armed.Store(true)
	rig.resolve(tempEvent("21"))
	rig.armed.Store(false)

	memberships := rig.slow["MembershipsByEntity"]
	if got := memberships.own.peak.Load(); got != 8 {
		t.Errorf("membership reads in flight at once = %d, want 8 (the bound)", got)
	}
	if got := rig.stores["MembershipsByEntity"].Gets; got != 21 {
		t.Errorf("membership reads = %d, want 21", got)
	}
}

// 🔑 THE DATABASE IS NEVER READ BY TWO LOOKUPS OF ONE EVENT AT ONCE. resolution.workers is
// bounded against the connection pool on the premise that a resolver holds at most one
// connection at a time. With every cache empty, every lookup of the event goes to the
// database — the profile chain, the relationships, the scoped-group gate and both
// memberships — and a pool that holds each statement 15 ms sees at most one at a time.
func TestAnEventNeverReadsTheDatabaseTwiceAtOnce(t *testing.T) {
	rig := newSlowRig(t, true, 0)
	pool := holdStatements(t, rig.db, 15*time.Millisecond)

	resolved := rig.resolve(tempEvent("21"))
	if got := stampedUnit(t, resolved); got != "Cel" {
		t.Fatalf("stamped unit = %q, want Cel", got)
	}
	if got := pool.statements.Load(); got < 5 {
		t.Fatalf("the cold event made %d database statements; want at least 5 (profile, relationships, "+
			"scoped groups, two memberships), or the empty caches were not empty", got)
	}
	if got := pool.flight.peak.Load(); got != 1 {
		t.Errorf("database statements in flight at once = %d, want 1", got)
	}
}

// 🔑 A LOOKUP THE CACHE COULD NOT ANSWER AHEAD GOES STRAIGHT TO THE DATABASE. The read made
// ahead already asked the cache and missed; asking it again on the lookup would be a second
// bucket round trip for every miss — the case reading ahead exists to make cheaper, a fleet
// reporting less often than every 5 s missing its relationships on every event — and a
// second miss counted. So with every cache empty, each cache is read exactly once per
// lookup the event makes: the device, its profile, its relationships, the scoped-group gate
// and one membership read per target (the device and its one anchor). With the in-process
// tier on as well as off: with it on, a bucket miss is not kept in memory, so a second
// lookup would reach the bucket again.
func TestAColdEventAsksEachCacheOncePerLookup(t *testing.T) {
	for _, tier := range []struct {
		name string
		opts []messaging.CacheOption
	}{
		{"bucket only", []messaging.CacheOption{messaging.WithoutLocalCache()}},
		{"memory and bucket", nil},
	} {
		t.Run(tier.name, func(t *testing.T) {
			rig := newCountingResolveRig(t, true, func(_ string, kv *msgtest.MemoryKV) *messaging.Cache {
				return kv.NewCache(tier.opts...)
			})
			if got := rig.totalGets(); got != 0 {
				t.Fatalf("the rig's caches were read %d times before the event, want 0", got)
			}

			resolved := rig.resolve(tempEvent("21"))
			if got := stampedUnit(t, resolved); got != "Cel" {
				t.Fatalf("stamped unit = %q, want Cel", got)
			}
			want := map[string]int{"DeviceByToken": 1, "ProfileResolutionByType": 1, "RelationshipsBySource": 1,
				"ScopedGroupsExist": 1, "MembershipsByEntity": 2}
			for name, w := range want {
				if got := rig.stores[name].Gets; got != w {
					t.Errorf("%s gets = %d, want %d: a lookup the read-ahead missed asked the cache again",
						name, got, w)
				}
			}
			// The counterweight: the lookups really did miss, and went to the database.
			if len(rig.statements()) == 0 {
				t.Fatal("the cold event made no database read; the caches were not empty")
			}
		})
	}
}

// The instrument for the test above: two statements from two goroutines are seen at once.
// If SQLite serialized them below the pool, the test above would pass for the wrong reason.
func TestTheStatementCounterSeesOverlap(t *testing.T) {
	rig := newSlowRig(t, false, 0)
	pool := holdStatements(t, rig.db, 30*time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var devices []model.Device
			_ = rig.db.WithContext(rig.ctx).Find(&devices).Error
		}()
	}
	wg.Wait()
	if got := pool.flight.peak.Load(); got != 2 {
		t.Fatalf("two concurrent statements peaked at %d in flight, want 2", got)
	}
}

// holdingPool counts the statements in flight at the connection pool, holding each for
// hold. It sits where a statement takes a connection, not in gorm's callbacks: a query that
// builds another query while it is being built (a filter that looks a row up, say) is
// nested there without holding two connections.
type holdingPool struct {
	gorm.ConnPool
	hold       time.Duration
	flight     inFlight
	statements atomic.Int32
}

func (p *holdingPool) around(f func()) {
	p.statements.Add(1)
	p.flight.enter()
	time.Sleep(p.hold)
	f()
	p.flight.leave()
}

func (p *holdingPool) QueryContext(ctx context.Context, query string, args ...any) (rows *sql.Rows, err error) {
	p.around(func() { rows, err = p.ConnPool.QueryContext(ctx, query, args...) })
	return rows, err
}

func (p *holdingPool) QueryRowContext(ctx context.Context, query string, args ...any) (row *sql.Row) {
	p.around(func() { row = p.ConnPool.QueryRowContext(ctx, query, args...) })
	return row
}

func (p *holdingPool) ExecContext(ctx context.Context, query string, args ...any) (res sql.Result, err error) {
	p.around(func() { res, err = p.ConnPool.ExecContext(ctx, query, args...) })
	return res, err
}

// holdStatements puts a holdingPool under db, for every session made from it afterwards,
// until the test ends (the fixture's own cleanup needs the pool it opened).
func holdStatements(t *testing.T, db *gorm.DB, hold time.Duration) *holdingPool {
	t.Helper()
	inner := db.ConnPool
	p := &holdingPool{ConnPool: inner, hold: hold}
	db.ConnPool = p
	db.Statement.ConnPool = p
	t.Cleanup(func() {
		db.ConnPool = inner
		db.Statement.ConnPool = inner
	})
	return p
}

// 🔑 A MEASUREMENT THAT FAILS VALIDATION NEVER READS RELATIONSHIPS OR SCOPED GROUPS FROM THE
// DATABASE, as before the reads were made at once. The caches are read ahead; the database
// is read only where the lookup is used, which an invalid event never reaches.
func TestAnInvalidMeasurementReadsOnlyItsProfileFromTheDatabase(t *testing.T) {
	rig := newSlowRig(t, true, 0)
	rig.resetCounters()

	_, reason, err := rig.rez.ResolveEvent(rig.ctx, tempEvent("not-a-number"))
	if err == nil || reason != uint(dmproto.FailureReason_Invalid) {
		t.Fatalf("resolve = (reason %d, %v), want Invalid", reason, err)
	}
	for _, s := range rig.statements() {
		if strings.Contains(s, "entity_relationships") || strings.Contains(s, "entity_group") {
			t.Errorf("an invalid measurement read the database beyond its profile: %s", s)
		}
	}
	if len(rig.statements()) == 0 {
		t.Fatal("the invalid measurement made no database read at all; the caches were not empty")
	}
}

// The same through the CachedApi and its read-ahead: with the caches empty and the
// relationships table gone, an invalid measurement is still Invalid, and a valid one fails
// on the relationships read. The second is the counterweight: it shows the relationship
// read really does fail in this rig.
func TestThroughTheCachesAnInvalidMeasurementWinsOverARelationshipFailure(t *testing.T) {
	rig := newSlowRig(t, false, 0)
	if err := rig.db.Migrator().DropTable(&model.EntityRelationship{}); err != nil {
		t.Fatalf("drop relationships: %v", err)
	}
	if _, reason, err := rig.rez.ResolveEvent(rig.ctx, tempEvent("not-a-number")); err == nil ||
		reason != uint(dmproto.FailureReason_Invalid) {
		t.Errorf("invalid measurement = (reason %d, %v), want Invalid", reason, err)
	}
	if _, reason, err := rig.rez.ResolveEvent(rig.ctx, tempEvent("21")); err == nil ||
		reason != uint(dmproto.FailureReason_ApiCallFailed) {
		t.Errorf("valid measurement = (reason %d, %v), want ApiCallFailed from the relationships read", reason, err)
	}
}

// failingRelationships is an api whose tracked-relationship read fails, and whose profile
// declares temp an integer.
type failingRelationships struct {
	model.DeviceManagementApi
}

func (failingRelationships) ProfileResolutionByDeviceType(context.Context, uint) (*model.ProfileResolution, error) {
	return &model.ProfileResolution{Metrics: []model.ResolvedMetric{{MetricKey: "temp", DataType: string(model.MetricInt)}}}, nil
}

func (failingRelationships) TrackedRelationshipsForDevice(context.Context, uint) (*model.EntityRelationshipSearchResults, error) {
	return nil, errors.New("relationships unavailable")
}

// Failures are reported in the order they were: an invalid measurement is Invalid even
// when its relationships could not be read.
func TestAnInvalidMeasurementWinsOverARelationshipFailure(t *testing.T) {
	rez := NewEventResolver(1, failingRelationships{}, "disabled", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	ctx := core.WithTenant(context.Background(), "acme")
	_, reason, err := rez.HandleEvent(ctx, &model.Device{}, tempEvent("abc"))
	if err == nil || reason != uint(dmproto.FailureReason_Invalid) {
		t.Fatalf("resolve = (reason %d, %v), want Invalid", reason, err)
	}
}

// BenchmarkResolveWithSlowCacheReads is the wall time of resolving one event when the
// key-value reads it cannot answer from memory each take 1.5 ms, the mean bucket read
// measured on a three-node GKE cluster. The shapes are which reads miss memory:
//
//   - none: every read answered from process memory.
//   - relationships: only the per-device relationships read misses. This is a fleet
//     reporting less often than every 5 s: its per-type and per-tenant entries stay warm.
//   - profile+relationships+scope: all three per-event reads miss, as on a cold replica
//     or when a per-type entry expires on the same event as a per-device one.
//   - scoped,3-targets: as above, in a tenant with a rule-scoped group, for a device tracked
//     to two areas, so its three membership reads miss too.
//
// The device lookup is answered from memory in every shape, so what is measured is the
// reads after it.
//
//	go test -run '^$' -bench ResolveWithSlowCacheReads -benchtime 200x ./processor/
func BenchmarkResolveWithSlowCacheReads(b *testing.B) {
	const hold = 1500 * time.Microsecond
	for _, shape := range []struct {
		name   string
		scoped bool
		miss   map[string]bool
	}{
		{"none", false, map[string]bool{}},
		{"relationships", false, map[string]bool{"RelationshipsBySource": true}},
		{"profile+relationships+scope", false, map[string]bool{"ProfileResolutionByType": true,
			"RelationshipsBySource": true, "ScopedGroupsExist": true}},
		{"scoped,3-targets", true, map[string]bool{"ProfileResolutionByType": true,
			"RelationshipsBySource": true, "ScopedGroupsExist": true, "MembershipsByEntity": true}},
	} {
		b.Run(shape.name, func(b *testing.B) {
			var armed atomic.Bool
			var flight inFlight
			rig := buildResolveRig(b, benchResolveDB(b), shape.scoped, 0,
				func(field string, kv *msgtest.MemoryKV) *messaging.Cache {
					s := &slowKV{MemoryKV: kv, hold: hold, armed: &armed, flight: &flight}
					if shape.miss[field] {
						return messaging.NewCacheOver(s, messaging.WithoutLocalCache())
					}
					return messaging.NewCacheOver(s)
				})
			if shape.scoped {
				addAnchor(b, rig, 2)
			}
			rig.resolve(tempEvent("21")) // fills every store and every in-process copy
			armed.Store(true)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := rig.rez.ResolveEvent(rig.ctx, tempEvent("21")); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
