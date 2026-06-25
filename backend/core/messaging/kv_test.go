// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// newTestManager spins up an in-process NATS server with JetStream enabled and
// returns a minimal NatsManager wired to it, plus a cleanup func. It is a
// white-box harness for the KV-backed Cache and DistributedLock: only the js
// context and Microservice identity fields they read are populated.
func newTestManager(t *testing.T) (*NatsManager, func()) {
	t.Helper()
	opts := &natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1, // ephemeral
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("new embedded nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		srv.Shutdown()
		t.Fatalf("connect: %v", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		srv.Shutdown()
		t.Fatalf("jetstream: %v", err)
	}
	nmgr := &NatsManager{
		Microservice: &core.Microservice{InstanceId: "test", FunctionalArea: "area"},
		js:           js,
	}
	return nmgr, func() {
		nc.Close()
		srv.Shutdown()
	}
}

type cachedValue struct {
	Token string
	Count int
}

func TestCacheSetGetDelete(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	ctx := context.Background()

	c, err := nmgr.NewCache("devices", time.Minute)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	// Miss on an absent key returns (false, nil) — not an error.
	var got cachedValue
	found, err := c.Get(ctx, "tenant-a|dev-1", &got)
	if err != nil || found {
		t.Fatalf("expected clean miss, got found=%v err=%v", found, err)
	}

	// A key containing characters outside the NATS KV key charset ("|") must
	// round-trip thanks to base64url encoding.
	want := cachedValue{Token: "dev-1", Count: 7}
	if err := c.Set(ctx, "tenant-a|dev-1", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	found, err = c.Get(ctx, "tenant-a|dev-1", &got)
	if err != nil || !found {
		t.Fatalf("expected hit, got found=%v err=%v", found, err)
	}
	if got != want {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}

	// Delete evicts; a subsequent Get misses cleanly. A second Delete tolerates
	// the miss.
	if err := c.Delete(ctx, "tenant-a|dev-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	found, _ = c.Get(ctx, "tenant-a|dev-1", &got)
	if found {
		t.Fatal("expected miss after delete")
	}
	if err := c.Delete(ctx, "tenant-a|dev-1"); err != nil {
		t.Fatalf("Delete on missing key should tolerate miss: %v", err)
	}
}

func TestDistributedLockMutualExclusion(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	// TTL well above the contention window so the holder's lock cannot auto-expire
	// mid-test; mutual exclusion is then purely a function of the held key.
	lock, err := nmgr.NewDistributedLock(30 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLock: %v", err)
	}

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lock.WithLock(context.Background(), "area", func(context.Context) error {
			close(held)
			<-release // keep holding until the test says to let go
			return nil
		})
	}()
	<-held // the goroutine now holds the lock

	// A competing acquire while the lock is held must NOT run its logic. A short
	// deadline makes it give up fast (WithLock honors ctx.Done() during backoff).
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	ran := false
	if err := lock.WithLock(ctx, "area", func(context.Context) error {
		ran = true
		return nil
	}); err == nil {
		t.Fatal("expected the competing acquire to fail while the lock was held")
	}
	if ran {
		t.Fatal("competing critical section must not have run while the lock was held")
	}

	// Release the holder, then the lock can be acquired again and the logic runs.
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("holder WithLock: %v", err)
	}
	reran := false
	if err := lock.WithLock(context.Background(), "area", func(context.Context) error {
		reran = true
		return nil
	}); err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	if !reran {
		t.Fatal("expected logic to run after the lock was released")
	}
}

func TestDistributedLockReleasesOnError(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	ctx := context.Background()

	lock, err := nmgr.NewDistributedLock(5 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLock: %v", err)
	}

	// A critical section that returns an error must still release the lock, so a
	// subsequent acquire succeeds.
	if err := lock.WithLock(ctx, "area", func(context.Context) error {
		return context.Canceled
	}); err == nil {
		t.Fatal("expected the logic error to propagate")
	}

	ran := false
	if err := lock.WithLock(ctx, "area", func(context.Context) error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("acquire after error release: %v", err)
	}
	if !ran {
		t.Fatal("lock was not released after the guarded logic errored")
	}
}
