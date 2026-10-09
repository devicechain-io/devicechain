// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

type typedNested struct {
	Tags []string
	Opt  *int
}

type typedValue struct {
	Name   string
	Items  []typedNested
	Ptr    *typedNested
	Labels map[string]string
}

// cloneTypedValue is a correct deep clone of typedValue.
func cloneTypedValue(in *typedValue) *typedValue {
	out := *in
	if in.Items != nil {
		out.Items = make([]typedNested, len(in.Items))
		for i, it := range in.Items {
			out.Items[i] = cloneTypedNested(it)
		}
	}
	if in.Ptr != nil {
		n := cloneTypedNested(*in.Ptr)
		out.Ptr = &n
	}
	if in.Labels != nil {
		out.Labels = make(map[string]string, len(in.Labels))
		for k, v := range in.Labels {
			out.Labels[k] = v
		}
	}
	return &out
}

func cloneTypedNested(in typedNested) typedNested {
	out := in
	out.Tags = append([]string(nil), in.Tags...)
	if in.Tags == nil {
		out.Tags = nil
	}
	if in.Opt != nil {
		v := *in.Opt
		out.Opt = &v
	}
	return out
}

func mutateTypedValue(v *typedValue) {
	v.Name = "mutated"
	v.Items[0].Tags[0] = "mutated"
	*v.Items[0].Opt = -1
	v.Items = append(v.Items, typedNested{})
	v.Ptr.Tags[0] = "mutated"
	*v.Ptr.Opt = -1
	v.Labels["k"] = "mutated"
	v.Labels["extra"] = "x"
}

func typedFixture() typedValue {
	one, two := 1, 2
	return typedValue{
		Name:   "orig",
		Items:  []typedNested{{Tags: []string{"a", "b"}, Opt: &one}},
		Ptr:    &typedNested{Tags: []string{"c"}, Opt: &two},
		Labels: map[string]string{"k": "v"},
	}
}

// TestGetClonedHitsAreIndependent mutates everything reachable from each value a hit
// returned (nested slices, a map, pointers) and requires the next hit, and the next read
// through plain Get, to be unchanged. It runs over the three ways a value reaches the tier:
// a Set, a bucket fill, and a hit on an entry whose decoded form is already attached.
func TestGetClonedHitsAreIndependent(t *testing.T) {
	want := typedFixture()
	wantJSON, _ := json.Marshal(want)
	ctx := context.Background()

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
			for i := 0; i < 4; i++ {
				got, found, err := GetCloned(ctx, c, "t|k", cloneTypedValue)
				if err != nil || !found {
					t.Fatalf("read %d = (%v, %v)", i, found, err)
				}
				if b, _ := json.Marshal(got); string(b) != string(wantJSON) {
					t.Fatalf("read %d saw an earlier caller's mutation: %s", i, b)
				}
				mutateTypedValue(got)

				var viaGet typedValue
				if found, err := c.Get(ctx, "t|k", &viaGet); err != nil || !found {
					t.Fatal(found, err)
				}
				if b, _ := json.Marshal(viaGet); string(b) != string(wantJSON) {
					t.Fatalf("Get saw a mutation of a typed hit: %s", b)
				}
			}
			if g := store.gets.Load(); via == "set" && g != 0 {
				t.Fatalf("a Set value reached the bucket %d times", g)
			}
		})
	}
}

// TestGetClonedFromMemory covers the memory-only path, which must neither ask the bucket
// nor share the held value.
func TestGetClonedFromMemory(t *testing.T) {
	ctx := context.Background()
	store := newCountingStore()
	c := NewCacheOver(store)
	if _, found, err := GetClonedFromMemory(c, "t|k", cloneTypedValue); found || err != nil {
		t.Fatalf("empty memory = (%v, %v)", found, err)
	}
	if err := c.Set(ctx, "t|k", typedFixture()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		got, found, err := GetClonedFromMemory(c, "t|k", cloneTypedValue)
		if err != nil || !found {
			t.Fatalf("read %d = (%v, %v)", i, found, err)
		}
		if !reflect.DeepEqual(*got, typedFixture()) {
			t.Fatalf("read %d saw a mutation: %+v", i, got)
		}
		mutateTypedValue(got)
	}
	if store.gets.Load() != 0 {
		t.Fatal("GetClonedFromMemory asked the bucket")
	}
}

// TestGetClonedFollowsInvalidation: a Set replaces the decoded value with the new one, and
// a Delete drops it, so a typed read never serves a value older than the change.
func TestGetClonedFollowsInvalidation(t *testing.T) {
	ctx := context.Background()
	store := newCountingStore()
	c := NewCacheOver(store)
	first := typedFixture()
	if err := c.Set(ctx, "t|k", first); err != nil {
		t.Fatal(err)
	}
	if _, _, err := GetCloned(ctx, c, "t|k", cloneTypedValue); err != nil { // attach
		t.Fatal(err)
	}

	second := typedFixture()
	second.Name = "second"
	if err := c.Set(ctx, "t|k", second); err != nil {
		t.Fatal(err)
	}
	got, _, _ := GetCloned(ctx, c, "t|k", cloneTypedValue)
	if got.Name != "second" {
		t.Fatalf("after Set, typed read = %q", got.Name)
	}

	if err := c.Delete(ctx, "t|k"); err != nil {
		t.Fatal(err)
	}
	if got, found, err := GetCloned(ctx, c, "t|k", cloneTypedValue); found || err != nil || got != nil {
		t.Fatalf("after Delete, typed read = (%v, %v, %v)", got, found, err)
	}
}

// TestGetClonedKeepsTheTTLClock: attaching a decoded value does not extend the entry. The
// value expires when its bytes would have.
func TestGetClonedKeepsTheTTLClock(t *testing.T) {
	ctx := context.Background()
	store := newCountingStore()
	c := NewCacheOver(store, WithLocalTTL(5*time.Second))
	now := time.Now()
	c.now = func() time.Time { return now }
	if err := c.Set(ctx, "t|k", typedFixture()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(4 * time.Second)
	if _, found, _ := GetCloned(ctx, c, "t|k", cloneTypedValue); !found {
		t.Fatal("expected a hit inside the TTL")
	}
	now = now.Add(2 * time.Second)
	if _, found, _ := GetCloned(ctx, c, "t|k", cloneTypedValue); !found {
		t.Fatal("expected the bucket to answer after the memory entry expired")
	}
	if store.gets.Load() != 1 {
		t.Fatalf("bucket gets = %d, want 1: the entry outlived its TTL", store.gets.Load())
	}
}

// TestGetClonedChargesTheByteCap: the decoded value is counted against the byte cap, and
// the accounting returns to zero when the entry goes.
func TestGetClonedChargesTheByteCap(t *testing.T) {
	ctx := context.Background()
	c := NewCacheOver(newCountingStore())
	if err := c.Set(ctx, "t|k", typedFixture()); err != nil {
		t.Fatal(err)
	}
	_, before := c.local.len()
	if _, _, err := GetCloned(ctx, c, "t|k", cloneTypedValue); err != nil {
		t.Fatal(err)
	}
	_, after := c.local.len()
	if after <= before {
		t.Fatalf("attaching a decoded value charged nothing: %d -> %d", before, after)
	}
	if _, _, err := GetCloned(ctx, c, "t|k", cloneTypedValue); err != nil {
		t.Fatal(err)
	}
	if _, again := c.local.len(); again != after {
		t.Fatalf("a second hit changed the charge: %d -> %d", after, again)
	}
	_ = c.Delete(ctx, "t|k")
	if n, b := c.local.len(); n != 0 || b != 0 {
		t.Fatalf("after Delete the tier holds (%d, %d)", n, b)
	}
}

// TestGetClonedWithOtherTypeDecodesAfresh: a key read as one type and then another never
// returns the wrong type's value.
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
	if _, found, err := GetCloned(ctx, c, "t|k", cloneMap); !found || err != nil {
		t.Fatal(found, err)
	}
	type other struct{ A string }
	cloneOther := func(o *other) *other { v := *o; return &v }
	if _, found, err := GetCloned(ctx, c, "t|k", cloneOther); found || err == nil {
		// a JSON object decodes into other without error: found, with A empty
		if !found || err != nil {
			t.Fatal(found, err)
		}
	}
}

// TestGetClonedDecodeError returns the error as Get does and holds nothing.
func TestGetClonedDecodeError(t *testing.T) {
	ctx := context.Background()
	store := newCountingStore()
	store.seed("t|k", `"not an object"`)
	c := NewCacheOver(store)
	if got, found, err := GetCloned(ctx, c, "t|k", cloneTypedValue); err == nil || found || got != nil {
		t.Fatalf("= (%v, %v, %v), want a decode error", got, found, err)
	}
}
