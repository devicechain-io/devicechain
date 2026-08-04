// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// The DETECT store, driven over a REAL BROKER against a REAL CATALOG QUERY.
//
// 🔴 EVERY INTERESTING ANSWER THIS STORE GIVES IS AN ANSWER ABOUT SOMETHING THAT DID NOT
// HAPPEN — nobody replied, this partition did not reply, the area was never deployed — and
// each of those is one absence away from its neighbour. "No engine is running" and "no
// engine was ever installed here" are the same silence on the wire and mean opposite things
// for a purge; "answered" and "erased" look identical to a store that only counts replies.
// So the two sides that produce the silence are real: a real nats-server decides whether a
// request has a subscriber (and answers 503 when it does not), and a real SQL statement —
// the store's own, verbatim, table name and quoting included — decides which partitions owe
// an answer. A fake on either side is a fake that agrees with whatever this package believes
// about the shapes, which is precisely the belief under test.
//
// What is NOT decided here: whether the store names the right catalog and the right table in
// a real Postgres. SQLite is coaxed into answering both queries, so a rename of
// detect_snapshots or its partition_id column would pass this file. That is pinned against
// the real migrated schema by the purge drill, which is where a name can actually be checked.

const (
	detectInstance = "inst-1"
	// A token with more than one segment, so a store that sent a hardcoded tenant — or the
	// empty one, which widens the engine's prefix match to every tenant — would be caught by
	// the responder rather than pass unnoticed.
	detectTenant = "acme-corp"
)

// detectRig starts a plain broker — no JetStream, because the eviction is core NATS
// request/reply — and returns its client URL. Port -1 lets the server pick a free one, the
// same way core/messaging's own detect rig does.
func detectRig(t *testing.T) string {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:       "127.0.0.1",
		Port:       -1,
		ServerName: "detect-store-test",
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "the test broker never became ready")
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

// detectDial opens one client connection to the rig. The store and the stand-in engines get
// their own, as they would in production, so nothing in this file can pass because two
// participants happen to share a connection's ordering or its subscription list.
func detectDial(t *testing.T, url string) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// detectConn hands the store a broker connection, which is the whole of messaging.Connection.
// A nil one is how an instance whose broker connection has not been established looks.
type detectConn struct{ nc *nats.Conn }

func (c detectConn) Conn() *nats.Conn { return c.nc }

// detectEngine is a stand-in for a running DETECT engine. It subscribes to the REAL eviction
// subject — derived through messaging.DetectPurgeSubject, never spelled out here, so a test
// cannot pass by agreeing with a subject the store does not use — and answers with a real
// DetectPurgeReply encoded the way the wire contract encodes it.
//
// It records what it was asked, because several tests here assert on a request that must NOT
// have been sent. A store that decides not to ask is indistinguishable from one that asked
// and got a helpful answer, unless the responder is counting.
type detectEngine struct {
	mu   sync.Mutex
	seen []messaging.DetectPurgeRequest
}

func (e *detectEngine) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.seen)
}

func (e *detectEngine) tenants() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.seen))
	for _, r := range e.seen {
		out = append(out, r.Tenant)
	}
	return out
}

// runEngine subscribes a stand-in engine that answers each request with whatever `answer`
// returns for that pass (1-based), so a test can model an engine that evicts something the
// first time and nothing after.
//
// Returning several replies models one process hosting several partitions. They go out in
// order on one connection, which NATS preserves — the property the unexpected-partition test
// leans on to be deterministic rather than a race between two responders.
func runEngine(t *testing.T, nc *nats.Conn,
	answer func(pass int) []messaging.DetectPurgeReply) *detectEngine {
	t.Helper()
	e := &detectEngine{}
	sub, err := nc.Subscribe(messaging.DetectPurgeSubject(detectInstance), func(msg *nats.Msg) {
		var req messaging.DetectPurgeRequest
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			// t.Fatal is not callable from this goroutine, and swallowing would turn a
			// broken payload into a silent timeout — the exact failure this file exists to
			// tell apart from a real one.
			t.Errorf("the eviction request did not decode as a DetectPurgeRequest: %v", err)
			return
		}
		e.mu.Lock()
		e.seen = append(e.seen, req)
		pass := len(e.seen)
		e.mu.Unlock()
		for _, r := range answer(pass) {
			body, err := json.Marshal(r)
			if err != nil {
				t.Errorf("encoding a DetectPurgeReply: %v", err)
				return
			}
			if err := msg.Respond(body); err != nil {
				t.Errorf("replying to the eviction request: %v", err)
				return
			}
		}
	})
	require.NoError(t, err)
	// Flush so the subscription is registered at the server before any test publishes:
	// otherwise a request can reach the server first and be answered 503, which would make
	// "an engine is running" and "none is" the same test.
	require.NoError(t, nc.Flush())
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return e
}

// answersWith is the ordinary engine: the same replies on every pass.
func answersWith(replies ...messaging.DetectPurgeReply) func(int) []messaging.DetectPurgeReply {
	return func(int) []messaging.DetectPurgeReply { return replies }
}

// detectDB is the database side, narrowed to the one method the store uses.
type detectDB struct{ db *gorm.DB }

func (d detectDB) DB(context.Context) *gorm.DB { return d.db }

// detectCatalog builds a database that answers BOTH of the store's questions with SQLite,
// so the store's own statements run unchanged.
//
//   - "has event-processing ever run here?" is tenantpurge.SchemaExists, which counts rows in
//     pg_namespace. A stand-in table of that name is enough; whether pg_namespace is the right
//     catalog is a question only real Postgres can answer, and the purge drill asks it there.
//   - "which partitions hold a checkpoint?" is a literal, quoted, schema-qualified SELECT.
//     🔑 SQLite spells a schema as an ATTACHed database, so attaching one named
//     "event-processing" makes the store's query — every character of it, including the
//     table and column names — the query this test actually executes. Rewriting it here
//     would leave the real one unexercised.
//
// deployed=false means the area has never run on this instance, so the schema is absent AND
// so is the table. That is the real shape: a store that skipped the catalog check would not
// merely read an empty table, it would fail on a table that does not exist.
func detectCatalog(t *testing.T, deployed bool, partitions ...string) Database {
	t.Helper()
	require.True(t, deployed || len(partitions) == 0,
		"an instance with no event-processing schema cannot have checkpoint rows")

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// 🔴 ONE CONNECTION, DELIBERATELY. ATTACH is per-connection in SQLite and ":memory:"
	// gives every new connection its own empty database, so a pool of two would serve the
	// store a handle where the attached schema does not exist — a test failure that looks
	// exactly like the defect being tested for.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	require.NoError(t, db.Exec(`CREATE TABLE pg_namespace (nspname text)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO pg_namespace (nspname) VALUES ('public')`).Error)
	if !deployed {
		return detectDB{db}
	}
	require.NoError(t, db.Exec(`INSERT INTO pg_namespace (nspname) VALUES (?)`, detectSchema).Error)
	require.NoError(t, db.Exec(`ATTACH DATABASE ':memory:' AS "`+detectSchema+`"`).Error)
	require.NoError(t, db.Exec(
		`CREATE TABLE "`+detectSchema+`"."detect_snapshots" (partition_id text)`).Error)
	for _, p := range partitions {
		require.NoError(t, db.Exec(
			`INSERT INTO "`+detectSchema+`"."detect_snapshots" (partition_id) VALUES (?)`, p).Error)
	}
	return detectDB{db}
}

// detectStore builds the store the way DefaultStores does.
func detectStore(nc *nats.Conn, db Database) *Detect {
	return NewDetect(detectConn{nc}, db, detectInstance)
}

// shortGather bounds the gather from the CALLER's side, which is where the store passes its
// context straight through to.
//
// PurgeTenantDetect waits DetectPurgeWindow (5s) for a partition that is running and does not
// answer, and a test that needs that case cannot make the wait shorter — the constant is the
// contract. It can, however, supply a deadline of its own, which is what core/messaging's own
// silent-partition test does. Used here only where the property under test is about COUNTING
// rather than about how long the wait is; the multi-partition test below sits out the real
// window on purpose, and says why.
const shortGather = time.Second

// TestAnInstanceThatNeverRanEventProcessingIsCleanRatherThanDeferred is the branch that lets
// a purge finish on a deployment that has no DETECT engine at all.
//
// 🔴 THIS IS NOT A HYPOTHETICAL PROFILE. An ingest-only instance runs no event-processing,
// so nothing ever subscribes to the eviction subject — and "no subscriber" is the SAME
// observation as an engine that is down, which must block. Read the wrong way, every purge
// on such an instance defers forever: the deletion record never completes, the token is
// never released, and there is no engine anyone could start to clear it.
//
// The control is the engine that IS listening and would answer with an error. If the store
// asked in spite of the absent schema, that reply would defer the purge — so this test fails
// on a store that skips the catalog check, rather than passing because the outcome happened
// to be empty for a different reason.
func TestAnInstanceThatNeverRanEventProcessingIsCleanRatherThanDeferred(t *testing.T) {
	url := detectRig(t)
	engine := runEngine(t, detectDial(t, url), answersWith(messaging.DetectPurgeReply{
		PartitionId: "p1", Evicted: 9, Error: "this engine must never have been asked",
	}))

	out, err := detectStore(detectDial(t, url), detectCatalog(t, false)).
		Erase(context.Background(), detectTenant, time.Now())

	require.NoError(t, err, "an area that was never deployed here is an answer, not a retry")
	assert.True(t, out.Clean(), "there is no DETECT engine on this instance and never has been, "+
		"so nothing here holds the tenant — a store that stayed dirty would block every purge "+
		"on an ingest-only instance forever, with no engine anyone could start to clear it")
	assert.Zero(t, out.Rows, "nothing was evicted and the ledger must not imply otherwise")
	assert.Zerof(t, engine.calls(), "the store asked an engine to evict a tenant on an instance "+
		"whose catalog says event-processing has never run here; the request was answered %d "+
		"time(s), so this store's 'nothing here' rests on who happens to be listening", engine.calls())
}

// TestACheckpointedPartitionWithNoEngineRunningIsDeferredAndNamed is the case that MUST block
// completion, and it is the other half of the test above.
//
// The row in detect_snapshots is durable evidence: that partition checkpointed this
// instance's engine state, the tenant's open windows and timers are inside that blob, and no
// SQL predicate reaches into it. Nobody is running to evict them. Reporting clean here writes
// a deletion record claiming an erasure that did not happen.
//
// The sentence has to NAME the partition, because the operator action is per-partition —
// "some engine did not answer" sends someone to look at a fleet, "p-alpha did not answer"
// sends them to a shard.
//
// 🔑 It returns fast, and that is a property rather than a convenience: with nothing
// subscribed the broker itself answers 503, so this case never waits out the gather window.
// A store that fell through to the timeout instead would cost the coordinator five seconds on
// every pass for every tenant, on precisely the instances where an engine is down.
func TestACheckpointedPartitionWithNoEngineRunningIsDeferredAndNamed(t *testing.T) {
	url := detectRig(t) // no responder: this instance's engine is not running

	start := time.Now()
	out, err := detectStore(detectDial(t, url), detectCatalog(t, true, "p-alpha")).
		Erase(context.Background(), detectTenant, time.Now())
	elapsed := time.Since(start)

	require.NoError(t, err, "an engine that is down is data for the ledger, not a failed call")
	assert.False(t, out.Clean(), "a partition holding a checkpoint did not answer, so this "+
		"tenant's detection windows and timers are still on disk — completing here would claim "+
		"an erasure that never happened")
	assert.Contains(t, out.Reason(), `"p-alpha"`,
		"the deferral must name the partition whose checkpoint still holds the tenant; without "+
			"it an operator is told to inspect a fleet rather than a shard")
	assert.Zero(t, out.Rows)
	assert.Lessf(t, elapsed, messaging.DetectPurgeWindow/2, "the pass took %s, so it waited out "+
		"the gather window rather than reading the broker's no-subscriber answer — that cost is "+
		"paid on every pass for every tenant while an engine is down", elapsed)
}

// TestAnAnsweringPartitionIsCleanAndItsEvictionsReachTheLedger is the ordinary success, and
// it is the control that keeps the deferral tests above from being satisfied by a store that
// simply never reports clean.
//
// The count matters as much as the cleanliness: Rows is what the coordinator reads as "this
// pass still found work", and it restarts the settle window. A store that dropped Evicted
// would let a purge settle over a pass that had just evicted live state.
//
// 🔑 IT ALSO ASSERTS HOW LONG THE PASS TOOK, and that assertion was added because its absence
// was measured: a store that gathered without handing over the set of partitions it expects
// still reaches every conclusion in this file correctly — and pays the full five-second
// window on EVERY pass for EVERY tenant, on the coordinator's single goroutine, while holding
// the advisory lock. Nothing fails, nothing is logged, and the queue just gets slower. The
// expected set is what lets the gather stop the moment everyone it is owed has answered.
func TestAnAnsweringPartitionIsCleanAndItsEvictionsReachTheLedger(t *testing.T) {
	url := detectRig(t)
	engine := runEngine(t, detectDial(t, url),
		answersWith(messaging.DetectPurgeReply{PartitionId: "p-alpha", Evicted: 4}))

	start := time.Now()
	out, err := detectStore(detectDial(t, url), detectCatalog(t, true, "p-alpha")).
		Erase(context.Background(), detectTenant, time.Now())
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Lessf(t, elapsed, messaging.DetectPurgeWindow/2, "the pass took %s with every "+
		"checkpointed partition answering, so the gather ran to its window instead of ending "+
		"when the last expected reply arrived — that is paid per tenant, per pass, forever",
		elapsed)
	assert.Truef(t, out.Clean(), "every partition holding a checkpoint answered, so nothing is "+
		"retained here: %s", out.Reason())
	assert.Equal(t, int64(4), out.Rows, "the eviction count is what tells the coordinator this "+
		"pass found work; losing it lets the settle window run over a pass that erased live state")
	require.Equal(t, 1, engine.calls())
	assert.Equal(t, []string{detectTenant}, engine.tenants(),
		"the engine evicts by tenant prefix, so the token on the wire is the whole of what it acts on")
}

// TestAPartitionThatAnswersWithAnErrorIsStillHoldingTheTenant separates ANSWERING from
// ERASING, which is the one distinction a reply-counting store cannot make.
//
// The responder replies only after its checkpoint commits — that is what makes a reply
// evidence at all — so a reply carrying an error is the engine saying the opposite: it
// evicted in memory and could not make it durable, or it is a halted writer. The state is
// still in the checkpoint on disk. A store that treated the reply as coverage would complete
// the purge, release the token, and leave the tenant's windows and timers behind.
func TestAPartitionThatAnswersWithAnErrorIsStillHoldingTheTenant(t *testing.T) {
	url := detectRig(t)
	runEngine(t, detectDial(t, url), answersWith(messaging.DetectPurgeReply{
		PartitionId: "p-alpha", Evicted: 2, Error: "committing the checkpoint: no space left on device",
	}))

	out, err := detectStore(detectDial(t, url), detectCatalog(t, true, "p-alpha")).
		Erase(context.Background(), detectTenant, time.Now())

	require.NoError(t, err, "a partition reporting a failed eviction is a ledger line, not a retry")
	assert.False(t, out.Clean(), "the partition answered but did not erase; counting the reply as "+
		"coverage completes a purge over state that is still in the checkpoint")
	assert.Contains(t, out.Reason(), `"p-alpha"`, "the deferral must name the partition")
	assert.Contains(t, out.Reason(), "no space left on device",
		"the engine's own reason is the only thing an operator can act on; a deferral that drops "+
			"it says a partition failed without saying how")
	assert.Equal(t, int64(2), out.Rows, "what it did evict is still work this pass found, and the "+
		"settle window must restart for it — the deferral, not the count, is what blocks")
}

// TestASecondPassEvictsNothingAndLetsTheSettleWindowRun is the idempotency this store is
// called on every pass to satisfy, and the assertion that matters is the SECOND one.
//
// 🔴 ROWS IS NOT COSMETIC. The coordinator restarts its settle window whenever a store
// reports rows, because rows arriving is what "not settled yet" means. A store that reported
// the same eviction count on every pass would therefore report CLEAN forever and never
// complete — the most reassuring line the coordinator has, written by a purge that can never
// finish. The object store shipped exactly that livelock, which is why this test exists here
// before anyone has seen it happen with an engine.
func TestASecondPassEvictsNothingAndLetsTheSettleWindowRun(t *testing.T) {
	url := detectRig(t)
	// A real engine: the first eviction removes the tenant's state, and there is nothing
	// left to remove afterwards.
	engine := runEngine(t, detectDial(t, url), func(pass int) []messaging.DetectPurgeReply {
		if pass == 1 {
			return []messaging.DetectPurgeReply{{PartitionId: "p-alpha", Evicted: 3}}
		}
		return []messaging.DetectPurgeReply{{PartitionId: "p-alpha", Evicted: 0}}
	})
	store := detectStore(detectDial(t, url), detectCatalog(t, true, "p-alpha"))

	first, err := store.Erase(context.Background(), detectTenant, time.Now())
	require.NoError(t, err)
	require.Equal(t, int64(3), first.Rows, "the first pass evicts the tenant's engine state")
	require.True(t, first.Clean())

	second, err := store.Erase(context.Background(), detectTenant, time.Now())

	require.NoError(t, err, "a second eviction of a tenant that is already gone must succeed")
	assert.True(t, second.Clean())
	assert.Zerof(t, second.Rows, "the second pass reported %d evicted over an engine that holds "+
		"nothing. The coordinator reads that as the store still receiving work and restarts the "+
		"settle window, so this tenant's purge can never complete and its token is never released",
		second.Rows)
	assert.Equal(t, 2, engine.calls(), "the engine is asked on every pass — the store must not "+
		"cache a clean answer, since state can arrive between passes")
}

// TestAnEngineThatHasNeverCheckpointedIsCountedRatherThanDiscarded covers the partition the
// database cannot name.
//
// detect_snapshots is written when a partition checkpoints, so an engine that started, took
// live state and has not checkpointed yet holds tenant data that NO ROW NAMES. It is outside
// the expected set by construction. Discarding its reply loses the eviction count for state
// that really was removed, and with it the settle-window restart — the purge would be free to
// complete on the very pass that found live state.
//
// 🔑 It is the empty expected set that makes this test wait: with no partition to tick off,
// the gather has no way to know it is done and runs to its deadline. The deadline here is the
// caller's rather than the 5s window, because the property is about the reply being COUNTED,
// not about how long a gather waits — the test below pays that cost once, on purpose.
func TestAnEngineThatHasNeverCheckpointedIsCountedRatherThanDiscarded(t *testing.T) {
	url := detectRig(t)
	runEngine(t, detectDial(t, url),
		answersWith(messaging.DetectPurgeReply{PartitionId: "never-checkpointed", Evicted: 5}))

	ctx, cancel := context.WithTimeout(context.Background(), shortGather)
	defer cancel()
	out, err := detectStore(detectDial(t, url), detectCatalog(t, true)).
		Erase(ctx, detectTenant, time.Now())

	require.NoError(t, err)
	assert.Truef(t, out.Clean(), "the only engine on the instance answered; nothing is retained: %s",
		out.Reason())
	assert.Equal(t, int64(5), out.Rows, "an engine outside the expected set still evicted five "+
		"entries of this tenant's live state; dropping the count lets the settle window run over "+
		"the pass that found it")
}

// TestOnlyTheSilentPartitionIsNamed is the multi-partition property, and the negative half of
// it is the point.
//
// GA runs one DETECT partition, so every test above passes on a store that ignores partition
// identity entirely. This one does not: one engine answers, one does not, and the ledger must
// name the second WITHOUT naming the first. A sweep that listed both would send an operator
// to restart a healthy engine and to go looking for state on a shard that has none — and a
// sweep that named neither would complete the purge over the shard that is still holding the
// tenant's windows.
//
// 🔑 THIS TEST WAITS OUT THE FULL GATHER WINDOW (5s) AND THAT IS THE POINT OF IT. The context
// is the coordinator's own — no deadline — so what ends the wait is DetectPurgeWindow inside
// PurgeTenantDetect, exactly as in production. It is the one test here that pays that cost:
// it is the only one whose subject is what happens when a running fleet answers PARTIALLY,
// where an early return would prove nothing about a store that gave up at the first reply.
func TestOnlyTheSilentPartitionIsNamed(t *testing.T) {
	url := detectRig(t)
	// One process, answering for the one partition it hosts. p-beta has a checkpoint row and
	// nothing running to answer for it.
	runEngine(t, detectDial(t, url),
		answersWith(messaging.DetectPurgeReply{PartitionId: "p-alpha", Evicted: 2}))

	out, err := detectStore(detectDial(t, url), detectCatalog(t, true, "p-alpha", "p-beta")).
		Erase(context.Background(), detectTenant, time.Now())

	require.NoError(t, err)
	assert.False(t, out.Clean(), "p-beta's checkpoint still holds this tenant's windows and timers")
	assert.Contains(t, out.Reason(), `"p-beta"`, "the partition that did not answer must be named")
	assert.NotContains(t, out.Reason(), "p-alpha", "p-alpha answered and erased, so naming it in "+
		"the deletion record sends an operator to restart a healthy engine and to hunt for state "+
		"on a shard that has none")
	assert.Equal(t, int64(2), out.Rows,
		"what p-alpha evicted is still work this pass found, deferral or not")
}

// TestNoBrokerConnectionIsRetryableRatherThanClean pins the direction the very first failure
// goes.
//
// With no connection there is no way to ask, and no way to know — which is a reason to come
// back, not a reason to conclude the engine holds nothing. Returning a clean outcome here
// would complete a purge over an engine nobody spoke to, on any instance whose broker
// connection had not been established yet.
func TestNoBrokerConnectionIsRetryableRatherThanClean(t *testing.T) {
	out, err := NewDetect(detectConn{nil}, detectCatalog(t, true, "p-alpha"), detectInstance).
		Erase(context.Background(), detectTenant, time.Now())

	require.Error(t, err, "nothing was asked and nothing is known; the ledger records this as "+
		"retryable rather than as an erasure")
	assert.Zero(t, out.Rows)
}
