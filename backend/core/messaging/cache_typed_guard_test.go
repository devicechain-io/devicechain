// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
)

// TestGetClonedWithOtherTypeDecodesAfresh: a key whose kept value is one type, read as
// another, decodes the bytes afresh into the second and returns that, never the first's
// value, and does not disturb what is kept.
func TestGetClonedWithOtherTypeDecodesAfresh(t *testing.T) {
	ctx := context.Background()
	c := NewCacheOver(newCountingStore())
	if err := c.Set(ctx, "t|k", map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	cloneMap := func(m *map[string]string) *map[string]string {
		out := make(map[string]string, len(*m))
		for k, v := range *m {
			out[k] = v
		}
		return &out
	}
	if m, found, err := GetCloned(ctx, c, "t|k", cloneMap); !found || err != nil || (*m)["a"] != "b" {
		t.Fatal(m, found, err)
	}
	type other struct{ A string }
	cloneOther := func(o *other) *other { v := *o; return &v }
	// encoding/json matches field names without regard to case, so the key "a" fills A.
	o, found, err := GetCloned(ctx, c, "t|k", cloneOther)
	if !found || err != nil || o == nil || o.A != "b" {
		t.Fatalf("read as the other type = (%v, %v, %v), want {A:b}", o, found, err)
	}
	// And the kept map is still the map.
	if m, found, err := GetCloned(ctx, c, "t|k", cloneMap); !found || err != nil || (*m)["a"] != "b" {
		t.Fatal(m, found, err)
	}
}

// TestAttachRefusesAnEntryThatWasReplaced is the stale-read guard: a reader that hit an
// entry, lost the race to a Set that replaced it, and then attaches what it decoded from
// the OLD bytes must not put that on the NEW entry. The next read must see the new value.
func TestAttachRefusesAnEntryThatWasReplaced(t *testing.T) {
	ctx := context.Background()
	c := NewCacheOver(newCountingStore())
	old := typedFixture()
	if err := c.Set(ctx, "t|k", old); err != nil {
		t.Fatal(err)
	}
	hit, ok := c.local.getHit("t|k", c.now()) // the reader's hit on the old entry
	if !ok {
		t.Fatal("expected a hit")
	}
	replacement := typedFixture()
	replacement.Name = "replacement"
	if err := c.Set(ctx, "t|k", replacement); err != nil {
		t.Fatal(err)
	}
	stale := old
	c.local.attach("t|k", hit.entry, &stale) // the reader finishing late

	got, found, err := GetCloned(ctx, c, "t|k", cloneTypedValue)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if got.Name != "replacement" {
		t.Fatalf("read after a replaced entry was attached to = %q, want the new value", got.Name)
	}
}

// TestGetClonedFillAcrossAWriteIsNotKept: a bucket read that was in flight across a write
// on this replica returns what it read but keeps nothing in memory, decoded or not, so the
// value from before the write cannot be served after it.
func TestGetClonedFillAcrossAWriteIsNotKept(t *testing.T) {
	ctx := context.Background()
	store := newCountingStore()
	store.seed("t|k", `{"Name":"before"}`)
	c := NewCacheOver(store)
	store.beforeGet = func() { c.local.invalidate("t|k") } // a write lands as the read starts
	got, found, err := GetCloned(ctx, c, "t|k", cloneTypedValue)
	if err != nil || !found || got.Name != "before" {
		t.Fatalf("= (%v, %v, %v)", got, found, err)
	}
	if n, _ := c.local.len(); n != 0 {
		t.Fatalf("a read in flight across a write left %d entries in memory", n)
	}
	store.beforeGet = nil
	if _, found, _ := GetCloned(ctx, c, "t|k", cloneTypedValue); !found {
		t.Fatal("expected the bucket to answer")
	}
	if store.gets.Load() != 2 {
		t.Fatalf("bucket gets = %d, want 2: the first read filled memory", store.gets.Load())
	}
}

// TestAttachKeepsTheFirstValueAndChargesOnce: attaching to an entry that already has a
// decoded value neither replaces it nor charges the entry again.
func TestAttachKeepsTheFirstValueAndChargesOnce(t *testing.T) {
	ctx := context.Background()
	c := NewCacheOver(newCountingStore())
	if err := c.Set(ctx, "t|k", typedFixture()); err != nil {
		t.Fatal(err)
	}
	hit, _ := c.local.getHit("t|k", c.now())
	first, second := typedFixture(), typedFixture()
	second.Name = "second"
	c.local.attach("t|k", hit.entry, &first)
	_, charged := c.local.len()
	c.local.attach("t|k", hit.entry, &second)
	if _, again := c.local.len(); again != charged {
		t.Fatalf("a second attach charged again: %d -> %d", charged, again)
	}
	if got, _, _ := GetCloned(ctx, c, "t|k", cloneTypedValue); got.Name != typedFixture().Name {
		t.Fatalf("a second attach replaced the kept value: %q", got.Name)
	}
}

// capCacheOf sets n keys in a cache whose byte cap is one byte less than those entries plus
// one decoded value's charge, so the first attach overshoots the cap by exactly one byte.
func capCacheOf(t *testing.T, n int) (*Cache, []string) {
	t.Helper()
	ctx := context.Background()
	probe := NewCacheOver(newCountingStore())
	keys := make([]string, n)
	for i := range keys {
		keys[i] = "t|k" + string(rune('a'+i))
		if err := probe.Set(ctx, keys[i], typedFixture()); err != nil {
			t.Fatal(err)
		}
	}
	_, total := probe.local.len()
	hit, _ := probe.local.getHit(keys[0], probe.now())
	extra := len(hit.data)
	c := NewCacheOver(newCountingStore(), WithLocalBounds(100, total+extra-1))
	for _, k := range keys {
		if err := c.Set(ctx, k, typedFixture()); err != nil {
			t.Fatal(err)
		}
	}
	if held, _ := c.local.len(); held != len(keys) {
		t.Fatalf("setup: %d of %d entries held before attaching", held, len(keys))
	}
	return c, keys
}

// TestAttachEvictsBackToTheCap: attaching a decoded value that takes the tier past its byte
// cap evicts least recently used entries until it is back under it.
func TestAttachEvictsBackToTheCap(t *testing.T) {
	ctx := context.Background()
	c, keys := capCacheOf(t, 3)
	newest := keys[len(keys)-1]
	if _, found, err := GetCloned(ctx, c, newest, cloneTypedValue); !found || err != nil {
		t.Fatal(found, err)
	}
	entries, bytes := c.local.len()
	if bytes > c.local.maxBytes {
		t.Fatalf("after attaching the tier holds %d B, over its %d B cap", bytes, c.local.maxBytes)
	}
	if entries != 2 {
		t.Fatalf("entries = %d, want 2: the least recently used should have gone", entries)
	}
	if h, ok := c.local.getHit(newest, c.now()); !ok || h.val == nil {
		t.Fatal("the entry attached to was evicted or lost its value")
	}
	if _, ok := c.local.getHit(keys[0], c.now()); ok {
		t.Fatal("the least recently used entry survived")
	}
}

// TestAttachToTheLeastRecentlyUsedEntryEvictsAnother: when the entry attached to is itself
// at the least recently used end, the cap is still restored, by evicting its neighbour.
func TestAttachToTheLeastRecentlyUsedEntryEvictsAnother(t *testing.T) {
	c, keys := capCacheOf(t, 3)
	target := keys[0] // the oldest Set, so already at the back
	hit, ok := c.local.getHit(target, c.now())
	if !ok {
		t.Fatal("expected a hit")
	}
	// Other readers touch the rest, so the target is back at the LRU end when it attaches.
	for _, k := range keys[1:] {
		c.local.peekHit(k, c.now())
	}
	v := typedFixture()
	c.local.attach(target, hit.entry, &v)
	_, bytes := c.local.len()
	if bytes > c.local.maxBytes {
		t.Fatalf("the tier holds %d B, over its %d B cap", bytes, c.local.maxBytes)
	}
	if h, ok := c.local.getHit(target, c.now()); !ok || h.val == nil {
		t.Fatal("the entry attached to was evicted")
	}
}

// TestDecodedChargeIsAppliedToTheByteCap: WithDecodedCharge scales what attaching charges.
func TestDecodedChargeIsAppliedToTheByteCap(t *testing.T) {
	ctx := context.Background()
	c := NewCacheOver(newCountingStore(), WithDecodedCharge(4, 10))
	if err := c.Set(ctx, "t|k", typedFixture()); err != nil {
		t.Fatal(err)
	}
	hit, _ := c.local.getHit("t|k", c.now())
	_, before := c.local.len()
	if _, _, err := GetCloned(ctx, c, "t|k", cloneTypedValue); err != nil {
		t.Fatal(err)
	}
	_, after := c.local.len()
	if want := len(hit.data)*4 + 10; after-before != want {
		t.Fatalf("attach charged %d B, want %d", after-before, want)
	}
}
