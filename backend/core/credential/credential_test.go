// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential_test

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

	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

const secret = "correct horse"

// testPolicy is small enough to walk to the cap in a few steps.
var testPolicy = credential.Policy{Free: 3, Base: time.Second, Cap: 8 * time.Second}

func policies(p credential.Policy) map[credential.Kind]credential.Policy {
	return map[credential.Kind]credential.Policy{credential.KindIdentity: p, credential.KindOAuthClient: p}
}

// clock is a manual clock: time moves only when a test says so.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Unix(1_700_000_000, 0)} }
func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// account is a lookup that counts its calls. A call to lookup is the observable proof
// that an attempt was ADMITTED: Check runs the compare only after it.
type account struct {
	hash  string
	calls atomic.Int32
}

func newAccount(t *testing.T) *account {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.MinCost)
	require.NoError(t, err)
	return &account{hash: string(h)}
}

// unknown is a lookup for a principal that does not exist.
func unknownAccount() *account { return &account{} }

func (a *account) lookup(context.Context) (string, error) {
	a.calls.Add(1)
	return a.hash, nil
}

func newChecker(t *testing.T, store credential.Store, clk *clock, opts ...credential.Option) *credential.Checker {
	t.Helper()
	c, err := credential.NewChecker(store, policies(testPolicy), append(opts, credential.WithClock(clk.Now))...)
	require.NoError(t, err)
	return c
}

var alice = credential.Principal{Kind: credential.KindIdentity, ID: "alice@example.com"}

func retryAfter(t *testing.T, err error) time.Duration {
	t.Helper()
	var th *credential.ThrottledError
	require.Truef(t, errors.As(err, &th), "want a ThrottledError, got %v", err)
	return th.RetryAfter
}

// The schedule, exactly: Free attempts back to back, then Base, doubling, capped.
func TestEscalationSchedule(t *testing.T) {
	clk := newClock()
	c := newChecker(t, credentialtest.NewStore(), clk)
	acct := newAccount(t)
	ctx := context.Background()

	// Attempts 1 and 2 carry no delay; the next one is evaluated at once.
	for i := 1; i <= testPolicy.Free-1; i++ {
		require.ErrorIs(t, c.Check(ctx, alice, "wrong", acct.lookup), credential.ErrMismatch, "attempt %d", i)
	}
	// Attempt 3 (the Free-th) is evaluated, and it is the one that sets Base.
	require.ErrorIs(t, c.Check(ctx, alice, "wrong", acct.lookup), credential.ErrMismatch)
	require.Equal(t, int32(testPolicy.Free), acct.calls.Load())

	for _, want := range []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second} {
		err := c.Check(ctx, alice, "wrong", acct.lookup)
		require.Equal(t, want, retryAfter(t, err))
		// Half-way through the delay it is still throttled, with the rest to go.
		clk.Advance(want / 2)
		require.Equal(t, want-want/2, retryAfter(t, c.Check(ctx, alice, "wrong", acct.lookup)))
		clk.Advance(want - want/2)
		require.ErrorIs(t, c.Check(ctx, alice, "wrong", acct.lookup), credential.ErrMismatch)
	}
}

// A success resets the count: the next failure is attempt 1 again.
func TestSuccessResets(t *testing.T) {
	clk := newClock()
	store := credentialtest.NewStore()
	c := newChecker(t, store, clk)
	acct := newAccount(t)
	ctx := context.Background()

	for i := 0; i < testPolicy.Free; i++ {
		require.ErrorIs(t, c.Check(ctx, alice, "wrong", acct.lookup), credential.ErrMismatch)
	}
	require.Equal(t, time.Second, retryAfter(t, c.Check(ctx, alice, secret, acct.lookup)),
		"a correct password during the delay is still throttled — it is not evaluated")
	clk.Advance(time.Second)
	require.NoError(t, c.Check(ctx, alice, secret, acct.lookup))
	require.Equal(t, 0, store.Len(), "a success deletes the record")

	// Free-1 failures in a row are evaluated back to back again.
	for i := 1; i < testPolicy.Free; i++ {
		require.ErrorIs(t, c.Check(ctx, alice, "wrong", acct.lookup), credential.ErrMismatch)
	}
}

// 🔴 A THROTTLED ATTEMPT DOES NO WORK: no lookup, no compare, no write. Asserted as a
// VALUE on the store's trace (exactly one read), with the positive control first —
// the same counters do move for an admitted attempt, so their staying still means
// something.
func TestThrottledAttemptDoesNoWork(t *testing.T) {
	clk := newClock()
	store := credentialtest.NewStore()
	c := newChecker(t, store, clk)
	acct := newAccount(t)
	ctx := context.Background()

	// Positive control: an admitted attempt reads, charges, and looks up.
	require.ErrorIs(t, c.Check(ctx, alice, "wrong", acct.lookup), credential.ErrMismatch)
	require.Equal(t, int32(1), acct.calls.Load())
	require.Equal(t, []string{"get:not-found", "create:ok"}, store.Ops())

	for i := 1; i < testPolicy.Free; i++ {
		require.ErrorIs(t, c.Check(ctx, alice, "wrong", acct.lookup), credential.ErrMismatch)
	}
	before := len(store.Ops())
	calls := acct.calls.Load()

	_ = retryAfter(t, c.Check(ctx, alice, secret, acct.lookup))
	assert.Equal(t, calls, acct.calls.Load(), "a throttled attempt must not look the principal up")
	assert.Equal(t, []string{"get:ok"}, store.Ops()[before:], "a throttled attempt reads once and writes nothing")
}

// 🔴 CONCURRENT ATTEMPTS CANNOT ALL GET IN. Pre-charging is what closes the parallel
// bypass: with a count taken only on failure, every request arriving before the first
// compare finished would read "no failures" and run. At most Free attempts are admitted
// before the delay applies, however many race.
func TestConcurrentAttemptsAreBounded(t *testing.T) {
	clk := newClock()
	c := newChecker(t, credentialtest.NewStore(), clk)
	acct := newAccount(t)

	const racers = 40
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = c.Check(context.Background(), alice, "wrong", acct.lookup)
		}()
	}
	close(start)
	wg.Wait()
	admitted := acct.calls.Load()
	assert.GreaterOrEqual(t, admitted, int32(1))
	assert.LessOrEqualf(t, admitted, int32(testPolicy.Free),
		"%d of %d concurrent attempts were evaluated; the limit is %d", admitted, racers, testPolicy.Free)
}

// A charge that loses the compare-and-set race re-reads and counts on top of the
// winner, rather than overwriting it.
func TestLostRaceRereadsAndCountsOnTop(t *testing.T) {
	clk := newClock()
	store := credentialtest.NewStore()
	c := newChecker(t, store, clk)
	acct := newAccount(t)

	var fired atomic.Bool
	store.BeforeWrite = func(key string) {
		// The competing write below goes through this same hook, so it must not recurse.
		if fired.Swap(true) {
			return
		}
		// A competing replica charges first, in the gap between our read and write.
		body, _ := json.Marshal(map[string]int64{"f": 1, "nb": clk.Now().UnixMilli()})
		_, err := store.Create(key, body)
		require.NoError(t, err)
	}
	require.ErrorIs(t, c.Check(context.Background(), alice, "wrong", acct.lookup), credential.ErrMismatch)

	entry, err := store.Get(credential.Key(alice))
	require.NoError(t, err)
	var rec struct {
		F int `json:"f"`
	}
	require.NoError(t, json.Unmarshal(entry.Value(), &rec))
	assert.Equal(t, 2, rec.F, "the loser must count on top of the winner's charge")
}

// 🔴 AN UNKNOWN PRINCIPAL IS INDISTINGUISHABLE FROM A KNOWN ONE. Same outcomes, same
// RetryAfter sequence, and the same sequence of store operations — the last is what a
// timing observer could otherwise tell apart.
func TestUnknownAndKnownPrincipalsBehaveIdentically(t *testing.T) {
	type step struct {
		outcome    string
		retryAfter time.Duration
	}
	run := func(acct *account, id string) ([]step, []string) {
		clk := newClock()
		store := credentialtest.NewStore()
		c := newChecker(t, store, clk)
		p := credential.Principal{Kind: credential.KindIdentity, ID: id}
		var steps []step
		for i := 0; i < 8; i++ {
			err := c.Check(context.Background(), p, "wrong", acct.lookup)
			var th *credential.ThrottledError
			switch {
			case errors.As(err, &th):
				steps = append(steps, step{"throttled", th.RetryAfter})
				clk.Advance(th.RetryAfter)
			case errors.Is(err, credential.ErrMismatch):
				steps = append(steps, step{"mismatch", 0})
			default:
				t.Fatalf("unexpected: %v", err)
			}
		}
		return steps, store.Ops()
	}
	knownSteps, knownOps := run(newAccount(t), "alice@example.com")
	unknownSteps, unknownOps := run(unknownAccount(), "nobody@example.com")
	require.Equal(t, knownSteps, unknownSteps)
	require.Equal(t, knownOps, unknownOps)
	// And the sequence actually escalated, so the equality above compared something.
	require.Contains(t, knownSteps, step{"throttled", 2 * time.Second})
}

// 🔴 AN UNKNOWN PRINCIPAL PAYS A REAL COMPARE, AT PRODUCTION COST. The dummy compare is
// the timing equalizer: without it an unknown identifier answers in microseconds and a
// known one in tens of milliseconds, which leaks existence to anyone with a stopwatch.
// Outcomes and store traces cannot see it skipped, so the compare itself is observed —
// once per admitted check, against a hash of the cost production passwords and client
// secrets are stored at. Both kinds of policy are covered, since they take different
// paths to the compare.
func TestUnknownPrincipalPaysARealCompare(t *testing.T) {
	for _, pol := range []credential.Policy{testPolicy, {Unthrottled: true}} {
		t.Run(fmt.Sprintf("unthrottled=%v", pol.Unthrottled), func(t *testing.T) {
			c, err := credential.NewChecker(credentialtest.NewStore(), policies(pol))
			require.NoError(t, err)
			var seen [][]byte
			credential.ObserveCompares(c, func(h []byte) { seen = append(seen, h) })

			// Control: a known principal's check compares its own hash.
			acct := newAccount(t)
			require.ErrorIs(t, c.Check(context.Background(), alice, "wrong", acct.lookup), credential.ErrMismatch)
			require.Len(t, seen, 1, "control: a known principal's check must compare once")
			assert.Equal(t, acct.hash, string(seen[0]))

			seen = nil
			nobody := credential.Principal{Kind: credential.KindIdentity, ID: "nobody@example.com"}
			require.ErrorIs(t, c.Check(context.Background(), nobody, "wrong", unknownAccount().lookup), credential.ErrMismatch)
			require.Len(t, seen, 1, "an unknown principal must pay exactly one compare")
			cost, err := bcrypt.Cost(seen[0])
			require.NoError(t, err, "the unknown principal's compare must be against a real bcrypt hash")
			assert.Equal(t, bcrypt.DefaultCost, cost, "the dummy must cost what a stored secret costs")
		})
	}
}

// 🔴 THE DUMMY COMPARE SURVIVES FAILING OPEN. Whoever sprays addresses controls whether
// the store is full, so an unknown principal that answered fast on the fail-open path
// would hand that attacker account enumeration back. With every charge refused for
// fullness, an unknown principal must still pay exactly one compare, at production cost.
func TestUnknownPrincipalPaysARealCompareWhenTheStoreIsFull(t *testing.T) {
	store := credentialtest.NewStore()
	c := newChecker(t, store, newClock())
	var seen [][]byte
	credential.ObserveCompares(c, func(h []byte) { seen = append(seen, h) })
	store.BeforeWrite = func(string) { store.Fail = fullStore }

	// Control: a known principal on the fail-open path compares its own hash.
	acct := newAccount(t)
	require.ErrorIs(t, c.Check(context.Background(), alice, "wrong", acct.lookup), credential.ErrMismatch)
	require.Len(t, seen, 1, "control: a known principal's fail-open check must compare once")
	assert.Equal(t, acct.hash, string(seen[0]))
	require.Equal(t, 0, store.Len(), "control: the charge really was refused, so this was the fail-open path")

	store.Fail = nil
	seen = nil
	nobody := credential.Principal{Kind: credential.KindIdentity, ID: "nobody@example.com"}
	require.ErrorIs(t, c.Check(context.Background(), nobody, "wrong", unknownAccount().lookup), credential.ErrMismatch)
	require.Len(t, seen, 1, "an unknown principal must pay exactly one compare while the store is full")
	cost, err := bcrypt.Cost(seen[0])
	require.NoError(t, err, "the unknown principal's compare must be against a real bcrypt hash")
	assert.Equal(t, bcrypt.DefaultCost, cost, "the dummy must cost what a stored secret costs")
	require.Equal(t, 0, store.Len(), "the unknown principal's charge was refused too")
}

// An UNTHROTTLED kind compares and does nothing else: no record is read or written, so
// no number of failures delays the correct secret, and no identifier can be locked out
// by someone who merely knows it.
func TestUnthrottledKindNeverTouchesTheStore(t *testing.T) {
	store := credentialtest.NewStore()
	c, err := credential.NewChecker(store, map[credential.Kind]credential.Policy{
		credential.KindIdentity:    testPolicy,
		credential.KindOAuthClient: {Unthrottled: true},
	}, credential.WithClock(newClock().Now))
	require.NoError(t, err)
	client := credential.Principal{Kind: credential.KindOAuthClient, ID: "grafana"}
	acct := newAccount(t)
	for i := 0; i < 10*testPolicy.Free; i++ {
		require.ErrorIs(t, c.Check(context.Background(), client, "wrong", acct.lookup), credential.ErrMismatch, "attempt %d", i)
	}
	require.NoError(t, c.Check(context.Background(), client, secret, acct.lookup))
	assert.Empty(t, store.Ops(), "an unthrottled kind must not read or write the attempt store")

	// It does not depend on the store either: a broker outage does not stop it.
	store.Fail = errors.New("nats: connection closed")
	require.NoError(t, c.Check(context.Background(), client, secret, acct.lookup))

	// Control: the throttled kind on the same checker does use the store.
	store.Fail = nil
	_ = c.Check(context.Background(), alice, "wrong", acct.lookup)
	assert.NotEmpty(t, store.Ops())
}

// An unknown principal cannot authenticate by presenting the dummy's own secret.
func TestUnknownPrincipalNeverMatches(t *testing.T) {
	c := newChecker(t, credentialtest.NewStore(), newClock())
	err := c.Check(context.Background(), alice, "dc-credential-timing-equalizer", unknownAccount().lookup)
	require.ErrorIs(t, err, credential.ErrMismatch)
}

// 🔴 A STORE FAILURE FAILS CLOSED: the attempt is not evaluated at all.
func TestStoreFailureFailsClosed(t *testing.T) {
	store := credentialtest.NewStore()
	store.Fail = errors.New("nats: connection closed")
	c := newChecker(t, store, newClock())
	acct := newAccount(t)

	err := c.Check(context.Background(), alice, secret, acct.lookup)
	require.ErrorIs(t, err, credential.ErrUnavailable)
	var ue *credential.UnavailableError
	require.True(t, errors.As(err, &ue))
	assert.Equal(t, map[string]any{"code": credential.CodeUnavailable}, ue.Extensions())
	assert.Equal(t, int32(0), acct.calls.Load(), "a CORRECT secret must not authenticate when the attempt cannot be counted")
}

// A write the store refuses for a reason other than a race or a full bucket is
// unavailable, not a throttle — and the attempt is not evaluated.
//
// 🔴 THE SECOND CASE IS A PLAIN error WHOSE TEXT READS LIKE FULLNESS. Fail-open is
// keyed on the JetStream error JetStream actually returns, not on words in a message,
// so this stays closed; jetstream_test.go is what shows the real error does match.
func TestRefusedWriteIsUnavailable(t *testing.T) {
	for _, refusal := range []error{
		errors.New("nats: timeout"),
		errors.New("nats: maximum bytes exceeded"),
	} {
		t.Run(refusal.Error(), func(t *testing.T) {
			store := credentialtest.NewStore()
			store.BeforeWrite = func(string) { store.Fail = refusal }
			c := newChecker(t, store, newClock())
			acct := newAccount(t)
			require.ErrorIs(t, c.Check(context.Background(), alice, secret, acct.lookup), credential.ErrUnavailable)
			assert.Equal(t, int32(0), acct.calls.Load(), "a refused charge that is not fullness must not be evaluated")
		})
	}
}

// fullStore is the error JetStream returns for a write to a bucket at its MaxBytes, as
// observed in jetstream_test.go.
var fullStore = &nats.APIError{Code: 503, ErrorCode: 10077, Description: "maximum bytes exceeded"}

// 🔴 A READ THAT SUCCEEDS FOLLOWED BY A CHARGE REFUSED FOR FULLNESS DOES NOT DENY. A
// principal with a record (not in a delay) is read fine, then its Update is refused
// because the bucket is full — which a replicated bucket does even for an update that
// replaces a message. The attempt is evaluated, charged nothing, and the correct secret
// signs in. The real-bucket tests cannot reach this path on a single server, whose
// store lets a same-size replacement through at the ceiling.
func TestFullStoreOnChargeFailsOpen(t *testing.T) {
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "checks_total"}, []string{"kind", "outcome"})
	store := credentialtest.NewStore()
	clk := newClock()
	c := newChecker(t, store, clk, credential.WithCounter(counter))
	acct := newAccount(t)
	ctx := context.Background()

	// One charged failure: a record exists, and it is not in a delay (Free is 3).
	require.ErrorIs(t, c.Check(ctx, alice, "wrong", acct.lookup), credential.ErrMismatch)
	before, err := store.Get(credential.Key(alice))
	require.NoError(t, err)

	store.BeforeWrite = func(string) { store.Fail = fullStore }
	for i := 0; i < 3*testPolicy.Free; i++ {
		store.Fail = nil
		require.ErrorIs(t, c.Check(ctx, alice, "wrong", acct.lookup), credential.ErrMismatch,
			"attempt %d: a wrong secret still fails normally, and nothing throttles", i)
	}
	store.Fail = nil
	require.NoError(t, c.Check(ctx, alice, secret, acct.lookup), "the correct secret signs in")
	assert.Equal(t, int32(1+3*testPolicy.Free+1), acct.calls.Load(), "every attempt was evaluated")

	store.Fail = nil
	store.BeforeWrite = nil
	after, err := store.Get(credential.Key(alice))
	require.NoError(t, err, "the refused charges and the refused delete left the record as it was")
	assert.Equal(t, before.Revision(), after.Revision(), "nothing was charged")
	assert.Equal(t, float64(3*testPolicy.Free+1),
		testutil.ToFloat64(counter.WithLabelValues("identity", credential.OutcomeStoreFull)))
}

// A success on the fail-open path CLEARS an existing record. The charge was refused for
// fullness but the principal had a record, so the delete a success always makes still
// runs — here the store lets it through — and the next failure starts from zero rather
// than from the count the refused charges left frozen.
func TestFullStoreSuccessStillClearsAnExistingRecord(t *testing.T) {
	store := credentialtest.NewStore()
	c := newChecker(t, store, newClock())
	acct := newAccount(t)
	ctx := context.Background()

	require.ErrorIs(t, c.Check(ctx, alice, "wrong", acct.lookup), credential.ErrMismatch)
	_, err := store.Get(credential.Key(alice))
	require.NoError(t, err, "precondition: a record exists")

	// Refuse the charge for fullness; BeforeWrite does not run for Delete, and Fail is
	// cleared before it, so the delete goes through.
	store.BeforeWrite = func(string) { store.Fail = fullStore }
	var ops []string
	lookup := func(ctx context.Context) (string, error) {
		store.Fail = nil
		ops = append(ops, "lookup")
		return acct.lookup(ctx)
	}
	require.NoError(t, c.Check(ctx, alice, secret, lookup))
	require.Equal(t, []string{"lookup"}, ops, "the attempt was evaluated")

	_, err = store.Get(credential.Key(alice))
	require.ErrorIs(t, err, nats.ErrKeyNotFound, "a success must clear the existing record even when its charge failed open")
}

// A principal already in a delay is still refused while the store is full: reading its
// record needs no room, and failing open is about the CHARGE, not the read.
func TestFullStoreStillHonoursARunningDelay(t *testing.T) {
	store := credentialtest.NewStore()
	c := newChecker(t, store, newClock())
	acct := newAccount(t)
	for i := 0; i < testPolicy.Free; i++ {
		require.ErrorIs(t, c.Check(context.Background(), alice, "wrong", acct.lookup), credential.ErrMismatch)
	}
	store.BeforeWrite = func(string) { store.Fail = fullStore }
	require.Equal(t, time.Second, retryAfter(t, c.Check(context.Background(), alice, secret, acct.lookup)))
	assert.Equal(t, int32(testPolicy.Free), acct.calls.Load())
}

func TestLookupErrorIsReturnedAndCharged(t *testing.T) {
	store := credentialtest.NewStore()
	c := newChecker(t, store, newClock())
	boom := errors.New("database is down")
	err := c.Check(context.Background(), alice, secret, func(context.Context) (string, error) { return "", boom })
	require.ErrorIs(t, err, boom)
	require.Equal(t, 1, store.Len(), "a failed lookup is not a free attempt")
}

func TestConstructionRefusals(t *testing.T) {
	_, err := credential.NewChecker(nil, policies(testPolicy))
	require.ErrorContains(t, err, "attempt store")

	for _, bad := range []credential.Policy{
		{Free: 0, Base: time.Second, Cap: time.Minute},
		{Free: 3, Base: 0, Cap: time.Minute},
		{Free: 3, Base: time.Minute, Cap: time.Second},
		// A cap over half the record TTL could let a record expire mid-delay.
		{Free: 3, Base: time.Second, Cap: credential.AttemptTTL/2 + time.Second},
		// The zero Policy is refused rather than read as "off".
		{},
		// Unthrottled carries no schedule; one set alongside it is a contradiction.
		{Unthrottled: true, Free: 3, Base: time.Second, Cap: time.Minute},
		{Unthrottled: true, Cap: time.Minute},
	} {
		_, err = credential.NewChecker(credentialtest.NewStore(), policies(bad))
		require.Error(t, err, "%+v", bad)
	}
}

// The key hashes the identifier (no plaintext email reaches NATS) and separates kinds.
func TestKey(t *testing.T) {
	k := credential.Key(alice)
	assert.True(t, strings.HasPrefix(k, "identity."), k)
	assert.NotContains(t, k, "alice")
	assert.NotContains(t, k, "@")
	assert.NotEqual(t, k, credential.Key(credential.Principal{Kind: credential.KindOAuthClient, ID: alice.ID}))
	assert.Len(t, k, len("identity.")+64)
}

func TestThrottledErrorShape(t *testing.T) {
	e := &credential.ThrottledError{RetryAfter: 1500 * time.Millisecond}
	assert.Equal(t, 2, e.RetryAfterSeconds(), "rounded UP")
	assert.Equal(t, "too many failed sign-in attempts; try again in 2 seconds", e.Error())
	assert.Equal(t, "too many failed sign-in attempts; try again in 1 second", (&credential.ThrottledError{RetryAfter: time.Second}).Error())
	assert.Equal(t, map[string]any{"code": "THROTTLED", "retryAfterSeconds": 2}, e.Extensions())
	assert.Equal(t, 1, (&credential.ThrottledError{RetryAfter: time.Millisecond}).RetryAfterSeconds())
}

// Every outcome is counted under its own label — the only view of an attack held at the
// cap, since throttled attempts write no audit row.
func TestOutcomesAreCounted(t *testing.T) {
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "checks_total"}, []string{"kind", "outcome"})
	clk := newClock()
	store := credentialtest.NewStore()
	c := newChecker(t, store, clk, credential.WithCounter(counter))
	acct := newAccount(t)
	ctx := context.Background()

	require.NoError(t, c.Check(ctx, alice, secret, acct.lookup))
	for i := 0; i < testPolicy.Free; i++ {
		_ = c.Check(ctx, alice, "wrong", acct.lookup)
	}
	_ = c.Check(ctx, alice, "wrong", acct.lookup)
	store.Fail = errors.New("down")
	_ = c.Check(ctx, alice, "wrong", acct.lookup)

	get := func(outcome string) float64 {
		return testutil.ToFloat64(counter.WithLabelValues("identity", outcome))
	}
	assert.Equal(t, 1.0, get(credential.OutcomeSuccess))
	assert.Equal(t, float64(testPolicy.Free), get(credential.OutcomeMismatch))
	assert.Equal(t, 1.0, get(credential.OutcomeThrottled))
	assert.Equal(t, 1.0, get(credential.OutcomeUnavailable))
	assert.Equal(t, 0.0, get(credential.OutcomeStoreFull))
}

// The store-full series exists at zero from construction, for throttled kinds only. An
// alert over increase() cannot see a series' first sample, so a series born with the
// first fail-open would hide it.
func TestStoreFullSeriesIsExportedAtZero(t *testing.T) {
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "checks_total"}, []string{"kind", "outcome"})
	_, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIdentity:    testPolicy,
		credential.KindOAuthClient: {Unthrottled: true},
	}, credential.WithCounter(counter))
	require.NoError(t, err)
	require.Equal(t, 1, testutil.CollectAndCount(counter))
	assert.Equal(t, 0.0, testutil.ToFloat64(counter.WithLabelValues("identity", credential.OutcomeStoreFull)))
}

// The delay schedule does not overflow a long way past the cap.
func TestDelayNeverOverflows(t *testing.T) {
	clk := newClock()
	store := credentialtest.NewStore()
	c, err := credential.NewChecker(store, policies(credential.Policy{Free: 1, Base: time.Minute, Cap: 4 * time.Minute}),
		credential.WithClock(clk.Now))
	require.NoError(t, err)
	acct := newAccount(t)
	for i := 0; i < 80; i++ {
		err := c.Check(context.Background(), alice, "wrong", acct.lookup)
		var th *credential.ThrottledError
		if errors.As(err, &th) {
			require.LessOrEqual(t, th.RetryAfter, 4*time.Minute)
			require.Greater(t, th.RetryAfter, time.Duration(0))
			clk.Advance(th.RetryAfter)
		}
	}
}

// compile-time: the production store type satisfies the interface.
var _ credential.Store = nats.KeyValue(nil)
