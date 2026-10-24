// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
)

// The JWKS is fetched from user-management's INTERNAL port. The public port does not serve it,
// so a validator built against the public address would never come up.
func TestJWKSURLUsesTheInternalPort(t *testing.T) {
	cfg := config.UserManagementConfiguration{Hostname: "user-management", Port: 8080, InternalPort: 8082}
	if got, want := jwksURLForInstance(cfg), "http://user-management:8082/auth/jwks"; got != want {
		t.Fatalf("jwks url = %q, want %q", got, want)
	}
}

// Caller level: with the public port answering 404 for the JWKS (as the real one does) and the
// internal port serving it, the validator comes up from the internal one.
func TestValidatorForInstanceFetchesFromTheInternalListener(t *testing.T) {
	public := httptest.NewServer(http.NotFoundHandler())
	defer public.Close()
	_, pubPortStr, _ := net.SplitHostPort(public.Listener.Addr().String())
	pubPort, _ := strconv.Atoi(pubPortStr)

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key, err := BuildJWKS([]*rsa.PublicKey{&priv.PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != JWKSPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(key)
	}))
	defer internal.Close()
	host, intPortStr, _ := net.SplitHostPort(internal.Listener.Addr().String())
	intPort, _ := strconv.Atoi(intPortStr)

	cfg := config.UserManagementConfiguration{Hostname: host, Port: uint32(pubPort), InternalPort: uint32(intPort)}
	if _, err := FetchValidatorForInstance(context.Background(), cfg); err != nil {
		t.Fatalf("validator did not come up from the internal listener: %v", err)
	}
	// Control: pointed at the public port alone (internal = public), the 404 fails it.
	bad := config.UserManagementConfiguration{Hostname: host, Port: uint32(pubPort), InternalPort: uint32(pubPort)}
	if _, err := FetchValidatorForInstance(context.Background(), bad); err == nil {
		t.Fatal("a validator built against the public port (which 404s the JWKS) came up")
	}
}
