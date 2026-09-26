// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/credential"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realKV is a KV bucket on an in-process JetStream server.
func realKV(t *testing.T) nats.KeyValue {
	kv, _, _ := realKVWith(t, 0)
	return kv
}

// realKVWith is realKV with a byte ceiling (0 = unlimited), also returning the
// connection and its JetStream context so a test can break or reconfigure them.
func realKVWith(t *testing.T, maxBytes int64) (nats.KeyValue, *nats.Conn, nats.JetStreamContext) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	kv, err := js.CreateKeyValue(&nats.KeyValueConfig{
		Bucket: "credential_attempts_test", TTL: time.Minute, MaxBytes: maxBytes,
	})
	require.NoError(t, err)
	return kv, nc, js
}

// fill writes filler keys until the bucket refuses one, and returns that refusal.
func fill(t *testing.T, kv nats.KeyValue) error {
	t.Helper()
	for i := 0; i < 10_000; i++ {
		if _, err := kv.Create(fmt.Sprintf("filler.%d", i), []byte(`{"f":1,"nb":1700000000000}`)); err != nil {
			return err
		}
	}
	t.Fatal("the bucket never filled; its byte ceiling is not being applied")
	return nil
}

// 🔴 A FULL BUCKET FAILS OPEN, against the REAL error. The in-memory tests inject the
// full-bucket error by value; this is what shows that value is what JetStream sends,
// so the fail-open path is reachable in production and not only in a fake.
//
// The bucket is filled with other keys, then a principal with no record signs in: its
// charge (a Create) is refused for fullness, so the attempt is evaluated without its
// backoff. A wrong secret fails as a mismatch however often it is sent, the right one
// signs in, and every one of those is counted as store_full.
func TestFullBucketFailsOpenOnJetStream(t *testing.T) {
	kv, _, _ := realKVWith(t, 4096)
	refusal := fill(t, kv)

	// What JetStream answers at the ceiling — pinned here so a change in nats.go or
	// the server that would silently turn fail-open back into fail-closed goes red.
	var apiErr *nats.APIError
	require.ErrorAs(t, refusal, &apiErr, "the full-bucket refusal: %v", refusal)
	assert.Equal(t, 503, apiErr.Code)
	assert.Equal(t, nats.ErrorCode(10077), apiErr.ErrorCode)
	assert.Equal(t, "maximum bytes exceeded", apiErr.Description)

	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "checks_total"}, []string{"kind", "outcome"})
	c := newChecker(t, kv, newClock(), credential.WithCounter(counter))
	acct := newAccount(t)
	ctx := context.Background()

	for i := 0; i < 3*testPolicy.Free; i++ {
		require.ErrorIs(t, c.Check(ctx, alice, "wrong", acct.lookup), credential.ErrMismatch,
			"attempt %d: a wrong secret fails normally, and nothing throttles", i)
	}
	require.NoError(t, c.Check(ctx, alice, secret, acct.lookup), "the correct secret signs in")
	assert.Equal(t, int32(3*testPolicy.Free+1), acct.calls.Load(), "every attempt was evaluated")

	// An unknown principal is evaluated too — against the dummy, as always.
	require.ErrorIs(t, c.Check(ctx, credential.Principal{Kind: credential.KindIdentity, ID: "nobody@example.com"}, "wrong", unknownAccount().lookup), credential.ErrMismatch)

	_, err := kv.Get(credential.Key(alice))
	require.ErrorIs(t, err, nats.ErrKeyNotFound, "nothing was charged")
	get := func(outcome string) float64 {
		return testutil.ToFloat64(counter.WithLabelValues("identity", outcome))
	}
	assert.Equal(t, float64(3*testPolicy.Free+2), get(credential.OutcomeStoreFull))
	assert.Equal(t, 0.0, get(credential.OutcomeMismatch))
	assert.Equal(t, 0.0, get(credential.OutcomeUnavailable))
}

// The controls: every OTHER store failure still fails closed, on the real server.
func TestOtherStoreFailuresStillFailClosedOnJetStream(t *testing.T) {
	// 🔴 THE NEAREST NEIGHBOUR. A message-count ceiling is refused with the SAME
	// JetStream error code as a byte ceiling (10077) and differs only in its
	// description, so this is what shows the match is on fullness, not on the code.
	t.Run("maximum messages", func(t *testing.T) {
		kv, _, js := realKVWith(t, 0)
		_, err := kv.Create("filler.0", []byte(`{}`))
		require.NoError(t, err)
		info, err := js.StreamInfo("KV_credential_attempts_test")
		require.NoError(t, err)
		cfg := info.Config
		cfg.MaxMsgs = 1
		_, err = js.UpdateStream(&cfg)
		require.NoError(t, err)
		_, err = kv.Create("filler.1", []byte(`{}`))
		var apiErr *nats.APIError
		require.ErrorAs(t, err, &apiErr, "precondition: the bucket refuses a second message")
		require.Equal(t, nats.ErrorCode(10077), apiErr.ErrorCode, "precondition: the same code as a full bucket")
		require.NotEqual(t, "maximum bytes exceeded", apiErr.Description)

		c := newChecker(t, kv, newClock())
		acct := newAccount(t)
		require.ErrorIs(t, c.Check(context.Background(), alice, secret, acct.lookup), credential.ErrUnavailable)
		assert.Equal(t, int32(0), acct.calls.Load(), "a refusal that is not fullness is not evaluated")
	})

	t.Run("broker unreachable", func(t *testing.T) {
		kv, nc, _ := realKVWith(t, 0)
		c := newChecker(t, kv, newClock())
		acct := newAccount(t)
		nc.Close()
		require.ErrorIs(t, c.Check(context.Background(), alice, secret, acct.lookup), credential.ErrUnavailable)
		assert.Equal(t, int32(0), acct.calls.Load(), "an attempt that cannot be counted is not evaluated")
	})
}

// 🔴 THE CHECKER AGAINST A REAL BUCKET, because the in-memory fake is only as good as
// its reading of nats.go. Each property here is one the fake claims to mirror and the
// Checker depends on: the not-found sentinel on a fresh key, Create over the tombstone
// a success leaves behind, and revision conflicts under concurrency.
func TestCheckerAgainstJetStream(t *testing.T) {
	kv := realKV(t)
	clk := newClock()
	c := newChecker(t, kv, clk)
	acct := newAccount(t)
	ctx := context.Background()

	// A fresh key reads as not found, so the first attempts are admitted.
	for i := 0; i < testPolicy.Free; i++ {
		require.ErrorIs(t, c.Check(ctx, alice, "wrong", acct.lookup), credential.ErrMismatch)
	}
	require.Equal(t, time.Second, retryAfter(t, c.Check(ctx, alice, "wrong", acct.lookup)))

	// A success deletes the record, leaving a tombstone...
	clk.Advance(time.Second)
	require.NoError(t, c.Check(ctx, alice, secret, acct.lookup))
	_, err := kv.Get(credential.Key(alice))
	require.ErrorIs(t, err, nats.ErrKeyNotFound)

	// ...which the next charge must be able to Create over.
	require.ErrorIs(t, c.Check(ctx, alice, "wrong", acct.lookup), credential.ErrMismatch)
	entry, err := kv.Get(credential.Key(alice))
	require.NoError(t, err)
	assert.Contains(t, string(entry.Value()), `"f":1`)
}

func TestConcurrentAttemptsAreBoundedOnJetStream(t *testing.T) {
	kv := realKV(t)
	c := newChecker(t, kv, newClock())
	acct := newAccount(t)

	const racers = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	var unexpected sync.Map
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			err := c.Check(context.Background(), alice, "wrong", acct.lookup)
			var th *credential.ThrottledError
			if !errors.Is(err, credential.ErrMismatch) && !errors.As(err, &th) {
				unexpected.Store(i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	unexpected.Range(func(k, v any) bool {
		t.Errorf("racer %v: unexpected error %v", k, v)
		return true
	})
	admitted := acct.calls.Load()
	assert.GreaterOrEqual(t, admitted, int32(1))
	assert.LessOrEqual(t, admitted, int32(testPolicy.Free))
}
