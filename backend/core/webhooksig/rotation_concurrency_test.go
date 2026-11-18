// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package webhooksig

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/stretchr/testify/require"
)

// Separate handles share a transaction mutex, unlike lockingStore's counting
// fake. This is a unit control, not a substitute for the Postgres test.
type serialRotationStore struct {
	secrets.SecretStore
	mu *sync.Mutex
}

func (s serialRotationStore) CreateIfAbsent(ctx context.Context, ref secrets.SecretRef, value []byte) (bool, error) {
	return secrets.CreateIfAbsent(ctx, s.SecretStore, ref, value)
}

func (s serialRotationStore) WithLock(_ context.Context, _, _ string, fn func(secrets.SecretStore) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(s.SecretStore)
}

type observedRotationStore struct {
	secrets.SecretStore
	attempted chan struct{}
	entered   chan struct{}
	parked    chan struct{}
	release   chan struct{}
}

func (s observedRotationStore) WithLock(ctx context.Context, tenant, key string, fn func(secrets.SecretStore) error) error {
	if s.attempted != nil {
		close(s.attempted)
	}
	return s.SecretStore.(secrets.Locker).WithLock(ctx, tenant, key, func(view secrets.SecretStore) error {
		if s.entered != nil {
			close(s.entered)
		}
		if s.parked != nil {
			view = parkedPrevious{view, s.parked, s.release}
		}
		return fn(view)
	})
}

type parkedPrevious struct {
	secrets.SecretStore
	parked  chan struct{}
	release chan struct{}
}

func (s parkedPrevious) Put(ctx context.Context, ref secrets.SecretRef, value []byte) error {
	if err := s.SecretStore.Put(ctx, ref, value); err != nil {
		return err
	}
	if ref.Name == "h/previous" {
		close(s.parked)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

type rotationResult struct {
	key string
	err error
}

// Park Rotate between the slot writes. Removing its sync.Map entry gives the
// second Keyring a DIFFERENT process mutex, modelling two replicas without a
// production seam. Neither this test nor its callers may run in parallel.
func rotationSchedule(t *testing.T, stores [2]secrets.SecretStore, end, wantOverlap bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := (Keyring{Store: stores[0]}).Create(ctx, "t1", "h")
	require.NoError(t, err)
	parked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unpark := func() { once.Do(func() { close(release) }) }
	defer unpark()
	attempted, entered := make(chan struct{}), make(chan struct{})
	a := Keyring{Store: observedRotationStore{SecretStore: stores[0], parked: parked, release: release}}
	b := Keyring{Store: observedRotationStore{SecretStore: stores[1], attempted: attempted, entered: entered}}
	doneA, doneB := make(chan rotationResult, 1), make(chan rotationResult, 1)
	go func() { key, err := a.Rotate(ctx, "t1", "h"); doneA <- rotationResult{key, err} }()
	select {
	case <-parked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	rotating.Delete("t1\nh")
	t.Cleanup(func() { rotating.Delete("t1\nh") })
	go func() {
		if end {
			doneB <- rotationResult{err: b.EndRotation(ctx, "t1", "h")}
		} else {
			key, err := b.Rotate(ctx, "t1", "h")
			doneB <- rotationResult{key, err}
		}
	}()
	select {
	case <-attempted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	overlap := false
	select {
	case <-entered:
		overlap = true
	case <-time.After(150 * time.Millisecond):
	}
	var rb rotationResult
	if overlap {
		select {
		case rb = <-doneB:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	unpark()
	var ra rotationResult
	select {
	case ra = <-doneA:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !overlap {
		select {
		case rb = <-doneB:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	require.NoError(t, ra.err)
	require.NoError(t, rb.err)
	require.Equal(t, wantOverlap, overlap, "second callback entered while first rotation was half-written")
	keys, err := ResolveKeys(ctx, "h", func(ctx context.Context, name string) (string, error) {
		value, err := stores[0].Resolve(ctx, secrets.SecretRef{Scope: secrets.ScopeTenant, Tenant: "t1", Name: name})
		return string(value), err
	})
	require.NoError(t, err)
	if !wantOverlap {
		if end {
			require.Equal(t, []string{ra.key}, keys)
		} else {
			require.Equal(t, []string{rb.key, ra.key}, keys, "previous must be the key returned by the preceding Rotate")
		}
	} else if !end {
		require.NotContains(t, keys, rb.key, "counting fake must demonstrate a lost receiver-held key")
		require.Equal(t, []string{ra.key, first}, keys)
	}
}

func TestRotationExcludesOtherReplicasAndCountingFakeDoesNot(t *testing.T) {
	for _, end := range []bool{false, true} {
		t.Run(map[bool]string{false: "rotate", true: "end"}[end], func(t *testing.T) {
			t.Run("serialized", func(t *testing.T) {
				st, mu := newMemStore(), &sync.Mutex{}
				rotationSchedule(t, [2]secrets.SecretStore{serialRotationStore{st, mu}, serialRotationStore{st, mu}}, end, false)
			})
			t.Run("counting-fake-negative-control", func(t *testing.T) {
				st := &lockingStore{memStore: newMemStore()}
				rotationSchedule(t, [2]secrets.SecretStore{st, st}, end, true)
			})
		})
	}
}
