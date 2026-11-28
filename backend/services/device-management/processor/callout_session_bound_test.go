// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// memKV is an in-memory sessionDiscriminatorStore with the jetstream semantics the bound relies
// on: Get of a missing key is ErrKeyNotFound, Create over a live key is ErrKeyExists, Update at a
// stale revision is ErrKeyRevisionMismatch, revisions are one sequence, and every call honours ctx.
type memKV struct {
	mu   sync.Mutex
	seq  uint64
	keys map[string]memEntry
	ops  []string

	// Fail, when set, is returned by every call. FailWrites only by Create and Update.
	Fail, FailWrites error
	// BeforeWrite runs before every Create and Update is applied, with the lock released.
	BeforeWrite func(key string)
	// Delay makes every call wait this long first, or until ctx is done.
	Delay time.Duration
}

type memEntry struct {
	value []byte
	rev   uint64
}

func newMemKV() *memKV { return &memKV{keys: map[string]memEntry{}} }

func (s *memKV) wait(ctx context.Context) error {
	if s.Delay == 0 {
		return ctx.Err()
	}
	select {
	case <-time.After(s.Delay):
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *memKV) Get(ctx context.Context, key string) (jetstream.KeyValueEntry, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops = append(s.ops, "get")
	if s.Fail != nil {
		return nil, s.Fail
	}
	e, ok := s.keys[key]
	if !ok {
		return nil, jetstream.ErrKeyNotFound
	}
	return memKVEntry{key: key, value: bytes.Clone(e.value), rev: e.rev}, nil
}

func (s *memKV) Create(ctx context.Context, key string, value []byte, _ ...jetstream.KVCreateOpt) (uint64, error) {
	return s.put(ctx, "create", key, value, 0)
}

func (s *memKV) Update(ctx context.Context, key string, value []byte, rev uint64) (uint64, error) {
	return s.put(ctx, "update", key, value, rev)
}

func (s *memKV) put(ctx context.Context, op, key string, value []byte, rev uint64) (uint64, error) {
	s.mu.Lock()
	hook := s.BeforeWrite
	s.mu.Unlock()
	if hook != nil {
		hook(key)
	}
	if err := s.wait(ctx); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops = append(s.ops, op)
	if s.Fail != nil {
		return 0, s.Fail
	}
	if s.FailWrites != nil {
		return 0, s.FailWrites
	}
	e, ok := s.keys[key]
	switch {
	case op == "create" && ok:
		return 0, jetstream.ErrKeyExists
	case op == "update" && (!ok || e.rev != rev):
		return 0, jetstream.ErrKeyRevisionMismatch
	}
	s.seq++
	s.keys[key] = memEntry{value: bytes.Clone(value), rev: s.seq}
	return s.seq, nil
}

func (s *memKV) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, op := range s.ops {
		if op == "create" || op == "update" {
			n++
		}
	}
	return n
}

func (s *memKV) opCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ops)
}

type memKVEntry struct {
	key   string
	value []byte
	rev   uint64
}

func (e memKVEntry) Bucket() string                  { return "mem" }
func (e memKVEntry) Key() string                     { return e.key }
func (e memKVEntry) Value() []byte                   { return e.value }
func (e memKVEntry) Revision() uint64                { return e.rev }
func (e memKVEntry) Created() time.Time              { return time.Time{} }
func (e memKVEntry) Delta() uint64                   { return 0 }
func (e memKVEntry) Operation() jetstream.KeyValueOp { return jetstream.KeyValuePut }

// fakeEvictor stands in for the broker: sessions holds the client ids that exist, keyed by their
// digest, and Evict removes one when owned accepts it.
type fakeEvictor struct {
	mu       sync.Mutex
	sessions map[string]string
	evicted  []string
	err      error
}

func newFakeEvictor() *fakeEvictor { return &fakeEvictor{sessions: map[string]string{}} }

func (f *fakeEvictor) open(clientID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[messaging.MqttSessionClientIDHash(clientID)] = clientID
}

func (f *fakeEvictor) Evict(ctx context.Context, idHash string, owned func(string) bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	id, ok := f.sessions[idHash]
	if !ok || !owned(id) {
		return false, nil
	}
	delete(f.sessions, idHash)
	f.evicted = append(f.evicted, id)
	return true, nil
}

func (f *fakeEvictor) evictedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.evicted...)
}

// suffixed counts the sessions the broker holds that carry a discriminator.
func (f *fakeEvictor) suffixed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, id := range f.sessions {
		if strings.Count(id, ":") > 2 {
			n++
		}
	}
	return n
}

func testIndexKey(t *testing.T) *SessionIndexKey {
	t.Helper()
	k, err := NewSessionIndexKey(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// sessionRig is a responder whose access-token credentials resolve to a device named by the
// credential id itself ("{tenant}:{device}" usernames), so one rig can connect many devices in
// many tenants, with the session bounds over an in-memory bucket and evictor and a clock the test
// moves. A granted connect "opens" its session at the fake broker.
type sessionRig struct {
	r       *CalloutResponder
	store   *memKV
	evictor *fakeEvictor
	now     time.Time
	vec     *prometheus.CounterVec
	evicted prometheus.Counter
}

func newSessionRig(t *testing.T, maxSessions, burst, perMinute int) *sessionRig {
	t.Helper()
	rig := &sessionRig{store: newMemKV(), evictor: newFakeEvictor(), now: time.Unix(1_780_000_000, 0)}
	r, _ := newTestResponder(t, func(_ context.Context, p *model.PresentedCredential) (*model.Device, error) {
		d := &model.Device{}
		d.Token = p.CredentialId
		return d, nil
	})
	r.now = func() time.Time { return rig.now }
	rig.vec = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "callout_session_limited_total"}, []string{"reason"})
	rig.evicted = prometheus.NewCounter(prometheus.CounterOpts{Name: "callout_sessions_evicted_total"})
	r.metrics = &CalloutMetrics{SessionLimited: rig.vec, SessionsEvicted: rig.evicted}
	r.WithSessionBounds(rig.store, testIndexKey(t), rig.evictor, maxSessions, burst, perMinute)
	rig.r = r
	return rig
}

func clientID(tenant, device, discriminator string) string {
	id := "inst-1:" + tenant + ":" + device
	if discriminator != "" {
		id += ":" + discriminator
	}
	return id
}

// connect presents device's own access token from client id
// "inst-1:{tenant}:{device}[:{discriminator}]" and reports whether it was granted.
func (s *sessionRig) connect(t *testing.T, tenant, device, discriminator string) bool {
	t.Helper()
	id := clientID(tenant, device, discriminator)
	jwt, errMsg := s.r.authorize(testRequestFromClient(t, tenant+":"+device, "", id))
	if errMsg != "" && errMsg != genericAuthFailure {
		t.Fatalf("a refusal must be the generic one, got %q", errMsg)
	}
	ok := jwt != "" && errMsg == ""
	if ok {
		s.evictor.open(id)
	}
	return ok
}

func (s *sessionRig) limited(reason string) float64 {
	return testutil.ToFloat64(s.vec.WithLabelValues(reason))
}

// A generous connect rate for the cap tests, so only the cap can act.
const roomyBurst, roomyRate = 10000, 60000

// Past the cap a new id is ADMITTED, and the least recently seen id's session is evicted to make
// room: the broker never holds more than the cap of one device's suffixed sessions.
func TestSessionCapEvictsTheLeastRecentlySeenToAdmitANewId(t *testing.T) {
	rig := newSessionRig(t, 3, roomyBurst, roomyRate)
	for _, d := range []string{"a", "b", "c"} {
		rig.now = rig.now.Add(time.Minute)
		if !rig.connect(t, "acme-corp", "sensor-001", d) {
			t.Fatalf("id %q under the cap was refused", d)
		}
	}
	rig.now = rig.now.Add(time.Minute)
	if !rig.connect(t, "acme-corp", "sensor-001", "d") {
		t.Fatal("a fourth id was refused: past the cap a device must be admitted after an eviction")
	}
	if got := rig.evictor.evictedIDs(); len(got) != 1 || got[0] != clientID("acme-corp", "sensor-001", "a") {
		t.Fatalf("evicted %v, want exactly the least recently seen id (a)", got)
	}
	if n := rig.evictor.suffixed(); n != 3 {
		t.Fatalf("the broker holds %d of the device's suffixed sessions, want the cap of 3", n)
	}
	if got := testutil.ToFloat64(rig.evicted); got != 1 {
		t.Fatalf("sessions_evicted = %v, want 1", got)
	}
	// Twenty more fresh ids: always admitted, never over the cap.
	for i := 0; i < 20; i++ {
		rig.now = rig.now.Add(time.Minute)
		if !rig.connect(t, "acme-corp", "sensor-001", fmt.Sprintf("boot-%d", i)) {
			t.Fatalf("fresh id %d refused", i)
		}
		if n := rig.evictor.suffixed(); n > 3 {
			t.Fatalf("after fresh id %d the broker holds %d sessions, over the cap", i, n)
		}
	}
	// The plain id is never counted or evicted.
	if !rig.connect(t, "acme-corp", "sensor-001", "") {
		t.Fatal("the plain client id was refused")
	}
	for _, id := range rig.evictor.evictedIDs() {
		if id == clientID("acme-corp", "sensor-001", "") {
			t.Fatal("the plain client id's session was evicted")
		}
	}
}

// Recency, not age, picks the victim: reconnecting with an old id moves it to the back.
func TestSessionCapEvictsByLastSeen(t *testing.T) {
	rig := newSessionRig(t, 2, roomyBurst, roomyRate)
	rig.connect(t, "acme-corp", "sensor-001", "first")
	rig.now = rig.now.Add(time.Hour)
	rig.connect(t, "acme-corp", "sensor-001", "second")
	rig.now = rig.now.Add(48 * time.Hour) // past the daily refresh, so the reconnect rewrites last-seen
	rig.connect(t, "acme-corp", "sensor-001", "first")
	rig.now = rig.now.Add(time.Hour)
	if !rig.connect(t, "acme-corp", "sensor-001", "third") {
		t.Fatal("third id refused")
	}
	if got := rig.evictor.evictedIDs(); len(got) != 1 || got[0] != clientID("acme-corp", "sensor-001", "second") {
		t.Fatalf("evicted %v, want the least recently SEEN id (second)", got)
	}
}

// Nothing releases a slot by time: a year later, the old ids still count, and a new one still
// evicts one of them rather than adding to them.
func TestSessionCapReleasesNoSlotByTime(t *testing.T) {
	rig := newSessionRig(t, 2, roomyBurst, roomyRate)
	rig.connect(t, "acme-corp", "sensor-001", "a")
	rig.connect(t, "acme-corp", "sensor-001", "b")
	rig.now = rig.now.Add(365 * 24 * time.Hour)
	rig.connect(t, "acme-corp", "sensor-001", "c")
	if got := rig.evictor.evictedIDs(); len(got) != 1 {
		t.Fatalf("a year on, a new id evicted %v: the old slots must still count", got)
	}
	if n := rig.evictor.suffixed(); n != 2 {
		t.Fatalf("the broker holds %d suffixed sessions, want 2", n)
	}
}

func TestSessionCapAlwaysAdmitsAKnownId(t *testing.T) {
	rig := newSessionRig(t, 2, roomyBurst, roomyRate)
	for _, d := range []string{"pub", "sub"} {
		if !rig.connect(t, "acme-corp", "sensor-001", d) {
			t.Fatalf("id %q refused under the cap", d)
		}
	}
	for day := 0; day < 3; day++ {
		for _, d := range []string{"pub", "sub", "pub"} {
			if !rig.connect(t, "acme-corp", "sensor-001", d) {
				t.Fatalf("known id %q refused on day %d", d, day)
			}
		}
		rig.now = rig.now.Add(25 * time.Hour)
	}
	if got := rig.evictor.evictedIDs(); len(got) != 0 {
		t.Fatalf("reconnecting known ids evicted %v", got)
	}
	// A failing last-seen rewrite does not refuse a known one.
	rig.store.FailWrites = errors.New("bucket full")
	if !rig.connect(t, "acme-corp", "sensor-001", "pub") {
		t.Fatal("a known id was refused because its last-seen rewrite failed")
	}
}

func TestSessionCapIsPerDeviceAndPerTenant(t *testing.T) {
	rig := newSessionRig(t, 1, roomyBurst, roomyRate)
	rig.connect(t, "acme-corp", "sensor-001", "x")
	rig.connect(t, "acme-corp", "sensor-002", "x")
	rig.connect(t, "globex", "sensor-001", "x")
	if got := rig.evictor.evictedIDs(); len(got) != 0 {
		t.Fatalf("a device's first id evicted another device's session: %v", got)
	}
	rig.now = rig.now.Add(time.Minute)
	rig.connect(t, "acme-corp", "sensor-001", "y")
	if got := rig.evictor.evictedIDs(); len(got) != 1 || got[0] != clientID("acme-corp", "sensor-001", "x") {
		t.Fatalf("evicted %v, want only acme-corp/sensor-001's own x", got)
	}
}

// The bucket holds no identifier: no key or value contains a tenant, device, discriminator or the
// broker's unkeyed session digest, and the entry key differs per tenant for the same device token.
func TestSessionIndexHoldsNoIdentifiers(t *testing.T) {
	rig := newSessionRig(t, 4, roomyBurst, roomyRate)
	rig.connect(t, "acme-corp", "sensor-001", "visible-suffix")
	rig.connect(t, "globex", "sensor-001", "visible-suffix")
	rig.store.mu.Lock()
	defer rig.store.mu.Unlock()
	if len(rig.store.keys) != 2 {
		t.Fatalf("want one entry per (tenant, device), got %d", len(rig.store.keys))
	}
	for k, e := range rig.store.keys {
		for _, s := range []string{"acme", "globex", "sensor", "visible", "inst-1",
			messaging.MqttSessionClientIDHash(clientID("acme-corp", "sensor-001", "visible-suffix"))} {
			if strings.Contains(k, s) || bytes.Contains(e.value, []byte(s)) {
				t.Fatalf("the bucket holds %q in plain (key %q value %s)", s, k, e.value)
			}
		}
	}
}

func TestSessionCapPlainIdTouchesNoStore(t *testing.T) {
	rig := newSessionRig(t, 1, roomyBurst, roomyRate)
	rig.store.Fail = errors.New("broker down")
	for i := 0; i < 5; i++ {
		if !rig.connect(t, "acme-corp", "sensor-001", "") {
			t.Fatal("a plain client id was refused while the session bucket is down")
		}
	}
	if rig.store.opCount() != 0 {
		t.Fatal("a plain client id read or wrote the session bucket")
	}
	if rig.connect(t, "acme-corp", "sensor-001", "x") {
		t.Fatal("a new id was granted while the bucket could not be read")
	}
	if got := rig.limited(sessionLimitedStoreUnavailable); got != 1 {
		t.Fatalf("session_store_unavailable = %v, want 1", got)
	}
}

func TestSessionCapRefusesWhenTheNewIdCannotBeRecorded(t *testing.T) {
	rig := newSessionRig(t, 4, roomyBurst, roomyRate)
	rig.store.FailWrites = errors.New("insufficient resources")
	if rig.connect(t, "acme-corp", "sensor-001", "x") {
		t.Fatal("an id that could not be recorded was granted")
	}
	if got := rig.limited(sessionLimitedStoreUnavailable); got != 1 {
		t.Fatalf("session_store_unavailable = %v, want 1", got)
	}
}

// An eviction that fails refuses the connect rather than admitting an uncounted session, and
// leaves the slot (and the session it counts) where it was.
func TestSessionCapRefusesWhenTheEvictionFails(t *testing.T) {
	rig := newSessionRig(t, 1, roomyBurst, roomyRate)
	rig.connect(t, "acme-corp", "sensor-001", "x")
	rig.evictor.err = errors.New("broker unreachable")
	writes := rig.store.writes()
	if rig.connect(t, "acme-corp", "sensor-001", "y") {
		t.Fatal("a new id was granted although the session it must replace could not be evicted")
	}
	if got := rig.limited(sessionLimitedEvictFailed); got != 1 {
		t.Fatalf("session_evict_failed = %v, want 1", got)
	}
	if rig.store.writes() != writes {
		t.Fatal("a failed eviction still rewrote the entry, releasing a slot whose session exists")
	}
	rig.evictor.err = nil
	if !rig.connect(t, "acme-corp", "sensor-001", "x") {
		t.Fatal("the recorded id was lost by the failed eviction")
	}
	if len(rig.evictor.evictedIDs()) != 0 {
		t.Fatal("reconnecting the recorded id evicted something")
	}
}

// An unreadable entry is NOT reset (that would release every slot without evicting a session);
// new ids fail closed, the plain id is unaffected.
func TestSessionCapDoesNotResetAnUnreadableEntry(t *testing.T) {
	rig := newSessionRig(t, 1, roomyBurst, roomyRate)
	key := testIndexKey(t).entryKey("acme-corp", "sensor-001")
	if _, err := rig.store.Create(context.Background(), key, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if rig.connect(t, "acme-corp", "sensor-001", "x") {
		t.Fatal("an unreadable entry was overwritten to admit a new id")
	}
	if !rig.connect(t, "acme-corp", "sensor-001", "") {
		t.Fatal("the plain id was refused")
	}
}

// Two replicas recording a new id for the same device at once: the loser re-reads and counts the
// winner's, so the cap holds across replicas, without refusing either connect.
func TestSessionCapHoldsAcrossARacingCreate(t *testing.T) {
	rig := newSessionRig(t, 2, roomyBurst, roomyRate)
	other := rig.r.sessions
	raced := false
	rig.store.BeforeWrite = func(string) {
		if raced {
			return
		}
		raced = true
		if why := other.admit(context.Background(), "acme-corp", "sensor-001", clientID("acme-corp", "sensor-001", ""), ":replica-b", rig.now); why != "" {
			t.Errorf("the competing replica was refused: %s", why)
		}
		rig.evictor.open(clientID("acme-corp", "sensor-001", "replica-b"))
	}
	if !rig.connect(t, "acme-corp", "sensor-001", "replica-a") {
		t.Fatal("the connect that lost the race was refused instead of retried")
	}
	rig.store.BeforeWrite = nil
	rig.now = rig.now.Add(time.Minute)
	if !rig.connect(t, "acme-corp", "sensor-001", "third") {
		t.Fatal("third id refused")
	}
	if n := rig.evictor.suffixed(); n != 2 {
		t.Fatalf("the broker holds %d sessions after the race, want the cap of 2", n)
	}
}

// Two replicas AT THE CAP each evict to admit a different new id, and the updates race: the loser
// re-reads, sees the winner's id, and evicts again, so the cap still holds.
func TestSessionCapHoldsAcrossRacingUpdatesAtTheCap(t *testing.T) {
	rig := newSessionRig(t, 2, roomyBurst, roomyRate)
	rig.connect(t, "acme-corp", "sensor-001", "a")
	rig.now = rig.now.Add(time.Minute)
	rig.connect(t, "acme-corp", "sensor-001", "b")
	rig.now = rig.now.Add(time.Minute)
	other := rig.r.sessions
	raced := false
	rig.store.BeforeWrite = func(string) {
		if raced {
			return
		}
		raced = true
		if why := other.admit(context.Background(), "acme-corp", "sensor-001", clientID("acme-corp", "sensor-001", ""), ":from-b", rig.now); why != "" {
			t.Errorf("the competing replica was refused: %s", why)
		}
		rig.evictor.open(clientID("acme-corp", "sensor-001", "from-b"))
	}
	if !rig.connect(t, "acme-corp", "sensor-001", "from-a") {
		t.Fatal("the replica that lost the update race was refused")
	}
	if n := rig.evictor.suffixed(); n > 2 {
		t.Fatalf("after racing updates at the cap the broker holds %d sessions, over the cap of 2", n)
	}
	if !rig.connect(t, "acme-corp", "sensor-001", "from-a") || !rig.connect(t, "acme-corp", "sensor-001", "from-b") {
		t.Fatal("one of the racing ids was not recorded")
	}
	if n := rig.evictor.suffixed(); n != 2 {
		t.Fatalf("the broker holds %d sessions, want 2", n)
	}
}

// A device whose every write loses (a pathological race) is refused after the bounded retries,
// not looped forever and not admitted unrecorded.
func TestSessionCapRetryExhaustion(t *testing.T) {
	rig := newSessionRig(t, 4, roomyBurst, roomyRate)
	n := 0
	rig.store.BeforeWrite = func(key string) {
		n++
		rig.store.mu.Lock()
		rig.store.seq++
		rig.store.keys[key] = memEntry{value: []byte(`{"s":{}}`), rev: rig.store.seq}
		rig.store.mu.Unlock()
	}
	if rig.connect(t, "acme-corp", "sensor-001", "x") {
		t.Fatal("a connect whose every write lost was granted")
	}
	if n != sessionDiscriminatorWriteAttempts {
		t.Fatalf("tried %d writes, want exactly %d", n, sessionDiscriminatorWriteAttempts)
	}
	if got := rig.limited(sessionLimitedStoreUnavailable); got != 1 {
		t.Fatalf("session_store_unavailable = %v, want 1", got)
	}
}

// The connect's deadline bounds every store call, and a connect that ran out of time records
// nothing: it never takes a slot.
func TestSessionCapHonoursTheDeadline(t *testing.T) {
	rig := newSessionRig(t, 4, roomyBurst, roomyRate)
	rig.r.lookupTimeout = 50 * time.Millisecond
	rig.store.Delay = time.Second
	start := time.Now()
	if rig.connect(t, "acme-corp", "sensor-001", "x") {
		t.Fatal("a connect whose bucket calls outlived the deadline was granted")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("the store call ran %v past a 50ms deadline", elapsed)
	}
	rig.store.Delay = 0
	if rig.store.writes() != 0 {
		t.Fatal("a timed-out connect wrote a slot")
	}
	// The same at the write: the read lands, the deadline passes before the write.
	rig.store.BeforeWrite = func(string) { time.Sleep(80 * time.Millisecond) }
	if rig.connect(t, "acme-corp", "sensor-001", "y") {
		t.Fatal("a connect whose deadline passed before its write was granted")
	}
	rig.store.mu.Lock()
	entries := len(rig.store.keys)
	rig.store.mu.Unlock()
	if entries != 0 {
		t.Fatal("a connect whose deadline passed before its write recorded a slot")
	}
}

func TestConnectRateIsPerDevice(t *testing.T) {
	rig := newSessionRig(t, 16, 3, 60) // three at once, then one a second
	for i := 0; i < 3; i++ {
		if !rig.connect(t, "acme-corp", "sensor-001", "") {
			t.Fatalf("connect %d inside the burst was refused", i+1)
		}
	}
	if rig.connect(t, "acme-corp", "sensor-001", "") {
		t.Fatal("a fourth connect inside the same instant was granted over a burst of 3")
	}
	if got := rig.limited(sessionLimitedConnectRate); got != 1 {
		t.Fatalf("connect_rate = %v, want 1", got)
	}
	if !rig.connect(t, "acme-corp", "sensor-002", "") || !rig.connect(t, "globex", "sensor-001", "") {
		t.Fatal("another device was refused by sensor-001's connect rate")
	}
	rig.now = rig.now.Add(time.Second)
	if !rig.connect(t, "acme-corp", "sensor-001", "") {
		t.Fatal("the connect that refilled after a second was refused")
	}
	if rig.connect(t, "acme-corp", "sensor-001", "") {
		t.Fatal("more than the refill was granted")
	}
	// A connect refused by the rate touches no bucket and evicts nothing.
	if rig.connect(t, "acme-corp", "sensor-001", "rate-refused") {
		t.Fatal("over the rate, a connect with a discriminator was granted")
	}
	if rig.store.opCount() != 0 {
		t.Fatal("a connect refused by the rate used the session bucket")
	}
}

func TestConnectLimiterSweepsRefilledBuckets(t *testing.T) {
	l := newConnectLimiter(2, 60, 100)
	now := time.Unix(1_780_000_000, 0)
	for _, d := range []string{"a", "b", "c"} {
		if !l.allow("t", d, now) {
			t.Fatalf("%s refused on its first connect", d)
		}
	}
	if l.size() != 3 {
		t.Fatalf("size = %d, want 3", l.size())
	}
	now = now.Add(connectLimiterSweepEvery)
	if !l.allow("t", "a", now) {
		t.Fatal("a refused after refilling")
	}
	if l.size() != 1 {
		t.Fatalf("size after the sweep = %d, want 1", l.size())
	}
}

// The limiter never holds more than its bound, and at saturation it fails OPEN for devices it has
// no bucket for (counted), while a device that does have one is still limited.
func TestConnectLimiterSaturation(t *testing.T) {
	l := newConnectLimiter(1, 1, 3) // one connect, then one a minute
	l.saturated = prometheus.NewCounter(prometheus.CounterOpts{Name: "sat"})
	now := time.Unix(1_780_000_000, 0)
	l.lastSweep = now
	for _, d := range []string{"a", "b", "c"} {
		if !l.allow("t", d, now) {
			t.Fatalf("%s refused on its first connect", d)
		}
	}
	for i := 0; i < 10; i++ {
		if !l.allow("t", fmt.Sprintf("new-%d", i), now) {
			t.Fatal("a device with no bucket was refused at saturation: the limiter must fail open")
		}
	}
	if l.size() != 3 {
		t.Fatalf("the limiter holds %d buckets, over its bound of 3", l.size())
	}
	if got := testutil.ToFloat64(l.saturated); got != 10 {
		t.Fatalf("saturated = %v, want 10", got)
	}
	if l.allow("t", "a", now) {
		t.Fatal("a tracked device was let through its limit at saturation")
	}
	// Once the buckets refill, the forced sweep makes room again without waiting for the minute.
	now = now.Add(61 * time.Second)
	l.lastSweep = now.Add(-2 * connectLimiterFullSweepEvery)
	if !l.allow("t", "late", now) || l.size() != 1 {
		t.Fatalf("after the buckets refilled a new device was not tracked (size %d)", l.size())
	}
}

func TestSessionBoundsDefaults(t *testing.T) {
	r, _ := newTestResponder(t, nil)
	if m, b, p := r.SessionBounds(); m != 0 || b != 0 || p != 0 {
		t.Fatalf("bounds before WithSessionBounds = %d/%d/%d, want off", m, b, p)
	}
	r.WithSessionBounds(newMemKV(), testIndexKey(t), newFakeEvictor(), 0, 0, 0)
	if m, b, p := r.SessionBounds(); m != DefaultMaxSessionsPerDevice || b != DefaultConnectBurstPerDevice || p != DefaultConnectsPerMinutePerDevice {
		t.Fatalf("defaulted bounds = %d/%d/%d", m, b, p)
	}
	if r.connects.maxDevices != DefaultConnectLimiterMaxDevices {
		t.Fatalf("limiter bound = %d", r.connects.maxDevices)
	}
}

func TestSessionIndexKey(t *testing.T) {
	k := testIndexKey(t)
	other, err := NewSessionIndexKey(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if k.entryKey("a", "b") == other.entryKey("a", "b") {
		t.Fatal("two root keys gave the same entry key")
	}
	if k.entryKey("ab", "c") == k.entryKey("a", "bc") || k.idDigest("t", "d", ":x") == k.idDigest("t", "d", ":y") {
		t.Fatal("distinct inputs collided")
	}
	sealed, err := k.seal("entry-1", "ABCDEFGH")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := k.open("entry-1", sealed); err != nil || got != "ABCDEFGH" {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := k.open("entry-2", sealed); err == nil {
		t.Fatal("a sealed digest opened under another entry key")
	}
	if _, err := NewSessionIndexKey([]byte("short")); err == nil {
		t.Fatal("a short root key was accepted")
	}
}

func TestClientIDDiscriminator(t *testing.T) {
	const req = "inst-1:acme:dev"
	for presented, want := range map[string]string{
		req:              "",
		req + ":a":       ":a",
		req + ":a:b":     ":a:b",
		req + "::":       "::",
		req + ":":        ":",
		req + ":pub-001": ":pub-001",
	} {
		if got := clientIDDiscriminator(presented, req); got != want {
			t.Errorf("clientIDDiscriminator(%q) = %q, want %q", presented, got, want)
		}
	}
}
