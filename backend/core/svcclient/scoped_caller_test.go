// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// 🔴 CONFUSED DEPUTY. A service token carries no scope, so a cross-service call made
// while serving a SCOPED member's request (ADR-081) would read the whole tenant on that
// member's behalf. Query refuses it — before minting, before dialling — and only the
// call that names the decision, QueryOnBehalfOfScopedCaller, proceeds.
func TestQueryRefusesToActForAScopedCallerUnlessToldItMay(t *testing.T) {
	var mints, hits int32
	mint := mintServer(t, "shh", &mints)
	defer mint.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	}))
	defer target.Close()
	c := clientFor(mint, "shh")

	scoped := auth.WithClaims(context.Background(), &auth.Claims{
		TokenType: auth.TokenTypeAccess, Tenant: "acme", Authorities: []string{"*"},
		ScopeAnchors: []string{"customer:acme-corp"},
	})
	var out map[string]any
	err := c.Query(scoped, target.URL, "acme", "query{x}", nil, &out)
	if !errors.Is(err, auth.ErrScopeUnsupported) {
		t.Fatalf("Query acted for a scoped caller under the service's identity: %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 || atomic.LoadInt32(&mints) != 0 {
		t.Fatalf("the refused call still reached the network: %d mint(s), %d call(s)", mints, hits)
	}

	// The deliberate door: the same caller, through the call that names the decision,
	// proceeds.
	if err := c.QueryOnBehalfOfScopedCaller(scoped, target.URL, "acme", "query{x}", nil, &out); err != nil {
		t.Fatalf("QueryOnBehalfOfScopedCaller was refused: %v", err)
	}
	// 🔴 And the decision does not outlive the call: the SAME context, handed to a plain
	// Query afterwards — the next call down the request, written by someone who never saw
	// the decision — is refused again. (When the permission was a context mark, it was
	// inherited by every call below the one that set it.)
	if err := c.Query(scoped, target.URL, "acme", "query{x}", nil, &out); !errors.Is(err, auth.ErrScopeUnsupported) {
		t.Fatalf("a plain Query after a deliberate scoped call was not refused: %v", err)
	}
	// Negative control: an unscoped caller, and a call with no caller at all (a
	// background consumer), proceed as they always have.
	unscoped := auth.WithClaims(context.Background(), &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme"})
	if err := c.Query(unscoped, target.URL, "acme", "query{x}", nil, &out); err != nil {
		t.Fatalf("an unscoped caller was refused: %v", err)
	}
	if err := c.Query(context.Background(), target.URL, "acme", "query{x}", nil, &out); err != nil {
		t.Fatalf("a call with no caller was refused: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("want 3 calls to reach the target, got %d", got)
	}
}
