// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
)

// mintServer stands in for user-management's mint endpoint: it verifies the shared
// secret and returns a canned token, counting how many times it is hit so a test
// can assert the client caches.
func mintServer(t *testing.T, secret string, mints *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != auth.ServiceTokenPath {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get(auth.ServiceSecretHeader) != secret {
			http.Error(w, "bad secret", http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(mints, 1)
		var req auth.ServiceTokenRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{
			Token:     "minted-for-" + req.Subject,
			ExpiresAt: 1 << 40, // far future so the cache holds across calls
		})
	}))
}

// clientFor builds a Client whose mint endpoint points at srv.
func clientFor(srv *httptest.Server, secret string) *Client {
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port)}, secret, "command-delivery", []string{string(auth.DeviceRead)})
}

func TestQuery_MintsThenCachesAndPassesAuth(t *testing.T) {
	var mints int32
	mint := mintServer(t, "shh", &mints)
	defer mint.Close()

	var gotAuth, gotTenant string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTenant = r.Header.Get(auth.ServiceTenantHeader)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"device": map[string]any{"token": "dev-1"}}})
	}))
	defer target.Close()

	c := clientFor(mint, "shh")
	var out struct {
		Device *struct{ Token string } `json:"device"`
	}
	if err := c.Query(context.Background(), target.URL, "tenant-a", "query{device{token}}", nil, &out); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if out.Device == nil || out.Device.Token != "dev-1" {
		t.Fatalf("unexpected data: %+v", out)
	}
	if gotAuth != "Bearer minted-for-command-delivery" {
		t.Fatalf("target did not receive the minted bearer: %q", gotAuth)
	}
	if gotTenant != "tenant-a" {
		t.Fatalf("target did not receive the tenant header: %q", gotTenant)
	}

	// A second call reuses the cached token — the mint endpoint is not hit again.
	if err := c.Query(context.Background(), target.URL, "tenant-a", "query{device{token}}", nil, &out); err != nil {
		t.Fatalf("second Query: %v", err)
	}
	if n := atomic.LoadInt32(&mints); n != 1 {
		t.Fatalf("expected exactly one mint (cached thereafter), got %d", n)
	}
}

func TestQuery_FailsClosedWithoutSecret(t *testing.T) {
	var mints int32
	mint := mintServer(t, "shh", &mints)
	defer mint.Close()

	c := clientFor(mint, "") // no configured secret
	err := c.Query(context.Background(), mint.URL, "tenant-a", "query{x}", nil, nil)
	if err == nil {
		t.Fatal("Query succeeded with no service secret configured")
	}
	if atomic.LoadInt32(&mints) != 0 {
		t.Fatal("minting was attempted despite an empty secret")
	}
}

func TestQuery_MissingTenantRejected(t *testing.T) {
	var mints int32
	mint := mintServer(t, "shh", &mints)
	defer mint.Close()
	c := clientFor(mint, "shh")
	if err := c.Query(context.Background(), mint.URL, "  ", "query{x}", nil, nil); err == nil {
		t.Fatal("Query accepted an empty tenant")
	}
}

func TestQuery_SurfacesGraphQLErrors(t *testing.T) {
	var mints int32
	mint := mintServer(t, "shh", &mints)
	defer mint.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{"message": "unauthorized"}}})
	}))
	defer target.Close()

	c := clientFor(mint, "shh")
	err := c.Query(context.Background(), target.URL, "tenant-a", "query{x}", nil, nil)
	if err == nil {
		t.Fatal("Query ignored a GraphQL errors array")
	}
}
