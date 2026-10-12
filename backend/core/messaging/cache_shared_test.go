// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
)

// frozenValue stands in for a type GetShared is for: built once, read through methods.
type frozenValue struct{ name string }

func (f *frozenValue) Name() string { return f.name }

// countingDecode decodes a typedValue's JSON into a frozenValue, counting its calls.
func countingDecode(calls *atomic.Int32) func([]byte) (*frozenValue, error) {
	return func(raw []byte) (*frozenValue, error) {
		calls.Add(1)
		var w typedValue
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, err
		}
		return &frozenValue{name: w.Name}, nil
	}
}

// TestGetSharedReturnsTheKeptValue: every read of an entry, by Set or by bucket fill, from
// the bucket path or from memory alone, is the one value decode built, and decode runs once.
func TestGetSharedReturnsTheKeptValue(t *testing.T) {
	ctx := context.Background()
	want := typedFixture()
	wantJSON, _ := json.Marshal(want)
	for _, via := range []string{"set", "bucket"} {
		t.Run(via, func(t *testing.T) {
			store := newCountingStore()
			c := NewCacheOver(store)
			if via == "set" {
				if err := c.Set(ctx, "t|k", want); err != nil {
					t.Fatal(err)
				}
			} else {
				store.seed("t|k", string(wantJSON))
			}
			var calls atomic.Int32
			decode := countingDecode(&calls)
			first, found, err := GetShared(ctx, c, "t|k", decode)
			if err != nil || !found || first.Name() != want.Name {
				t.Fatalf("first read = (%v, %v, %v)", first, found, err)
			}
			for i := 0; i < 3; i++ {
				got, found, err := GetShared(ctx, c, "t|k", decode)
				if err != nil || !found || got != first {
					t.Fatalf("read %d = (%p, %v, %v), want the kept %p", i, got, found, err, first)
				}
				mem, found, err := GetSharedFromMemory(c, "t|k", decode)
				if err != nil || !found || mem != first {
					t.Fatalf("memory read %d = (%p, %v, %v), want the kept %p", i, mem, found, err, first)
				}
			}
			if n := calls.Load(); n != 1 {
				t.Fatalf("decode ran %d times, want once per entry", n)
			}
		})
	}
}

// TestGetSharedFollowsInvalidation: a Set brings a NEW value, leaving the one an earlier
// reader holds as it was, and a Delete drops it.
func TestGetSharedFollowsInvalidation(t *testing.T) {
	ctx := context.Background()
	c := NewCacheOver(newCountingStore())
	var calls atomic.Int32
	decode := countingDecode(&calls)
	if err := c.Set(ctx, "t|k", typedFixture()); err != nil {
		t.Fatal(err)
	}
	old, _, err := GetShared(ctx, c, "t|k", decode)
	if err != nil {
		t.Fatal(err)
	}
	second := typedFixture()
	second.Name = "second"
	if err := c.Set(ctx, "t|k", second); err != nil {
		t.Fatal(err)
	}
	got, found, err := GetShared(ctx, c, "t|k", decode)
	if err != nil || !found || got.Name() != "second" || got == old {
		t.Fatalf("after Set = (%v, %v, %v)", got, found, err)
	}
	if old.Name() != typedFixture().Name {
		t.Fatalf("the value an earlier reader holds changed to %q", old.Name())
	}
	if err := c.Delete(ctx, "t|k"); err != nil {
		t.Fatal(err)
	}
	if got, found, err := GetShared(ctx, c, "t|k", decode); found || err != nil || got != nil {
		t.Fatalf("after Delete = (%v, %v, %v)", got, found, err)
	}
}

// TestGetSharedDecodeError returns decode's error, from the bucket and from memory, and
// keeps nothing.
func TestGetSharedDecodeError(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("refused")
	refuse := func([]byte) (*frozenValue, error) { return nil, boom }

	store := newCountingStore()
	store.seed("t|k", `{"Name":"x"}`)
	c := NewCacheOver(store)
	if got, found, err := GetShared(ctx, c, "t|k", refuse); !errors.Is(err, boom) || found || got != nil {
		t.Fatalf("bucket read = (%v, %v, %v), want the decode error", got, found, err)
	}

	c = NewCacheOver(newCountingStore())
	if err := c.Set(ctx, "t|k", typedFixture()); err != nil {
		t.Fatal(err)
	}
	if got, found, err := GetSharedFromMemory(c, "t|k", refuse); !errors.Is(err, boom) || found || got != nil {
		t.Fatalf("memory read = (%v, %v, %v), want the decode error", got, found, err)
	}
	var calls atomic.Int32
	if got, _, err := GetShared(ctx, c, "t|k", countingDecode(&calls)); err != nil || got.Name() != typedFixture().Name {
		t.Fatalf("a refused decode kept something: (%v, %v)", got, err)
	}
}
