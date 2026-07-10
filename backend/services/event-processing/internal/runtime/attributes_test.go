// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import "testing"

// TestFlattenServerOverShared pins the SERVER-over-SHARED precedence the whole feature leans on: a
// server-scoped value shadows a shared-scoped one for the same key, while a key present in only one
// scope carries through. Row order must not matter (LoadAll/LoadDevice return rows unordered).
func TestFlattenServerOverShared(t *testing.T) {
	cases := []struct {
		name string
		rows []AttrEntry
	}{
		{"shared then server", []AttrEntry{
			{Scope: AttrScopeShared, Key: "lim", Value: 100},
			{Scope: AttrScopeServer, Key: "lim", Value: 50},
			{Scope: AttrScopeShared, Key: "onlyShared", Value: 7},
			{Scope: AttrScopeServer, Key: "onlyServer", Value: 9},
		}},
		{"server then shared (order flipped)", []AttrEntry{
			{Scope: AttrScopeServer, Key: "onlyServer", Value: 9},
			{Scope: AttrScopeServer, Key: "lim", Value: 50},
			{Scope: AttrScopeShared, Key: "onlyShared", Value: 7},
			{Scope: AttrScopeShared, Key: "lim", Value: 100},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := flatten(tc.rows)
			if m["lim"] != 50 {
				t.Errorf("server value must win for a shared+server key: got %v, want 50", m["lim"])
			}
			if m["onlyShared"] != 7 {
				t.Errorf("shared-only key must carry: got %v, want 7", m["onlyShared"])
			}
			if m["onlyServer"] != 9 {
				t.Errorf("server-only key must carry: got %v, want 9", m["onlyServer"])
			}
		})
	}
}

// TestFlattenIgnoresUnknownScope proves a scope outside the whitelisted vocabulary (which cannot
// reach the projection, but defense-in-depth) is not weighed — it neither seeds a key nor shadows a
// real value.
func TestFlattenIgnoresUnknownScope(t *testing.T) {
	m := flatten([]AttrEntry{
		{Scope: "CLIENT", Key: "lim", Value: 999},
		{Scope: AttrScopeShared, Key: "lim", Value: 100},
	})
	if m["lim"] != 100 {
		t.Fatalf("an unknown-scope row must be ignored: got %v, want 100 (the shared value)", m["lim"])
	}
}

// TestViewReconcileGroupsByDevice proves Reconcile buckets a flat cross-device row set into the
// right per-device flattened maps, scoping by (tenant, device) so a token reused across tenants is
// not conflated.
func TestViewReconcileGroupsByDevice(t *testing.T) {
	v := NewDeviceAttributeView()
	v.Reconcile([]AttrEntry{
		{Tenant: "acme", DeviceToken: "d1", Scope: AttrScopeShared, Key: "lim", Value: 100},
		{Tenant: "acme", DeviceToken: "d1", Scope: AttrScopeServer, Key: "lim", Value: 50},
		{Tenant: "acme", DeviceToken: "d2", Scope: AttrScopeServer, Key: "lim", Value: 30},
		{Tenant: "other", DeviceToken: "d1", Scope: AttrScopeServer, Key: "lim", Value: 7}, // same token, other tenant
	})
	if got := v.For("acme", "d1")["lim"]; got != 50 {
		t.Errorf("acme/d1 lim = %v, want 50 (server wins)", got)
	}
	if got := v.For("acme", "d2")["lim"]; got != 30 {
		t.Errorf("acme/d2 lim = %v, want 30", got)
	}
	if got := v.For("other", "d1")["lim"]; got != 7 {
		t.Errorf("other/d1 lim = %v, want 7 (tenant-scoped, not conflated with acme/d1)", got)
	}
	if m := v.For("acme", "nope"); m != nil {
		t.Errorf("unknown device must return nil, got %v", m)
	}
}

// TestViewReplaceDevice proves a recheck REPLACES a device's whole entry rather than merging: a key
// dropped from the new row set disappears, a changed value updates, and an empty row set (every
// attribute removed / device purged) drops the entry entirely.
func TestViewReplaceDevice(t *testing.T) {
	v := NewDeviceAttributeView()
	v.ReplaceDevice("acme", "d1", []AttrEntry{
		{Tenant: "acme", DeviceToken: "d1", Scope: AttrScopeServer, Key: "lim", Value: 50},
		{Tenant: "acme", DeviceToken: "d1", Scope: AttrScopeShared, Key: "warn", Value: 40},
	})
	if got := v.For("acme", "d1"); got["lim"] != 50 || got["warn"] != 40 {
		t.Fatalf("initial replace = %v, want lim=50 warn=40", got)
	}
	// Recheck re-reads a set with "warn" gone and "lim" changed: the old "warn" must not linger.
	v.ReplaceDevice("acme", "d1", []AttrEntry{
		{Tenant: "acme", DeviceToken: "d1", Scope: AttrScopeServer, Key: "lim", Value: 60},
	})
	got := v.For("acme", "d1")
	if got["lim"] != 60 {
		t.Errorf("replaced lim = %v, want 60", got["lim"])
	}
	if _, ok := got["warn"]; ok {
		t.Errorf("dropped key must not survive a replace: %v", got)
	}
	// An empty set (all removed / purged) drops the entry.
	v.ReplaceDevice("acme", "d1", nil)
	if m := v.For("acme", "d1"); m != nil {
		t.Errorf("empty replace must drop the entry, got %v", m)
	}
}

// TestViewReplaceDeviceSwapsMap proves ReplaceDevice swaps in a fresh map rather than mutating the
// prior one, so a reference For handed to an in-flight fan-out is not changed underfoot.
func TestViewReplaceDeviceSwapsMap(t *testing.T) {
	v := NewDeviceAttributeView()
	v.ReplaceDevice("acme", "d1", []AttrEntry{{Tenant: "acme", DeviceToken: "d1", Scope: AttrScopeServer, Key: "lim", Value: 50}})
	held := v.For("acme", "d1")
	v.ReplaceDevice("acme", "d1", []AttrEntry{{Tenant: "acme", DeviceToken: "d1", Scope: AttrScopeServer, Key: "lim", Value: 60}})
	if held["lim"] != 50 {
		t.Fatalf("a previously-returned map must not be mutated by a later replace: got %v, want 50", held["lim"])
	}
}
