// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credcheck

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeBackend struct {
	mu      sync.Mutex
	calls   int
	accept  func(Presented) bool
	err     error
	release chan struct{}
	started chan struct{}
}

func (f *fakeBackend) Check(ctx context.Context, _ string, p Presented) (bool, error) {
	f.mu.Lock()
	f.calls++
	started, release, accept, err := f.started, f.release, f.accept, f.err
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		<-release
	}
	if err != nil {
		return false, err
	}
	return accept(p), nil
}

func (f *fakeBackend) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newVerifier(t *testing.T, b Backend, mutate func(*Config)) (*Verifier, *clock) {
	t.Helper()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	cfg := Config{Now: clk.now}
	if mutate != nil {
		mutate(&cfg)
	}
	v, err := New(b, cfg)
	require.NoError(t, err)
	return v, clk
}

var good = Presented{Device: "d1", Type: "MQTT_BASIC", Id: "user-1", Secret: "right-secret"}

func onlyGood(p Presented) bool { return p.Secret == "right-secret" }

func TestVerify_AcceptedAndRejected(t *testing.T) {
	b := &fakeBackend{accept: onlyGood}
	v, _ := newVerifier(t, b, nil)
	assert.Equal(t, Accepted, v.Verify(context.Background(), "acme", good))
	bad := good
	bad.Secret = "wrong"
	assert.Equal(t, Rejected, v.Verify(context.Background(), "acme", bad))
}

// The cache is the reason there is no per-request round trip: repeating the same request costs
// nothing after the first, in both directions.
func TestVerify_RemembersAVerdictForItsTTL(t *testing.T) {
	b := &fakeBackend{accept: onlyGood}
	v, clk := newVerifier(t, b, nil)
	bad := good
	bad.Secret = "wrong"
	for i := 0; i < 50; i++ {
		assert.Equal(t, Accepted, v.Verify(context.Background(), "acme", good))
		assert.Equal(t, Rejected, v.Verify(context.Background(), "acme", bad))
	}
	assert.Equal(t, 2, b.callCount(), "one call per distinct request, not per event")

	clk.advance(11 * time.Second) // past the rejected TTL, inside the accepted one
	assert.Equal(t, Accepted, v.Verify(context.Background(), "acme", good))
	assert.Equal(t, 2, b.callCount())
	assert.Equal(t, Rejected, v.Verify(context.Background(), "acme", bad))
	assert.Equal(t, 3, b.callCount(), "a rejected verdict expires first, so a fixed credential is not held to it")

	clk.advance(31 * time.Second)
	assert.Equal(t, Accepted, v.Verify(context.Background(), "acme", good))
	assert.Equal(t, 4, b.callCount())
}

// A verdict depends on every field of the request, so none may be left out of the key.
func TestVerify_KeyCoversEveryField(t *testing.T) {
	b := &fakeBackend{accept: func(Presented) bool { return true }}
	v, _ := newVerifier(t, b, nil)
	variants := []struct {
		tenant string
		p      Presented
	}{
		{"acme", good},
		{"other", good},
		{"acme", Presented{Device: "d2", Type: good.Type, Id: good.Id, Secret: good.Secret}},
		{"acme", Presented{Device: good.Device, Type: "ACCESS_TOKEN", Id: good.Id, Secret: good.Secret}},
		{"acme", Presented{Device: good.Device, Type: good.Type, Id: "user-2", Secret: good.Secret}},
		{"acme", Presented{Device: good.Device, Type: good.Type, Id: good.Id, Secret: "other"}},
	}
	for _, c := range variants {
		v.Verify(context.Background(), c.tenant, c.p)
	}
	assert.Equal(t, len(variants), b.callCount(), "no two distinct requests may share a verdict")
	// Field boundaries are part of the key: ("ab","c") is not ("a","bc").
	v.Verify(context.Background(), "acme", Presented{Device: "ab", Type: "c"})
	v.Verify(context.Background(), "acme", Presented{Device: "a", Type: "bc"})
	assert.Equal(t, len(variants)+2, b.callCount())
}

// The secret is never held: neither as a key nor in any entry.
func TestVerify_NeverHoldsThePlaintextSecret(t *testing.T) {
	b := &fakeBackend{accept: onlyGood}
	v, _ := newVerifier(t, b, nil)
	v.Verify(context.Background(), "acme", good)
	v.mu.Lock()
	defer v.mu.Unlock()
	for k := range v.verdicts {
		assert.NotContains(t, string(k[:]), good.Secret)
	}
	assert.Len(t, v.verdicts, 1)
}

// The digest input holds the presented secret in the clear, in a buffer that is reused. It is
// wiped before it goes back, so the pool never holds a secret between requests.
func TestDigest_WipesItsScratchBuffer(t *testing.T) {
	v, _ := newVerifier(t, &fakeBackend{accept: onlyGood}, nil)
	v.digest("acme", "d1", "MQTT_BASIC", "user-1", "super-secret-value-0123456789")
	sc := v.scratch.Get().(*scratch)
	defer v.scratch.Put(sc)
	raw := sc.buf[:cap(sc.buf)]
	if bytes.Contains(raw, []byte("super-secret")) {
		t.Fatal("the pooled scratch buffer still holds the secret")
	}
}

// The pooled digest is the HMAC of the same input a fresh hasher would compute.
func TestDigest_MatchesAFreshHMAC(t *testing.T) {
	v, _ := newVerifier(t, &fakeBackend{accept: onlyGood}, nil)
	m := hmac.New(sha256.New, v.salt)
	for _, f := range []string{"acme", "", "d1"} {
		n := len(f)
		m.Write([]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
		m.Write([]byte(f))
	}
	var want key
	m.Sum(want[:0])
	for i := 0; i < 3; i++ { // repeated, so a reused hasher that is not reset shows
		assert.Equal(t, want, v.digest("acme", "", "d1"))
	}
}

// Two verifiers key differently: a digest from one process means nothing to another.
func TestVerify_DigestIsKeyedPerProcess(t *testing.T) {
	a, _ := newVerifier(t, &fakeBackend{accept: onlyGood}, nil)
	b, _ := newVerifier(t, &fakeBackend{accept: onlyGood}, nil)
	assert.NotEqual(t, a.digest("acme", "x"), b.digest("acme", "x"))
	assert.Equal(t, a.digest("acme", "x"), a.digest("acme", "x"))
}

// The oracle bound. A credential that keeps failing is answered Rejected FreeFailures times and
// then not at all: Unknown, which the door treats as 202. A guesser then cannot tell right from
// wrong by the status until the hold lapses.
func TestVerify_HoldsACredentialThatKeepsFailing(t *testing.T) {
	b := &fakeBackend{accept: onlyGood}
	var skips []string
	v, clk := newVerifier(t, b, func(c *Config) { c.OnSkip = func(r string) { skips = append(skips, r) } })
	guess := func(i int) Presented {
		p := good
		p.Secret = fmt.Sprintf("guess-%d", i)
		return p
	}
	for i := 0; i < 5; i++ {
		assert.Equal(t, Rejected, v.Verify(context.Background(), "acme", guess(i)), "guess %d", i)
	}
	assert.Equal(t, 5, b.callCount())

	// Held: neither a wrong guess nor the RIGHT secret reaches the backend or is told apart.
	assert.Equal(t, Unknown, v.Verify(context.Background(), "acme", guess(99)))
	assert.Equal(t, Unknown, v.Verify(context.Background(), "acme", good))
	assert.Equal(t, 5, b.callCount(), "a held credential costs the backend nothing")
	assert.Equal(t, []string{"held", "held"}, skips)

	// After the first hold lapses (1s) one guess is asked again, and a further failure doubles it.
	clk.advance(1100 * time.Millisecond)
	assert.Equal(t, Rejected, v.Verify(context.Background(), "acme", guess(100)))
	clk.advance(1100 * time.Millisecond)
	assert.Equal(t, Unknown, v.Verify(context.Background(), "acme", guess(101)), "the second hold is 2s")
	clk.advance(1 * time.Second)
	assert.Equal(t, Rejected, v.Verify(context.Background(), "acme", guess(102)))
	// The right secret, once the hold lapses, authenticates and clears the record.
	clk.advance(5 * time.Second)
	assert.Equal(t, Accepted, v.Verify(context.Background(), "acme", good))
	for i := 0; i < 5; i++ {
		assert.Equal(t, Rejected, v.Verify(context.Background(), "acme", guess(200+i)), "a success resets the count")
	}
}

// The hold is per credential, not per tenant or per secret.
func TestVerify_HoldIsPerCredential(t *testing.T) {
	b := &fakeBackend{accept: onlyGood}
	v, _ := newVerifier(t, b, nil)
	for i := 0; i < 5; i++ {
		p := good
		p.Secret = fmt.Sprintf("g%d", i)
		v.Verify(context.Background(), "acme", p)
	}
	other := good
	other.Id = "user-2"
	other.Secret = "right-secret"
	assert.Equal(t, Accepted, v.Verify(context.Background(), "acme", other), "another credential is unaffected")
	assert.Equal(t, Accepted, v.Verify(context.Background(), "other-tenant", good), "another tenant is unaffected")
}

func TestVerify_HoldGrowsToTheCap(t *testing.T) {
	v, _ := newVerifier(t, &fakeBackend{accept: onlyGood}, nil)
	assert.Equal(t, time.Duration(0), v.hold(0))
	assert.Equal(t, time.Duration(0), v.hold(4), "four failures are still free")
	assert.Equal(t, 1*time.Second, v.hold(5), "the fifth failure starts the hold")
	assert.Equal(t, 2*time.Second, v.hold(6))
	assert.Equal(t, 32*time.Second, v.hold(10))
	assert.Equal(t, 60*time.Second, v.hold(11))
	assert.Equal(t, 60*time.Second, v.hold(1000))
}

// A full failure table cannot count a new credential, and a credential that cannot be counted
// cannot be throttled, so it is not answered: Unknown.
func TestVerify_FullFailureTableDoesNotAnswer(t *testing.T) {
	b := &fakeBackend{accept: onlyGood}
	var skips []string
	v, clk := newVerifier(t, b, func(c *Config) {
		c.FailureEntries = 2
		c.OnSkip = func(r string) { skips = append(skips, r) }
	})
	for i := 0; i < 2; i++ {
		p := good
		p.Id = fmt.Sprintf("u%d", i)
		p.Secret = "wrong"
		assert.Equal(t, Rejected, v.Verify(context.Background(), "acme", p))
	}
	p := good
	p.Id = "u-new"
	p.Secret = "wrong"
	assert.Equal(t, Unknown, v.Verify(context.Background(), "acme", p))
	assert.Equal(t, []string{"table_full"}, skips)

	// Records that have outlived their longest hold are reclaimed, so the table is not a lockout.
	clk.advance(3 * time.Minute)
	assert.Equal(t, Rejected, v.Verify(context.Background(), "acme", p))
}

// Failing to ask is Unknown, is never cached, and is counted.
func TestVerify_BackendErrorIsUnknownAndNotRemembered(t *testing.T) {
	b := &fakeBackend{accept: onlyGood, err: errors.New("device-management is down")}
	var outcomes []string
	v, _ := newVerifier(t, b, func(c *Config) { c.OnCall = func(o string) { outcomes = append(outcomes, o) } })
	assert.Equal(t, Unknown, v.Verify(context.Background(), "acme", good))
	assert.Equal(t, Unknown, v.Verify(context.Background(), "acme", good))
	assert.Equal(t, 2, b.callCount(), "an error is asked again")
	assert.Equal(t, []string{"error", "error"}, outcomes)

	b.mu.Lock()
	b.err = nil
	b.mu.Unlock()
	assert.Equal(t, Accepted, v.Verify(context.Background(), "acme", good), "and recovers")
}

// An error does not count against the credential: a flaky dependency must not put a legitimate
// device into hold-off.
func TestVerify_BackendErrorsDoNotCountAsFailures(t *testing.T) {
	b := &fakeBackend{accept: onlyGood, err: errors.New("boom")}
	v, _ := newVerifier(t, b, nil)
	for i := 0; i < 20; i++ {
		v.Verify(context.Background(), "acme", good)
	}
	b.mu.Lock()
	b.err = nil
	b.mu.Unlock()
	assert.Equal(t, Accepted, v.Verify(context.Background(), "acme", good))
}

// The in-flight bound: a request over it is Unknown immediately rather than queued.
func TestVerify_BusyIsUnknown(t *testing.T) {
	b := &fakeBackend{accept: onlyGood, release: make(chan struct{}), started: make(chan struct{}, 4)}
	var skips atomic.Int32
	v, _ := newVerifier(t, b, func(c *Config) {
		c.MaxInFlight = 1
		c.OnSkip = func(r string) {
			if r == "busy" {
				skips.Add(1)
			}
		}
	})
	done := make(chan Verdict, 1)
	go func() { done <- v.Verify(context.Background(), "acme", good) }()
	<-b.started

	other := good
	other.Id = "user-2"
	assert.Equal(t, Unknown, v.Verify(context.Background(), "acme", other))
	assert.EqualValues(t, 1, skips.Load())
	close(b.release)
	assert.Equal(t, Accepted, <-done)
}

// Concurrent identical requests share one call.
func TestVerify_IdenticalConcurrentRequestsShareOneCall(t *testing.T) {
	b := &fakeBackend{accept: onlyGood, release: make(chan struct{}), started: make(chan struct{}, 16)}
	v, _ := newVerifier(t, b, nil)
	var wg sync.WaitGroup
	results := make(chan Verdict, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- v.Verify(context.Background(), "acme", good)
		}()
	}
	<-b.started
	time.Sleep(50 * time.Millisecond) // let the rest join the flight
	close(b.release)
	wg.Wait()
	close(results)
	for r := range results {
		assert.Equal(t, Accepted, r)
	}
	assert.Equal(t, 1, b.callCount())
}

// 🔴 THE THRESHOLD HOLDS UNDER CONCURRENCY. N different guesses at one credential, all in flight
// before any has come back, must not all be answered: one call per credential at a time, the rest
// UNKNOWN. Without it the hold-off bounds guesses per burst and not per credential.
func TestVerify_OneCallPerCredentialAtATime(t *testing.T) {
	b := &fakeBackend{accept: onlyGood, release: make(chan struct{}), started: make(chan struct{}, 32)}
	var skips sync.Map
	v, _ := newVerifier(t, b, func(c *Config) {
		c.OnSkip = func(r string) {
			n, _ := skips.LoadOrStore(r, new(atomic.Int32))
			n.(*atomic.Int32).Add(1)
		}
	})
	results := make(chan Verdict, 10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p := good
			p.Secret = fmt.Sprintf("guess-%d", i)
			results <- v.Verify(context.Background(), "acme", p)
		}(i)
	}
	<-b.started // one is in flight
	// The nine others are answered at once, without waiting for it.
	unknown := 0
	for i := 0; i < 9; i++ {
		select {
		case r := <-results:
			assert.Equal(t, Unknown, r)
			unknown++
		case <-time.After(5 * time.Second):
			t.Fatal("a guess at a credential with a call in flight waited instead of being answered UNKNOWN")
		}
	}
	close(b.release)
	assert.Equal(t, Rejected, <-results)
	assert.Equal(t, 9, unknown)
	assert.Equal(t, 1, b.callCount(), "only one call reached the backend")
	n, _ := skips.Load("probing")
	require.NotNil(t, n)
	assert.EqualValues(t, 9, n.(*atomic.Int32).Load())
}

// The same wrong request posted by many devices at once is ONE call and ONE rejection: the
// waiters share the verdict and the accounting, so a fleet cannot put a credential into hold-off
// (or inflate the call counter) by sharing a bad one.
func TestVerify_IdenticalConcurrentRejectionsAreCountedOnce(t *testing.T) {
	b := &fakeBackend{accept: onlyGood, release: make(chan struct{}), started: make(chan struct{}, 32)}
	var calls atomic.Int32
	v, _ := newVerifier(t, b, func(c *Config) { c.OnCall = func(string) { calls.Add(1) } })
	bad := good
	bad.Secret = "wrong"
	results := make(chan Verdict, 8)
	for i := 0; i < 8; i++ {
		go func() { results <- v.Verify(context.Background(), "acme", bad) }()
	}
	<-b.started
	time.Sleep(50 * time.Millisecond) // let the rest join the flight
	close(b.release)
	for i := 0; i < 8; i++ {
		assert.Equal(t, Rejected, <-results)
	}
	assert.Equal(t, 1, b.callCount())
	assert.EqualValues(t, 1, calls.Load(), "the call counter counts calls, not callers")
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, f := range v.failures {
		assert.Equal(t, 1, f.count, "eight callers sharing one call are one failure")
	}
}

// The hold is checked again once the credential's slot is held: the call that held it before may
// have just started the hold, and the verdict this call would release is past the threshold.
func TestAsk_RechecksTheHoldOnceItHoldsTheSlot(t *testing.T) {
	b := &fakeBackend{accept: onlyGood}
	v, clk := newVerifier(t, b, nil)
	who := v.digest("acme", good.Type, good.Id)
	v.failures[who] = failureEntry{count: v.cfg.FreeFailures, last: clk.now()}
	assert.Equal(t, Unknown, v.ask(context.Background(), "acme", good, v.digest("k"), who))
	assert.Equal(t, 0, b.callCount(), "a held credential reached the backend")
	v.mu.Lock()
	_, stillProbing := v.probing[who]
	v.mu.Unlock()
	assert.False(t, stillProbing, "the slot is released on every path")
}

// The cache is bounded: a flood of distinct requests cannot grow it past Entries.
func TestVerify_CacheIsBounded(t *testing.T) {
	b := &fakeBackend{accept: func(Presented) bool { return true }}
	v, _ := newVerifier(t, b, func(c *Config) { c.Entries = 64 })
	for i := 0; i < 1000; i++ {
		v.Verify(context.Background(), "acme", Presented{Device: fmt.Sprintf("d%d", i), Type: "MQTT_BASIC", Id: "u", Secret: "s"})
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	assert.LessOrEqual(t, len(v.verdicts), 64)
}

// A request's cancellation does not cancel the shared call, and the timeout does bound it.
func TestVerify_TimeoutBoundsTheCall(t *testing.T) {
	slow := backendFunc(func(ctx context.Context, _ string, _ Presented) (bool, error) {
		<-ctx.Done()
		return false, ctx.Err()
	})
	v, _ := newVerifier(t, slow, func(c *Config) { c.Timeout = 20 * time.Millisecond })
	start := time.Now()
	assert.Equal(t, Unknown, v.Verify(context.Background(), "acme", good))
	assert.Less(t, time.Since(start), 2*time.Second)
}

type backendFunc func(ctx context.Context, tenant string, p Presented) (bool, error)

func (f backendFunc) Check(ctx context.Context, tenant string, p Presented) (bool, error) {
	return f(ctx, tenant, p)
}

func TestConfigDefaults(t *testing.T) {
	c := Config{}.withDefaults()
	assert.Equal(t, 30*time.Second, c.AcceptedTTL)
	assert.Equal(t, 10*time.Second, c.RejectedTTL)
	assert.Equal(t, 5, c.FreeFailures)
	assert.Equal(t, 60*time.Second, c.MaxHold)
	assert.Equal(t, 32, c.MaxInFlight)
	assert.Equal(t, 400*time.Millisecond, c.Timeout)
	assert.Equal(t, 100_000, c.Entries)
	assert.Equal(t, 50_000, c.FailureEntries)
}

// The DM backend sends exactly the fields the schema names and reads the field it names.
func TestDM_Check(t *testing.T) {
	var gotQuery, gotTenant string
	var gotVars map[string]any
	c := clientFunc(func(ctx context.Context, url, tenant, query string, vars map[string]any, out any) error {
		gotQuery, gotTenant, gotVars = query, tenant, vars
		ptr := out.(*struct {
			Accepted *bool `json:"checkDeviceCredential"`
		})
		yes := true
		ptr.Accepted = &yes
		return nil
	})
	dm := NewDM(c, "http://dm/graphql")
	ok, err := dm.Check(context.Background(), "acme", good)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "acme", gotTenant)
	assert.Contains(t, gotQuery, "checkDeviceCredential(device: $device, credentialType: $type, credentialId: $id, secret: $secret)")
	assert.Equal(t, map[string]any{"device": "d1", "type": "MQTT_BASIC", "id": "user-1", "secret": "right-secret"}, gotVars)

	_, err = dm.Check(context.Background(), "acme", Presented{Device: "d"})
	require.NoError(t, err)
	assert.NotContains(t, gotVars, "secret", "no secret is sent when none was presented")

	failing := NewDM(clientFunc(func(context.Context, string, string, string, map[string]any, any) error {
		return errors.New("down")
	}), "u")
	_, err = failing.Check(context.Background(), "acme", good)
	assert.Error(t, err)
}

// An answer with no verdict (what a faulty peer sends as {} or {"data":null}) is an ERROR, not a
// rejection, and an explicit false is still a rejection. Through the real JSON decoder, so the
// test fails if the field goes back to a plain bool.
func TestDM_Check_MissingVerdictIsNotARejection(t *testing.T) {
	for name, doc := range map[string]string{
		"empty object": `{}`,
		"null field":   `{"checkDeviceCredential":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			dm := NewDM(clientFunc(func(_ context.Context, _, _, _ string, _ map[string]any, out any) error {
				return json.Unmarshal([]byte(doc), out)
			}), "u")
			ok, err := dm.Check(context.Background(), "acme", good)
			assert.ErrorIs(t, err, ErrNoVerdict)
			assert.False(t, ok)

			// Through the verifier: UNKNOWN, and nothing cached (a rejection would be).
			v, _ := New(dm, Config{})
			assert.Equal(t, Unknown, v.Verify(context.Background(), "acme", good))
		})
	}
	dm := NewDM(clientFunc(func(_ context.Context, _, _, _ string, _ map[string]any, out any) error {
		return json.Unmarshal([]byte(`{"checkDeviceCredential":false}`), out)
	}), "u")
	ok, err := dm.Check(context.Background(), "acme", good)
	require.NoError(t, err)
	assert.False(t, ok, "an explicit false is a rejection")
}

type clientFunc func(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error

func (f clientFunc) Query(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error {
	return f(ctx, baseURL, tenant, query, variables, out)
}

// Hot-path cost of a warm verdict (the only cost a steady device pays) and of the digest alone.
func BenchmarkVerify_Warm(b *testing.B) {
	v, _ := New(&fakeBackend{accept: onlyGood}, Config{})
	v.Verify(context.Background(), "acme", good)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if v.Verify(ctx, "acme", good) != Accepted {
			b.Fatal("not accepted")
		}
	}
}

func BenchmarkVerify_WarmParallel(b *testing.B) {
	v, _ := New(&fakeBackend{accept: onlyGood}, Config{})
	v.Verify(context.Background(), "acme", good)
	ctx := context.Background()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			v.Verify(ctx, "acme", good)
		}
	})
}

// A backend that answers "no" after its deadline has passed has not refused anything: the answer is
// unusable, so it is UNKNOWN, uncounted and uncached.
func TestVerify_AnAnswerAfterTheDeadlineIsNotARefusal(t *testing.T) {
	late := backendFunc(func(ctx context.Context, _ string, _ Presented) (bool, error) {
		<-ctx.Done()
		return false, nil // ignores its own cancellation and answers "no"
	})
	var outcomes []string
	v, _ := newVerifier(t, late, func(c *Config) {
		c.Timeout = 10 * time.Millisecond
		c.OnCall = func(o string) { outcomes = append(outcomes, o) }
	})
	bad := good
	bad.Secret = "wrong"
	for i := 0; i < 8; i++ {
		assert.Equal(t, Unknown, v.Verify(context.Background(), "acme", bad))
	}
	assert.Equal(t, []string{"error", "error", "error", "error", "error", "error", "error", "error"}, outcomes)
	v.mu.Lock()
	defer v.mu.Unlock()
	assert.Empty(t, v.failures, "a late answer is not a failure of the credential")
	assert.Empty(t, v.verdicts, "and is not remembered")
}
