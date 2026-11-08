// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"errors"
	"testing"
)

// WithLock hands fn a transaction-bound view: a failure inside fn rolls back every write it made,
// so a half-finished read-modify-write is never visible to another replica.
func TestWithLockRollsBackWhenFnFails(t *testing.T) {
	store := NewStore(newStoreDB(t), newTestKP(t))
	ctx := context.Background()
	ref := tenantRef("t1", "a")

	ok, err := WithLock(ctx, store, "t1", "k", func(v SecretStore) error {
		if err := v.Put(ctx, ref, []byte("x")); err != nil {
			return err
		}
		return errors.New("boom")
	})
	if !ok || err == nil {
		t.Fatalf("want ok and the fn error, got ok=%v err=%v", ok, err)
	}
	if exists, _ := store.Exists(ctx, ref); exists {
		t.Fatal("a write made inside a failed WithLock survived")
	}

	ok, err = WithLock(ctx, store, "t1", "k", func(v SecretStore) error { return v.Put(ctx, ref, []byte("x")) })
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if exists, _ := store.Exists(ctx, ref); !exists {
		t.Fatal("a committed WithLock write is missing")
	}
}

type plainStore struct{ SecretStore }

// A store that cannot lock is reported, not silently run unlocked.
func TestWithLockReportsAStoreThatCannotLock(t *testing.T) {
	ran := false
	ok, err := WithLock(context.Background(), plainStore{}, "t1", "k", func(SecretStore) error { ran = true; return nil })
	if ok || err != nil || ran {
		t.Fatalf("ok=%v err=%v ran=%v", ok, err, ran)
	}
}
