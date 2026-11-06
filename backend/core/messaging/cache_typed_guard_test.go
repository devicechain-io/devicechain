// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
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

// TestDecodedChargeOfChargesByTheValue: a value the charger recognizes is charged what it says, not
// by the factor; one it does not recognize falls back; the byte cap still decides whether a value
// that is too large to hold at that charge is attached at all.
func TestDecodedChargeOfChargesByTheValue(t *testing.T) {
	ctx := context.Background()
	var seen []int
	c := NewCacheOver(newCountingStore(), WithDecodedCharge(4, 10), WithLocalBounds(100, 1<<20),
		WithDecodedChargeOf(func(v any, encodedLen int) (int, bool) {
			seen = append(seen, encodedLen)
			if _, ok := v.(*typedValue); ok {
				return 5000, true
			}
			return 0, false
		}))
	if err := c.Set(ctx, "t|k", typedFixture()); err != nil {
		t.Fatal(err)
	}
	_, before := c.local.len()
	if _, _, err := GetCloned(ctx, c, "t|k", cloneTypedValue); err != nil {
		t.Fatal(err)
	}
	_, after := c.local.len()
	if after-before != 5000 {
		t.Fatalf("attach charged %d B, want the 5000 the charger gave", after-before)
	}
	if len(seen) != 1 {
		t.Fatalf("the charger ran %d times for one attach", len(seen))
	}

	// Too large at the charged size: the entry is held undecoded rather than attached.
	small := NewCacheOver(newCountingStore(), WithLocalBounds(100, 3000),
		WithDecodedChargeOf(func(any, int) (int, bool) { return 5000, true }))
	if err := small.Set(ctx, "t|k", typedFixture()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := GetCloned(ctx, small, "t|k", cloneTypedValue); err != nil {
		t.Fatal(err)
	}
	if h, ok := small.local.getHit("t|k", small.now()); !ok || h.val != nil {
		t.Fatalf("a value charged past the cap was attached (hit %v, decoded %v)", ok, h.val != nil)
	}
}

// TestATenantOverItsShareEvictsItsOwnEntries: with a tenant share, a tenant that stores past it
// loses its own least recently used entries and never another tenant's; a value over the share on
// its own is not held.
func TestATenantOverItsShareEvictsItsOwnEntries(t *testing.T) {
	ctx := context.Background()
	c := NewCacheOver(newCountingStore(), WithLocalBounds(100, 40000), WithLocalTenantShare(25),
		WithDecodedChargeOf(func(any, int) (int, bool) { return 4000, true }))
	for _, k := range []string{"quiet|1", "quiet|2"} {
		if err := c.Set(ctx, k, typedFixture()); err != nil {
			t.Fatal(err)
		}
	}
	// The noisy tenant decodes ten entries of about 4 KB each: its share is 10,000 bytes.
	for i := 0; i < 10; i++ {
		k := fmt.Sprintf("noisy|%d", i)
		if err := c.Set(ctx, k, typedFixture()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := GetCloned(ctx, c, k, cloneTypedValue); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{"quiet|1", "quiet|2"} {
		if _, ok := c.local.getHit(k, c.now()); !ok {
			t.Fatalf("%s was evicted by another tenant's entries", k)
		}
	}
	held := 0
	for i := 0; i < 10; i++ {
		if _, ok := c.local.getHit(fmt.Sprintf("noisy|%d", i), c.now()); ok {
			held++
		}
	}
	if held == 0 || held >= 10 {
		t.Fatalf("the noisy tenant holds %d of its 10 entries; its share should hold some and not all", held)
	}
	if _, ok := c.local.getHit("noisy|9", c.now()); !ok {
		t.Fatal("the newest entry of the noisy tenant was evicted instead of its oldest")
	}
	c.local.mu.Lock()
	over := c.local.tenantBytes["noisy"] > c.local.tenantCap
	c.local.mu.Unlock()
	if over {
		t.Fatal("the tenant is over its share")
	}
}

// TestAFullTierMakesRoomFromTheLargestHolder: when the tier as a whole is full, the entry that
// goes is the largest holder's, not the least recently used of whoever happens to hold it. A
// tenant within its share keeps its entries while a larger holder has entries to give, which is
// what lets the documentation say that one tenant does not evict another's.
func TestAFullTierMakesRoomFromTheLargestHolder(t *testing.T) {
	ctx := context.Background()
	c := NewCacheOver(newCountingStore(), WithLocalBounds(100, 40000), WithLocalTenantShare(40),
		WithDecodedChargeOf(func(any, int) (int, bool) { return 7000, true }))
	// The quiet tenant's single entry is the oldest in the tier: plain least-recently-used
	// eviction would take it first.
	for _, k := range []string{"quiet|1", "big1|1", "big1|2", "big2|1", "big2|2", "late|1"} {
		if err := c.Set(ctx, k, typedFixture()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := GetCloned(ctx, c, k, cloneTypedValue); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{"quiet|1", "late|1"} {
		if h, ok := c.local.getHit(k, c.now()); !ok || h.val == nil {
			t.Fatalf("%s lost its decoded entry to a larger holder's store (held %v)", k, ok)
		}
	}
	lost := 0
	for _, k := range []string{"big1|1", "big1|2", "big2|1", "big2|2"} {
		if h, ok := c.local.getHit(k, c.now()); !ok || h.val == nil {
			lost++
		}
	}
	if lost != 1 {
		t.Fatalf("the larger holders lost %d decoded entries between them, want exactly 1", lost)
	}
	c.local.mu.Lock()
	over := c.local.bytes > c.local.maxBytes
	c.local.mu.Unlock()
	if over {
		t.Fatal("the tier is over its byte bound")
	}
}

// TestAFullTierNeverEvictsTheEntryJustStored: the tenant that is storing is never made to
// evict the entry it just stored, even when it is the largest holder.
func TestAFullTierNeverEvictsTheEntryJustStored(t *testing.T) {
	ctx := context.Background()
	c := NewCacheOver(newCountingStore(), WithLocalBounds(100, 20000), WithLocalTenantShare(100),
		WithDecodedChargeOf(func(any, int) (int, bool) { return 9000, true }))
	for _, k := range []string{"a|1", "b|1", "b|2"} {
		if err := c.Set(ctx, k, typedFixture()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := GetCloned(ctx, c, k, cloneTypedValue); err != nil {
			t.Fatal(err)
		}
	}
	if h, ok := c.local.getHit("b|2", c.now()); !ok || h.val == nil {
		t.Fatal("the entry just stored was evicted to make room for itself")
	}
}
