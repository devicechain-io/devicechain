// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/credential"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realKV is a KV bucket on an in-process JetStream server.
func realKV(t *testing.T) nats.KeyValue {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(),
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
	kv, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: "credential_attempts_test", TTL: time.Minute})
	require.NoError(t, err)
	return kv
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
