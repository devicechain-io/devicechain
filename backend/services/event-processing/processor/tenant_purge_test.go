// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"gorm.io/gorm"
)

// The PROCESSOR SEAM of the ADR-077 DETECT tenant eviction.
//
// The engine sweep itself is pinned in internal/detect/core/eviction_test.go and the three
// view sweeps in internal/runtime/tenant_eviction_test.go; neither is repeated here. What
// this file is about is the thing between them and the purge coordinator — the part that
// decides WHAT THE COORDINATOR IS TOLD, and therefore what goes into a deletion record.
//
// 🔑 THE ONE CLAIM EVERY TEST IN HERE ORBITS: a reply that says "evicted" is a statement
// that the tenant's state will not come back. In-memory removal does not support that
// statement — the engine's contents are one checkpoint row, and a restart reads the row,
// not the memory. So the seam's correctness is not "did the sweep run" (the engine tests
// answer that) but "is the answer sent only for what is DURABLE, and is a failure to make
// it durable reported as a failure rather than as silence or as success".
//
// 🔴 EVERY TEST HERE DRIVES THE REAL run() LOOP. The dispatch under test is one line —
//
//	case tp := <-rp.tenantPurges: tp.reply <- rp.applyTenantPurge(tp.tenant)
//
// — and a test goroutine that mirrors it would exercise a copy, leaving the real case arm's
// existence, its channel, and its liveness alongside the other select arms unmeasured.
// evictionRig.start below launches the actual run() goroutine (the same one
// TestRunLoopCheckpointsAndAcksOnEOF drives), so a purge request that never reaches the
// select — a case arm deleted, a channel never made, a loop parked elsewhere — fails these
// tests by timing out rather than passing against a stand-in. Verified by mutation: deleting
// the case arm fails seven of the tests below.
//
// The two responder tests that stub the eviction (the reply-less-request test and the
// queue-group test) are the deliberate exceptions — what they measure is NATS DELIVERY, and
// driving a real engine there would make the assertion depend on the sweep instead.

const evictInstance = "inst-evict"

// recordingWriter keeps the PAYLOADS of every published derived event, not just a count,
// so a test can ask WHOSE detection was published rather than how many were. captureWriter
// (the shared one) counts, which cannot tell a surviving tenant's detection from a purged
// tenant's.
type recordingWriter struct {
	mu       sync.Mutex
	payloads []string
}

func (w *recordingWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, m := range msgs {
		w.payloads = append(w.payloads, string(m.Value))
	}
	return nil
}

func (w *recordingWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

func (w *recordingWriter) HandleResponse(error) {}

func (w *recordingWriter) recorded() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.payloads...)
}

// evictionRules mints one absence rule per tenant, with the id shape the publish path mints
// ("{tenant}/{profileToken}@{version}/{ruleToken}") so the tenant prefix the eviction matches
// on is real rather than arranged. Absence is chosen because a single heartbeat arms a timer,
// which is state the engine SERIALIZES — the eviction has to be visible in the snapshot bytes
// for the durability assertions below to mean anything.
func evictionRules(t *testing.T, tenants ...string) *runtime.RuleRegistry {
	t.Helper()
	scoped := make([]runtime.ScopedRule, 0, len(tenants))
	for _, tenant := range tenants {
		cr, err := rules0.Compile(rules0.Rule{
			ID:   tenant + "/prof@1/dead",
			Name: "dead",
			Type: rules0.TypeAbsence,
			Ttl:  rules0.Duration(30 * time.Second),
		}, rules0.Limits{})
		if err != nil {
			t.Fatalf("compile absence rule for %q: %v", tenant, err)
		}
		scoped = append(scoped, runtime.ScopedRule{Tenant: tenant, ProfileVersionToken: "prof@1", Compiled: cr})
	}
	return runtime.NewRuleRegistry(scoped)
}

// evictionRig is a processor whose REAL single-writer loop can be started, holding live
// state for the named tenants and a DURABLE checkpoint that already contains all of it.
//
// The pre-committed checkpoint is not setup convenience: it is the non-vacuity baseline. A
// test that asserts "the durable row no longer names the tenant" against a store that never
// held the tenant asserts nothing, so every rig hands back the pre-eviction payload for the
// test to check the tenant IS there first.
type evictionRig struct {
	rp     *ResolvedEventsProcessor
	store  *model.SnapshotStore
	writer *recordingWriter
	// before is the durable snapshot payload committed during setup, i.e. the state the
	// eviction is claiming to erase.
	before   string
	started  bool
	stopOnce sync.Once
	cancel   context.CancelFunc
}

// newEvictionRig builds the processor and commits a checkpoint holding every named tenant's
// state. The loop is NOT running yet, deliberately: loop-owned fields (stale, pendingDets,
// armRetries, attrRetries) are set by the test between here and start(), where writing them
// is ordered before the loop goroutine ever reads them.
func newEvictionRig(t *testing.T, store *model.SnapshotStore, tenants ...string) *evictionRig {
	t.Helper()
	ctx := context.Background()
	reg := evictionRules(t, tenants...)
	w := &recordingWriter{}
	loopCtx, cancel := context.WithCancel(context.Background())
	rp := &ResolvedEventsProcessor{
		// A reader with no script parks on the loop context, so the read pump never feeds the
		// loop and the ONLY thing that wakes the select is the eviction under test.
		ResolvedEventsReader: &fakeReader{},
		Store:                store,
		cfg: Config{
			PartitionId:        "singleton",
			CheckpointEvents:   1000,
			CheckpointInterval: time.Hour,
			TickInterval:       time.Hour,
			Clock:              detectcore.RealClock{},
		},
		registry:  reg,
		publisher: runtime.NewPublisher(w, reg, (*detectMetrics)(nil)),
		clock:     detectcore.RealClock{},
		// 🔴 THE CHANNEL HAS TO BE MADE HERE, and its absence is not a detail this rig gets to
		// hide. A struct-literal processor (the shape every test in this package uses, because
		// the constructor needs a *core.Microservice to mint Prometheus collectors) leaves
		// tenantPurges NIL, and a nil channel blocks BOTH the send and the loop's receive
		// forever — an eviction request that is never accepted and never refused. The first run
		// of this file failed exactly that way. Since the rig supplies the channel rather than
		// the production constructor, the constructor's own wiring is covered separately by
		// TestTheConstructorWiresTheEvictionChannel.
		tenantPurges: make(chan tenantPurgeRequest),
		procCtx:      loopCtx,
		procCancel:   cancel,
	}
	if err := rp.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	// One heartbeat per tenant arms that tenant's absence timer: real engine state, minted
	// through the real fan-out, keyed on an id carrying the tenant.
	for i, tenant := range tenants {
		rp.handle(measuredMsg(t, uint64(i+1), tenant, "d1", "prof@1", "temperature", "20", &fakeAck{}))
	}
	if !rp.checkpoint(ctx) {
		t.Fatal("setup checkpoint did not commit; the durability assertions below would be vacuous")
	}
	rig := &evictionRig{rp: rp, store: store, writer: w, cancel: cancel}
	rig.before = rig.payload(t)
	t.Cleanup(rig.stop)
	return rig
}

// start launches the real run() loop.
func (r *evictionRig) start(t *testing.T) {
	t.Helper()
	r.started = true
	r.rp.readerWG.Add(1)
	go r.rp.run()
}

// stop cancels the loop and JOINS it. The join is what makes it safe for a test to read the
// loop-owned fields afterwards (pendingDets, armRetries, attrRetries, engine): they are
// written only on the loop goroutine, and its exit happens-before Wait returns.
func (r *evictionRig) stop() {
	r.stopOnce.Do(func() {
		r.cancel()
		if r.started {
			r.rp.readerWG.Wait()
		}
	})
}

// payload reads the DURABLE snapshot back through the store — through the same Load a
// restart uses, never off the in-memory engine. The whole point of the seam is that those
// two can disagree.
func (r *evictionRig) payload(t *testing.T) string {
	t.Helper()
	snap, ok, err := r.store.Load(context.Background(), "singleton")
	if err != nil {
		t.Fatalf("load durable snapshot: %v", err)
	}
	if !ok {
		t.Fatal("no durable snapshot row exists")
	}
	return string(snap.Payload)
}

// evict calls the real EvictTenant with a bounded context, so a loop that never receives
// the request fails the test instead of hanging it.
func (r *evictionRig) evict(t *testing.T, tenant string) (int64, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.rp.EvictTenant(ctx, tenant)
}

// newBreakableStore is newTestStore plus a handle on the underlying gorm DB, so a test can
// make Save FAIL. The store's own DB is unexported, and there is no fault injection point on
// the concrete *model.SnapshotStore the processor holds, so breaking the schema out from
// under it is how a commit failure is produced without replacing the real store with a fake
// — the failure then happens inside the real Save, in a real transaction.
func newBreakableStore(t *testing.T) (*model.SnapshotStore, func()) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.DetectSnapshot{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store := model.NewSnapshotStore(&rdb.RdbManager{Database: db})
	return store, func() {
		t.Helper()
		if err := db.Migrator().DropTable(&model.DetectSnapshot{}); err != nil {
			t.Fatalf("drop snapshot table: %v", err)
		}
	}
}

// TestEvictionAnswersOnlyAfterTheCheckpointCommits is the claim the purge coordinator
// writes into the deletion record, asserted where it lands: the DURABLE row.
//
// 🔑 The oracle is the snapshot read back THROUGH THE STORE, not the engine's memory. An
// assertion on the engine would pass for an eviction that never committed — which is
// precisely the failure this seam exists to prevent, because the coordinator would then have
// recorded an erasure that the next restart undoes by replaying the row.
//
// The ordering claim ("only after") is carried by the fact that the assertion runs
// immediately on EvictTenant's return, with nothing in between: the reply is sent by the
// loop after applyTenantPurge returns, and applyTenantPurge returns after checkpoint()
// commits, so if the read below found the tenant still there the reply would have preceded
// its own commit.
func TestEvictionAnswersOnlyAfterTheCheckpointCommits(t *testing.T) {
	rig := newEvictionRig(t, newTestStore(t), "acme", "globex")
	if !strings.Contains(rig.before, "acme") {
		t.Fatalf("the pre-eviction durable snapshot holds no state for acme, so this test would "+
			"assert nothing about an eviction: %s", rig.before)
	}
	rig.start(t)

	evicted, err := rig.evict(t, "acme")
	if err != nil {
		t.Fatalf("EvictTenant: %v", err)
	}
	if evicted == 0 {
		t.Fatal("the eviction reported removing nothing while the durable snapshot held the tenant")
	}

	after := rig.payload(t)
	if strings.Contains(after, "acme") {
		t.Fatalf("the durable snapshot still names the evicted tenant after a successful reply — "+
			"a restart would restore what the coordinator was told was erased: %s", after)
	}
	if !strings.Contains(after, "globex") {
		t.Fatalf("the eviction took the surviving tenant's durable state with it, which is a "+
			"silent outage for a tenant nobody deleted: %s", after)
	}
}

// TestAnEvictionThatCannotCommitIsReportedAsAnError is the HIGHEST-VALUE TEST IN THIS FILE.
//
// 🔴 WITHOUT IT the code is still green when applyTenantPurge ignores checkpoint()'s return
// value: the sweep runs, memory is clean, the reply says "evicted N", the coordinator writes
// the tenant off, and the very next restart restores every window, timer and arming from the
// checkpoint row that was never rewritten. Nothing errors, nothing logs an anomaly, and the
// deletion record — the artifact a regulator or a customer is shown — is false.
//
// This is also the reason checkpoint() returns a bool at all. Every other caller ignores it
// (a scheduled checkpoint that defers just retries next tick); this caller cannot, because
// its return value is a promise made to another service.
func TestAnEvictionThatCannotCommitIsReportedAsAnError(t *testing.T) {
	store, breakStore := newBreakableStore(t)
	rig := newEvictionRig(t, store, "acme", "globex")
	if !strings.Contains(rig.before, "acme") {
		t.Fatalf("vacuous setup: the durable snapshot holds no acme state: %s", rig.before)
	}
	// The engine still holds the tenant; only the ability to COMMIT is taken away.
	breakStore()
	rig.start(t)

	evicted, err := rig.evict(t, "acme")
	if err == nil {
		t.Fatalf("EvictTenant reported success (%d evicted) for an eviction whose checkpoint could "+
			"not commit; the coordinator would record an erasure a restart undoes", evicted)
	}
	// The count is still reported, and that is deliberate: the entries ARE gone from memory.
	// What the error says is that gone-from-memory is not the question that was asked.
	if evicted == 0 {
		t.Fatalf("a failed commit should still report what was removed in memory; got %d", evicted)
	}
	if !strings.Contains(err.Error(), "durable") {
		t.Fatalf("the error does not say the eviction is not durable, which is the one thing the "+
			"caller has to know: %v", err)
	}
}

// TestAStaleWriterRefusesToAnswerForThePartition covers the split-brain loser.
//
// A writer that Save has already refused (ErrStaleCheckpoint) is halted: another process
// owns this partition's checkpoint. Its engine is no longer the durable truth, and its
// checkpoint would be refused again, so it must not answer — an answer from it would be
// counted by the coordinator as one partition reporting clean while the partition that
// actually owns the state was never asked.
//
// 🔑 THE ASSERTION THAT MATTERS IS evicted == 0, not merely err != nil. Deleting the stale
// guard still produces an error (checkpoint() refuses a stale writer too), so an
// error-only assertion passes over the mutation. What changes is that the halted writer
// would have swept its engine first and reported a non-zero count alongside the error —
// mutating state it has no authority over, and handing the coordinator a number that
// describes a partition it does not own.
func TestAStaleWriterRefusesToAnswerForThePartition(t *testing.T) {
	rig := newEvictionRig(t, newTestStore(t), "acme", "globex")
	// Written before the loop goroutine starts, so this is not a race: start() is the
	// happens-before edge.
	rig.rp.stale = true
	rig.start(t)

	evicted, err := rig.evict(t, "acme")
	if err == nil {
		t.Fatal("a halted split-brain writer answered an eviction as though it owned the partition")
	}
	if evicted != 0 {
		t.Fatalf("a halted writer reported %d entries evicted; it must sweep nothing and claim "+
			"nothing for a partition another writer owns", evicted)
	}
	if !strings.Contains(err.Error(), "split-brain") {
		t.Fatalf("the refusal does not identify itself as the split-brain halt, so an operator "+
			"cannot tell it from a commit failure: %v", err)
	}
	if after := rig.payload(t); !strings.Contains(after, "acme") {
		t.Fatalf("a halted writer changed the durable checkpoint: %s", after)
	}
}

// TestEvictingNothingForcesNoCheckpointAndReportsNoError covers the steady state the purge
// coordinator's settle window is actually waiting to observe: pass after pass returning zero.
//
// 🔑 THE STORE IS BROKEN ON PURPOSE, and it is the whole instrument. "No checkpoint was
// forced" is invisible against a healthy store — a redundant Save of an identical payload
// leaves no trace anyone can assert on. Against a store that cannot commit, a forced
// checkpoint becomes an ERROR, so (0, nil) is proof the checkpoint was never attempted.
//
// Why it matters that it is not attempted: the settle window runs this call on every pass
// for as long as the deletion takes, against every partition. A no-op that checkpointed
// would rewrite the whole engine's snapshot on a schedule for tenants it holds nothing of,
// and — worse — would turn any unrelated store blip into a reported failure to erase a
// tenant whose state was never there.
func TestEvictingNothingForcesNoCheckpointAndReportsNoError(t *testing.T) {
	store, breakStore := newBreakableStore(t)
	rig := newEvictionRig(t, store, "acme", "globex")
	breakStore()
	rig.start(t)

	evicted, err := rig.evict(t, "never-held")
	if err != nil {
		t.Fatalf("evicting a tenant this engine holds nothing of forced a checkpoint (it errored "+
			"against a store that cannot commit): %v", err)
	}
	if evicted != 0 {
		t.Fatalf("evicting a tenant this engine holds nothing of reported %d entries", evicted)
	}
}

// TestEvictTenantValidatesTheTenantBeforeItReachesTheLoop.
//
// 🔴 THIS SIDE IS WHERE THE CONSEQUENCE LANDS. The caller (messaging.PurgeTenantDetect)
// validates too, and that check is not the one that protects anything here: this process
// holds the state, and the request arrives over a subject anything on the broker can reach.
// The engine matches state by the prefix "{tenant}/", so:
//
//   - ""        makes the prefix "/", which matches no minted id today but names no tenant
//     either — an eviction of nothing, reported to the coordinator as an erasure;
//   - "acme/x"  makes the prefix "acme/x/", reaching a deeper segment of ids belonging to a
//     tenant that was not named in the request;
//   - "*" / ">" are the broker's own wildcards, harmless as a Go prefix but exactly the
//     tokens that turn into a fleet-wide match anywhere a tenant is spliced into a subject.
//
// The refusal must happen BEFORE the request is handed to the loop, which is what this test
// measures: no loop is running, so nothing can receive the request. A validation that was
// dropped would leave EvictTenant blocked on the send until its context expires — the
// elapsed-time bound below fails on that, deterministically and without hanging the suite.
func TestEvictTenantValidatesTheTenantBeforeItReachesTheLoop(t *testing.T) {
	rig := newEvictionRig(t, newTestStore(t), "acme")
	// Deliberately NOT started: an unbuffered channel with no receiver is the instrument.

	for _, tenant := range []string{"", "acme/prof", "*", ">"} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		start := time.Now()
		evicted, err := rig.rp.EvictTenant(ctx, tenant)
		elapsed := time.Since(start)
		cancel()

		if err == nil {
			t.Fatalf("EvictTenant(%q) was accepted (evicted=%d); the engine matches state by the "+
				"%q prefix, so this evicts something other than the tenant it names", tenant, evicted, tenant+"/")
		}
		if evicted != 0 {
			t.Fatalf("EvictTenant(%q) reported %d entries evicted while refusing", tenant, evicted)
		}
		if !strings.Contains(err.Error(), "refusing to evict") {
			t.Fatalf("EvictTenant(%q) failed for some other reason than the refusal: %v", tenant, err)
		}
		if elapsed > 500*time.Millisecond {
			t.Fatalf("EvictTenant(%q) took %v with no loop running, so it was handed to the loop "+
				"before being validated — the check must refuse it in the caller's goroutine", tenant, elapsed)
		}
	}
}

// TestEvictionDropsTheTenantsBufferedDetectionsAndRetries covers the loop-owned buffers that
// are keyed on a tenant but are NOT engine state.
//
// The detections are the sharp one. A buffered detection is published to its own tenant's
// subject by the next checkpoint — the very checkpoint this eviction forces — so a retained
// one would emit an alarm for a tenant whose erasure has just been reported, into a subject
// tree the broker purge has already swept. The two retry sets are the tenant's device tokens
// sitting in this process's memory after a deletion record says they are gone.
//
// 🔴 THE ORACLE IS THE COUNT, AND THE PUBLISHED-EVENT LOG IS NOT AN ORACLE HERE. This is the
// healthy-neighbour trap: by the time the checkpoint publishes, applyTenantPurge has already
// swept the tenant out of the REGISTRY, so a retained detection is dropped as an orphan by
// the publisher's own Lookup and never reaches the writer. Asserting "no acme event was
// published" therefore passes with dropTenantBuffers deleted entirely — the registry sweep
// covers for it — while the tenant's rule id and device token sit in the buffer regardless.
// So the buffers are isolated instead: this rig registers NO rule and holds NO engine state
// for the victim, which makes the reported count exactly the number of buffer entries swept.
func TestEvictionDropsTheTenantsBufferedDetectionsAndRetries(t *testing.T) {
	rig := newEvictionRig(t, newTestStore(t), "globex")
	rp := rig.rp
	det := func(tenant, series string) detectcore.Detection {
		return detectcore.Detection{
			RuleID: tenant + "/prof@1/dead",
			Series: series,
			Kind:   detectcore.Absence,
			Edge:   detectcore.EdgeRaised,
			At:     testBase.Add(time.Minute),
		}
	}
	// Loop-owned state, written before start() publishes it to the loop goroutine.
	rp.pendingDets = []detectcore.Detection{det("acme", "d1"), det("acme", "d2"), det("globex", "d9")}
	rp.armRetries = map[armUpdate]struct{}{
		{tenant: "acme", deviceToken: "d1"}:   {},
		{tenant: "acme", deviceToken: "d2"}:   {},
		{tenant: "globex", deviceToken: "d1"}: {},
	}
	rp.attrRetries = map[attrUpdate]struct{}{
		{tenant: "acme", deviceToken: "d1"}:   {},
		{tenant: "globex", deviceToken: "d1"}: {},
	}
	rig.start(t)

	// 2 buffered detections + 2 arming rechecks + 1 attribute recheck, and nothing else:
	// the victim owns no rule and no engine state in this rig.
	const want = 5
	evicted, err := rig.evict(t, "acme")
	if err != nil {
		t.Fatalf("EvictTenant: %v", err)
	}
	if evicted != want {
		t.Fatalf("eviction reported %d entries, want %d — the victim holds no rules and no engine "+
			"state here, so this count is exactly the loop-owned buffers that were swept", evicted, want)
	}

	rig.stop() // join the loop before reading its fields

	if len(rp.armRetries) != 1 {
		t.Fatalf("armRetries = %v, want exactly the surviving tenant's entry", rp.armRetries)
	}
	if _, ok := rp.armRetries[armUpdate{tenant: "globex", deviceToken: "d1"}]; !ok {
		t.Fatalf("the eviction swept the surviving tenant's arming recheck: %v", rp.armRetries)
	}
	if len(rp.attrRetries) != 1 {
		t.Fatalf("attrRetries = %v, want exactly the surviving tenant's entry", rp.attrRetries)
	}
	if _, ok := rp.attrRetries[attrUpdate{tenant: "globex", deviceToken: "d1"}]; !ok {
		t.Fatalf("the eviction swept the surviving tenant's attribute recheck: %v", rp.attrRetries)
	}

	// Secondary (not the oracle, see above): the surviving tenant's buffered detection was
	// still delivered by the forced checkpoint, so the eviction did not swallow it.
	got := rig.writer.recorded()
	if len(got) != 1 || !strings.Contains(got[0], "globex") {
		t.Fatalf("the forced checkpoint should have published the surviving tenant's buffered "+
			"detection and nothing else; got %v", got)
	}
}

// TestTheConstructorWiresTheEvictionChannel closes the one gap the rig above opens.
//
// Every other test in this file supplies tenantPurges itself, so none of them would notice
// if NewResolvedEventsProcessor stopped creating it. That omission has no compile-time
// signal and no runtime error: a nil channel is never closed, never full and never ready, so
// the loop's select arm simply never fires and EvictTenant blocks until the caller's context
// expires. The responder would then answer every eviction request with a timeout error, on
// an engine that is running perfectly — and the purge coordinator would record a partition
// that "did not answer" forever.
//
// This is the ONLY test that builds the processor the way production does, which is why it
// takes the cost of a real Microservice (its metric constructors register into the default
// Prometheus registerer, so exactly one test in the package may do this).
func TestTheConstructorWiresTheEvictionChannel(t *testing.T) {
	ms := &core.Microservice{FunctionalArea: "detecttenantpurgetest"}
	rp := NewResolvedEventsProcessor(ms, &fakeReader{}, &fakeReplayOpener{}, newTestStore(t),
		nil, &recordingWriter{}, nil, Config{
			PartitionId:        "singleton",
			CheckpointEvents:   1000,
			CheckpointInterval: time.Hour,
			TickInterval:       time.Hour,
		}, core.NewNoOpLifecycleCallbacks())

	ctx := context.Background()
	if err := rp.ExecuteInitialize(ctx); err != nil {
		t.Fatalf("ExecuteInitialize: %v", err)
	}
	if err := rp.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rp.readerWG.Add(1)
	go rp.run()
	t.Cleanup(func() { rp.procCancel(); rp.readerWG.Wait() })

	// The engine holds nothing, so the interesting part is not the result but that a result
	// ARRIVES: the request reached the loop and the loop answered.
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	evicted, err := rp.EvictTenant(callCtx, "acme")
	if err != nil {
		t.Fatalf("a constructor-built processor did not answer an eviction: %v", err)
	}
	if evicted != 0 {
		t.Fatalf("an empty engine reported %d entries evicted", evicted)
	}
}

// --- The NATS responder -----------------------------------------------------------------

// purgeBroker starts a real embedded nats-server. Core NATS only — the eviction is a
// request/reply control message and deliberately not a stream (a durable fact carrying it
// would be deleted by the very purge it was driving).
//
// It is a real broker rather than a fake connection because everything worth testing about
// the responder lives in the server's behaviour: subject matching, whether a request reaches
// one subscriber or all of them, and what happens to a request with no reply subject.
func purgeBroker(t *testing.T) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:       "127.0.0.1",
		Port:       -1,
		ServerName: "detect-purge-processor-test",
	})
	if err != nil {
		t.Fatalf("start test broker: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("the test broker never became ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv
}

func purgeConn(t *testing.T, srv *natsserver.Server) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect to test broker: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// askForEviction publishes a real DetectPurgeRequest and gathers every reply that arrives
// within the window. It uses a private inbox with a SYNC subscription rather than
// nc.Request, for the same reason PurgeTenantDetect does: Request returns the first reply
// and unsubscribes, which would make the two-responder test below unable to fail.
func askForEviction(t *testing.T, nc *nats.Conn, tenant string, want int) []messaging.DetectPurgeReply {
	t.Helper()
	payload, err := json.Marshal(messaging.DetectPurgeRequest{Tenant: tenant})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	inbox := nats.NewInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		t.Fatalf("subscribe to reply inbox: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush inbox subscription: %v", err)
	}
	if err := nc.PublishRequest(messaging.DetectPurgeSubject(evictInstance), inbox, payload); err != nil {
		t.Fatalf("publish eviction request: %v", err)
	}
	var out []messaging.DetectPurgeReply
	deadline := time.Now().Add(10 * time.Second)
	for len(out) < want && time.Now().Before(deadline) {
		msg, err := sub.NextMsg(time.Until(deadline))
		if err != nil {
			break
		}
		var reply messaging.DetectPurgeReply
		if err := json.Unmarshal(msg.Data, &reply); err != nil {
			t.Fatalf("decode reply: %v", err)
		}
		out = append(out, reply)
	}
	return out
}

// TestResponderRoundTripsAnEvictionOverTheBroker is the whole seam end to end: a request on
// the wire, through the responder, onto the single-writer loop, into the engine, out to the
// snapshot store, and back as a reply — over a real broker and a real sqlite store.
//
// The PartitionId assertion is not a formality. With several partitions running, the caller
// distinguishes a complete answer from a partial one ONLY by who signed each reply; a reply
// with an empty or wrong partition id makes one engine's answer indistinguishable from
// another's, and the coordinator would count a partition as having answered when it had not.
func TestResponderRoundTripsAnEvictionOverTheBroker(t *testing.T) {
	srv := purgeBroker(t)
	nc := purgeConn(t, srv)
	rig := newEvictionRig(t, newTestStore(t), "acme", "globex")
	if !strings.Contains(rig.before, "acme") {
		t.Fatalf("vacuous setup: no acme state in the durable snapshot: %s", rig.before)
	}
	rig.start(t)

	r := NewTenantPurgeResponder(nc, evictInstance, rig.rp)
	if err := r.Start(); err != nil {
		t.Fatalf("responder Start: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop() })

	replies := askForEviction(t, nc, "acme", 1)
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1 — silence and \"I hold nothing\" are the same observation "+
			"to a caller that only sees a timeout, and they are opposite facts", len(replies))
	}
	got := replies[0]
	if got.PartitionId != "singleton" {
		t.Fatalf("reply partition = %q, want %q; an unsigned reply cannot be matched to the "+
			"partition that owes an answer", got.PartitionId, "singleton")
	}
	if got.Error != "" {
		t.Fatalf("reply carries an error: %s", got.Error)
	}
	if got.Evicted == 0 {
		t.Fatal("reply reports nothing evicted while the durable snapshot held the tenant")
	}
	if after := rig.payload(t); strings.Contains(after, "acme") {
		t.Fatalf("the reply reported an eviction the durable snapshot did not receive: %s", after)
	}
}

// TestResponderRepliesWithTheErrorRatherThanSilence.
//
// 🔴 A FAILED EVICTION MUST COME BACK AS A REPLY CARRYING THE ERROR. The two wrong answers
// are opposite and both fatal: replying clean records a partition as erased when it still
// holds the tenant, and NOT replying at all is read by the caller as a timeout, which — with
// the settle window's own tolerance for a slow partition — can be waited out and eventually
// treated as a partition that had nothing to say. The error field exists so a partition that
// could not commit is recorded as still holding the tenant.
func TestResponderRepliesWithTheErrorRatherThanSilence(t *testing.T) {
	srv := purgeBroker(t)
	nc := purgeConn(t, srv)
	store, breakStore := newBreakableStore(t)
	rig := newEvictionRig(t, store, "acme", "globex")
	breakStore()
	rig.start(t)

	r := NewTenantPurgeResponder(nc, evictInstance, rig.rp)
	if err := r.Start(); err != nil {
		t.Fatalf("responder Start: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop() })

	replies := askForEviction(t, nc, "acme", 1)
	if len(replies) != 1 {
		t.Fatal("a partition that could not complete the eviction answered with SILENCE, which the " +
			"caller cannot tell from a slow partition or from one holding nothing")
	}
	if replies[0].Error == "" {
		t.Fatalf("a failed eviction came back as a clean answer (evicted=%d); the coordinator would "+
			"record the tenant as erased from an engine that still holds it", replies[0].Evicted)
	}
	if replies[0].PartitionId != "singleton" {
		t.Fatalf("the failure reply is unsigned (partition %q), so the caller cannot say WHICH "+
			"partition still holds the tenant", replies[0].PartitionId)
	}
}

// TestResponderIgnoresARequestWithNoReplySubject.
//
// A fire-and-forget publish to the eviction subject carries no reply subject. Anything with
// access to the broker can send one, so this is reachable without a bug anywhere, and the
// responder must both survive it and DO NOTHING about it.
//
// 🔑 "SURVIVE IT" ALONE IS NOT AN ORACLE — that was this test's first draft and it was
// worthless. Deleting the reply-subject guard leaves the responder perfectly healthy: it
// evicts the tenant, then fails to publish the answer to the empty subject and logs it. The
// suite stayed green. What is actually wrong in that world is not the failed publish, it is
// that A TENANT WAS EVICTED FOR A REQUEST NOBODY IS LISTENING TO — a real, checkpointed
// erasure whose result is discarded, so the coordinator never learns it happened and the
// count it eventually gathers (zero, from the idempotent second pass) is indistinguishable
// from an engine that never held the tenant.
//
// So the oracle is the number of evictions performed for two requests, one of them
// reply-less. Proving a negative needs a bounded settle window, which is what the loop below
// is: the handler runs on its own goroutine, so the reply to the SECOND request does not by
// itself prove the first goroutine has finished not-evicting.
func TestResponderIgnoresARequestWithNoReplySubject(t *testing.T) {
	srv := purgeBroker(t)
	nc := purgeConn(t, srv)

	// A stubbed eviction, because what is being measured is whether the handler CALLS it —
	// not what it does. Driving a real engine here would make the count depend on the sweep.
	var evictions atomic.Int64
	r := &TenantPurgeResponder{
		conn: nc, instanceId: evictInstance, partition: "singleton",
		evict: func(context.Context, string) (int64, error) { evictions.Add(1); return 1, nil },
	}
	if err := r.Start(); err != nil {
		t.Fatalf("responder Start: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop() })
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush responder subscription: %v", err)
	}

	payload, err := json.Marshal(messaging.DetectPurgeRequest{Tenant: "acme"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := nc.Publish(messaging.DetectPurgeSubject(evictInstance), payload); err != nil {
		t.Fatalf("publish reply-less request: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// The responder must still be serving: a handler that panicked or a subscription that
	// died would fail here.
	replies := askForEviction(t, nc, "acme", 1)
	if len(replies) != 1 {
		t.Fatal("the responder stopped answering after a request with no reply subject")
	}

	// Settle: give a second eviction time to show up before concluding it never happened.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && evictions.Load() <= 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := evictions.Load(); got != 1 {
		t.Fatalf("%d evictions ran for two requests, one of which carried no reply subject: the "+
			"reply-less one erased a tenant whose result nobody could receive, so the erasure "+
			"happened and was never recorded", got)
	}
}

// TestEveryResponderAnswersTheSameRequest pins the PLAIN Subscribe.
//
// 🔴 A QUEUE GROUP HERE WOULD BE A SILENT, COMPLETE-LOOKING LIE. Queue delivery hands each
// request to exactly ONE member, so on a sharded fleet a single partition would evict, reply
// clean, and the caller — which has no count on the wire and stops gathering once the
// partitions it knows about have answered — would see a well-formed reply and record the
// tenant as erased from an engine that still holds it in every other partition. The failure
// is invisible precisely because the reply it did get looked fine.
//
// The two responders are given the real Start(), so the subscribe call under test is the
// production one; only the eviction they perform is stubbed, because what is being measured
// is DELIVERY, not the sweep.
func TestEveryResponderAnswersTheSameRequest(t *testing.T) {
	srv := purgeBroker(t)
	// Separate connections, as separate pods would be: a queue group spanning connections is
	// exactly the production shape, so this cannot pass by accident on a single-connection
	// quirk.
	nc1, nc2, asker := purgeConn(t, srv), purgeConn(t, srv), purgeConn(t, srv)

	stub := func(_ context.Context, _ string) (int64, error) { return 1, nil }
	for _, spec := range []struct {
		conn      *nats.Conn
		partition string
	}{{nc1, "part-a"}, {nc2, "part-b"}} {
		r := &TenantPurgeResponder{conn: spec.conn, instanceId: evictInstance, partition: spec.partition, evict: stub}
		if err := r.Start(); err != nil {
			t.Fatalf("responder %s Start: %v", spec.partition, err)
		}
		t.Cleanup(func() { _ = r.Stop() })
		if err := spec.conn.Flush(); err != nil {
			t.Fatalf("flush responder %s subscription: %v", spec.partition, err)
		}
	}

	replies := askForEviction(t, asker, "acme", 2)
	seen := map[string]bool{}
	for _, rep := range replies {
		seen[rep.PartitionId] = true
	}
	if !seen["part-a"] || !seen["part-b"] {
		t.Fatalf("only %v answered; every partition must answer for ITSELF — a queue group would "+
			"deliver to one, evict one engine, and hand the caller a complete-looking reply while "+
			"the rest of the fleet still holds the tenant", replies)
	}
}

// TestEvictionSweepsTheTenantsDeadLetters covers the one thing the eviction touches that
// the single-writer loop does not own: the publisher's dead-letter ring.
//
// It is a bounded diagnostic, in memory only, populated solely by the tenant backstop
// refusing a detection — a path that should not fire at all in correct operation. None of
// that changes what the entries ARE: the purged tenant's rule id, device token and tenant
// name, readable by any operator through DeadLetters() after the deletion record has said
// they are gone. It was also the last piece to be wired, and nothing else in this package
// would notice its absence — the ring is off the checkpoint path entirely, so no snapshot
// assertion anywhere can see it.
//
// The ring is populated by driving the REAL backstop (a detection for a rule the registry
// does not hold is dead-lettered as an orphan) rather than by writing the slice, so a
// change to what the backstop records is a failure here rather than a test that keeps
// passing over a ring nothing fills any more.
func TestEvictionSweepsTheTenantsDeadLetters(t *testing.T) {
	rig := newEvictionRig(t, newTestStore(t), "globex")
	rp := rig.rp

	for _, id := range []string{"acme/prof@1/orphan", "acme/prof@1/orphan2", "globex/prof@1/orphan"} {
		if err := rp.publisher.Publish(context.Background(), detectcore.Detection{
			RuleID: id, Series: "d1", Kind: detectcore.Threshold,
			Edge: detectcore.EdgeRaised, At: testBase,
		}); err != nil {
			t.Fatalf("publishing an orphan detection for %q: %v", id, err)
		}
	}
	if got := len(rp.publisher.DeadLetters()); got != 3 {
		t.Fatalf("test bug: the backstop recorded %d dead letters, want 3 — nothing is being "+
			"swept below and the assertions would be vacuous", got)
	}

	rig.start(t)
	evicted, err := rig.evict(t, "acme")
	if err != nil {
		t.Fatalf("EvictTenant: %v", err)
	}
	// The victim holds no rules and no engine state in this rig, so the count IS the ring.
	if evicted != 2 {
		t.Fatalf("eviction reported %d entries, want 2 — the victim's only state here is its "+
			"two dead letters", evicted)
	}
	rig.stop()

	remaining := rp.publisher.DeadLetters()
	if len(remaining) != 1 {
		t.Fatalf("dead-letter ring = %+v, want only the surviving tenant's entry", remaining)
	}
	if !strings.HasPrefix(remaining[0].RuleID, "globex/") {
		t.Fatalf("the eviction swept the surviving tenant's dead letter and kept the victim's: %+v",
			remaining)
	}
}
