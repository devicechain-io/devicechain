// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets"
)

// TestResolveCachesWithinTTL resolves once from the store, then serves the cache until the TTL
// expires, then re-resolves.
func TestResolveCachesWithinTTL(t *testing.T) {
	store := &fakeSecretStore{values: map[string][]byte{"webhook/auth": []byte("tok-123")}}
	r := NewSecretResolver(store)
	now := time.Unix(1_000_000, 0)
	r.now = func() time.Time { return now }

	v, err := r.Resolve(context.Background(), "acme", "webhook/auth")
	if err != nil || v != "tok-123" {
		t.Fatalf("first resolve = (%q,%v), want (tok-123,nil)", v, err)
	}
	// A second resolve within the TTL is served from cache (no new store call).
	if _, err := r.Resolve(context.Background(), "acme", "webhook/auth"); err != nil {
		t.Fatalf("cached resolve: %v", err)
	}
	if store.resolveCalls() != 1 {
		t.Fatalf("expected 1 store resolve (cache hit), got %d", store.resolveCalls())
	}
	// After the TTL the cache entry is stale and the store is hit again.
	now = now.Add(defaultSecretCacheTTL + time.Second)
	if _, err := r.Resolve(context.Background(), "acme", "webhook/auth"); err != nil {
		t.Fatalf("post-TTL resolve: %v", err)
	}
	if store.resolveCalls() != 2 {
		t.Fatalf("expected a re-resolve after TTL, got %d store calls", store.resolveCalls())
	}
}

// TestResolvePerTenantKeying keeps two tenants' identically-named handles distinct.
func TestResolvePerTenantKeying(t *testing.T) {
	store := &fakeSecretStore{values: map[string][]byte{"webhook/auth": []byte("shared")}}
	r := NewSecretResolver(store)
	// Both tenants share the handle NAME in this fake store, but the resolver keys the cache by
	// (tenant, handle), so each tenant's resolve is a distinct cache entry and a distinct store call.
	if _, err := r.Resolve(context.Background(), "acme", "webhook/auth"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(context.Background(), "globex", "webhook/auth"); err != nil {
		t.Fatal(err)
	}
	if store.resolveCalls() != 2 {
		t.Fatalf("expected a separate store resolve per tenant, got %d", store.resolveCalls())
	}
}

// TestResolveNotFoundPropagates surfaces ErrSecretNotFound (the caller fails closed) and does not
// cache the miss.
func TestResolveNotFoundPropagates(t *testing.T) {
	store := &fakeSecretStore{values: map[string][]byte{}}
	r := NewSecretResolver(store)
	_, err := r.Resolve(context.Background(), "acme", "missing")
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Fatalf("want ErrSecretNotFound, got %v", err)
	}
	// A miss is not cached; a later Put would be seen on the next resolve.
	if _, err := r.Resolve(context.Background(), "acme", "missing"); err == nil {
		t.Fatal("want a second error, got nil")
	}
	if store.resolveCalls() != 2 {
		t.Fatalf("a miss must not be cached; want 2 store calls, got %d", store.resolveCalls())
	}
}

// TestResolveRejectsBadRef fails closed on a malformed ref before touching the store.
func TestResolveRejectsBadRef(t *testing.T) {
	store := &fakeSecretStore{}
	r := NewSecretResolver(store)
	if _, err := r.Resolve(context.Background(), "", "webhook/auth"); err == nil {
		t.Fatal("empty tenant should be rejected (tenant-scoped ref requires a tenant)")
	}
	if store.resolveCalls() != 0 {
		t.Fatalf("a malformed ref must not reach the store, got %d calls", store.resolveCalls())
	}
}
