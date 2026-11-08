// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package webhooksig

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockingStore is a memStore that is also a secrets.Locker: it counts the lock acquisitions and
// rolls the map back when fn fails, as the database store does.
type lockingStore struct {
	*memStore
	locks atomic.Int32
}

func (s *lockingStore) WithLock(_ context.Context, _, _ string, fn func(secrets.SecretStore) error) error {
	s.locks.Add(1)
	s.mu.Lock()
	snap := make(map[secrets.SecretRef][]byte, len(s.m))
	for k, v := range s.m {
		snap[k] = v
	}
	s.mu.Unlock()
	if err := fn(s.memStore); err != nil {
		s.mu.Lock()
		s.m = snap
		s.mu.Unlock()
		return err
	}
	return nil
}

// Every read-modify-write on the slots runs under the store's cross-replica lock, not only Rotate.
func TestRotateEndRotationAndDeleteTakeTheStoreLock(t *testing.T) {
	ctx := context.Background()
	st := &lockingStore{memStore: newMemStore()}
	kr := Keyring{Store: st}
	_, err := kr.Create(ctx, "t1", "h")
	require.NoError(t, err)

	_, err = kr.Rotate(ctx, "t1", "h")
	require.NoError(t, err)
	assert.EqualValues(t, 1, st.locks.Load(), "Rotate")
	require.NoError(t, kr.EndRotation(ctx, "t1", "h"))
	assert.EqualValues(t, 2, st.locks.Load(), "EndRotation")
	require.NoError(t, kr.Delete(ctx, "t1", "h"))
	assert.EqualValues(t, 3, st.locks.Load(), "Delete")
}

// A rotation that fails part-way leaves no half-rotated pair behind.
func TestFailedRotationRollsBack(t *testing.T) {
	ctx := context.Background()
	st := &lockingStore{memStore: newMemStore()}
	first, err := Keyring{Store: st}.Create(ctx, "t1", "h")
	require.NoError(t, err)

	failing := Keyring{Store: &failCurrentPut{lockingStore: st}}
	_, err = failing.Rotate(ctx, "t1", "h")
	require.Error(t, err)

	keys, err := ResolveKeys(ctx, "h", st.resolver("t1"))
	require.NoError(t, err)
	assert.Equal(t, []string{first}, keys, "the old key is still the only key; previous was not left behind")
}

// failCurrentPut fails the write that installs the new current key, after previous was written.
type failCurrentPut struct{ *lockingStore }

func (f *failCurrentPut) WithLock(ctx context.Context, tn, key string, fn func(secrets.SecretStore) error) error {
	return f.lockingStore.WithLock(ctx, tn, key, func(inner secrets.SecretStore) error {
		return fn(failingView{inner})
	})
}

type failingView struct{ secrets.SecretStore }

func (v failingView) Put(ctx context.Context, r secrets.SecretRef, b []byte) error {
	if len(r.Name) >= len(slotCurrent) && r.Name[len(r.Name)-len(slotCurrent):] == slotCurrent {
		return errors.New("injected")
	}
	return v.SecretStore.Put(ctx, r, b)
}
