// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchemaDiscoveryDoesNotInferLegacyFromStatus(t *testing.T) {
	for _, status := range []int{400, 404, 405, 204} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls, legacyCalls := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodOptions {
					w.WriteHeader(status)
					return
				}
				calls++
				_, _ = w.Write([]byte(`{"data":{}}`))
			}))
			defer srv.Close()
			s := NewTenantSession(srv.Client(), srv.URL, "", "", "tenant").WithSchemaNegotiation(func(string) string {
				legacyCalls++
				return skewOldSchema
			})
			s.access, s.expiresAt = "token", time.Now().Add(time.Hour)
			err := s.Query(context.Background(), srv.URL, `{item{modern}}`, nil, nil)
			var statusErr *StatusError
			var unsupported *UnsupportedFeatureError
			if !errors.As(err, &statusErr) || statusErr.StatusCode != status || errors.As(err, &unsupported) || calls != 0 || legacyCalls != 0 {
				t.Fatalf("unidentified endpoint classified as legacy: err=%v calls=%d legacyCalls=%d", err, calls, legacyCalls)
			}
		})
	}
}

func TestSchemaDiscoveryRejectsRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, destination := range []string{"other plane", "other origin", "plaintext downgrade"} {
			t.Run(fmt.Sprintf("%d/%s", status, destination), func(t *testing.T) {
				var redirected, operations atomic.Int32
				manifest := func(w http.ResponseWriter, r *http.Request) {
					redirected.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"format": 1, "schema": skewNewSchema})
				}
				target := httptest.NewServer(http.HandlerFunc(manifest))
				defer target.Close()
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/other" {
						manifest(w, r)
						return
					}
					if r.Method != http.MethodOptions {
						operations.Add(1)
						_, _ = w.Write([]byte(`{"data":{}}`))
						return
					}
					location := target.URL
					if destination == "other plane" {
						location = "/other"
					}
					http.Redirect(w, r, location, status)
				})
				srv := httptest.NewUnstartedServer(handler)
				if destination == "plaintext downgrade" {
					srv.StartTLS()
				} else {
					srv.Start()
				}
				defer srv.Close()
				client := srv.Client()
				var policyCalls atomic.Int32
				client.CheckRedirect = func(*http.Request, []*http.Request) error { policyCalls.Add(1); return nil }
				a := NewAdminSession(client, srv.URL, "", "").WithSchemaNegotiation(nil)
				a.identity, a.expiresAt = "identity", time.Now().Add(time.Hour)
				if err := a.Query(context.Background(), srv.URL, `{item{modern}}`, nil, nil); err == nil || redirected.Load() != 0 || operations.Load() != 0 {
					t.Fatalf("discovery followed redirect: err=%v redirected=%d operations=%d", err, redirected.Load(), operations.Load())
				}
				if policyCalls.Load() != 0 {
					t.Fatal("discovery delegated redirect safety to the caller")
				}
				_ = client.CheckRedirect(nil, nil)
				if policyCalls.Load() != 1 {
					t.Fatal("discovery mutated the caller's redirect policy")
				}
			})
		}
	}
}

func TestSchemaNegotiationCachesPerSessionAndExactEndpoint(t *testing.T) {
	var discoveries, operations atomic.Int32
	var mu sync.Mutex
	byEndpoint := map[string]int{}
	currentSchema := skewNewSchema
	endpointClient := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			discoveries.Add(1)
			mu.Lock()
			byEndpoint[r.URL.RequestURI()]++
			sdl := currentSchema
			if r.URL.Path == "/old" || r.URL.RawQuery == "plane=old" {
				sdl = skewOldSchema
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"format": 1, "schema": sdl})
			return
		}
		operations.Add(1)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer endpointClient.Close()
	newSession := func() *TenantSession {
		s := NewTenantSession(endpointClient.Client(), endpointClient.URL, "", "", "tenant").WithSchemaNegotiation(nil)
		s.access, s.expiresAt = "token", time.Now().Add(time.Hour)
		return s
	}
	s := newSession()
	for range 100 {
		if err := s.Query(context.Background(), endpointClient.URL+"/new", `{item{modern}}`, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if discoveries.Load() != 1 || operations.Load() != 100 {
		t.Fatalf("100 operations: discoveries=%d operations=%d", discoveries.Load(), operations.Load())
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := s.Query(context.Background(), endpointClient.URL+"/concurrent", `{item{modern}}`, nil, nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for _, path := range []string{"/old", "/new?plane=old"} {
		if err := s.Query(context.Background(), endpointClient.URL+path, `{item{modern}}`, nil, nil); err == nil {
			t.Fatalf("reused wrong endpoint's contract for %s", path)
		}
	}
	if discoveries.Load() != 4 || operations.Load() != 120 {
		t.Fatalf("endpoint cache: discoveries=%d operations=%d", discoveries.Load(), operations.Load())
	}
	mu.Lock()
	currentSchema = skewOldSchema
	mu.Unlock()
	if err := newSession().Query(context.Background(), endpointClient.URL+"/new", `{item{modern}}`, nil, nil); err == nil {
		t.Fatal("a new session reused a previous session's schema")
	}
	if discoveries.Load() != 5 {
		t.Fatalf("new session did not discover: %d", discoveries.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if byEndpoint["/concurrent"] != 1 || byEndpoint["/new"] != 2 || byEndpoint["/old"] != 1 || byEndpoint["/new?plane=old"] != 1 {
		t.Fatalf("wrong discovery counts: %v", byEndpoint)
	}
}

func TestSchemaDiscoveryFailuresDoNotDispatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, "unauthorized"},
		{"outage", 503, "unavailable"},
		{"malformed", 200, "<html>proxy</html>"},
		{"unknown format", 200, `{"format":2,"schema":"type Query { item: Item! } type Item { name: String! }"}`},
		{"empty schema", 200, `{"format":1,"schema":""}`},
		{"invalid schema", 200, `{"format":1,"schema":"broken"}`},
		{"oversize", 200, strings.Repeat(" ", maxResponseBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodOptions {
					calls++
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			s := NewTenantSession(srv.Client(), srv.URL, "", "", "tenant").WithSchemaNegotiation(func(string) string { return skewOldSchema })
			s.WithLegacyEndpointVersion(srv.URL, "v1.7.0")
			s.access, s.expiresAt = "token", time.Now().Add(time.Hour)
			if err := s.Query(context.Background(), srv.URL, `{item{name}}`, nil, nil); err == nil || calls != 0 {
				t.Fatalf("discovery failure allowed dispatch: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestSchemaNegotiationUsesEachEndpointContract(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			sdl := skewNewSchema
			if r.URL.Path == "/old" {
				sdl = skewOldSchema
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"format": 1, "schema": sdl})
			return
		}
		calls++
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()
	a := NewAdminSession(srv.Client(), srv.URL, "", "").WithSchemaNegotiation(nil)
	a.identity, a.expiresAt = "identity", time.Now().Add(time.Hour)
	if err := a.Query(context.Background(), srv.URL+"/new", `{item{modern}}`, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Query(context.Background(), srv.URL+"/old", `{item{modern}}`, nil, nil); err == nil || calls != 1 {
		t.Fatalf("reused another endpoint's contract: err=%v operations=%d", err, calls)
	}
}

func TestLegacyFallbackRequiresVerifiedEndpointVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		allow   bool
	}{
		{"v1.7.0", true}, {"1.8.2", true},
		{"", false}, {"v1.6.9", false}, {"v1.9.0", false}, {"v2.7.0", false},
		{"v1.7.0-rc.1", false}, {"v1.7.00", false}, {"unknown", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			calls, legacyCalls := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				calls++
				_, _ = w.Write([]byte(`{"data":{}}`))
			}))
			defer srv.Close()
			known := srv.URL + "/graphql?plane=tenant"
			provider := func(string) string { legacyCalls++; return skewOldSchema }
			s := NewTenantSession(srv.Client(), srv.URL, "", "", "tenant").WithSchemaNegotiation(provider).WithLegacyEndpointVersion(known, tc.version)
			s.access, s.expiresAt = "token", time.Now().Add(time.Hour)
			err := s.Query(context.Background(), known, `{item{name}}`, nil, nil)
			if tc.allow {
				if err != nil || calls != 1 || legacyCalls != 1 {
					t.Fatalf("verified legacy endpoint refused: err=%v calls=%d legacy=%d", err, calls, legacyCalls)
				}
				var unsupported *UnsupportedFeatureError
				if err := s.Query(context.Background(), known, `{item{modern}}`, nil, nil); !errors.As(err, &unsupported) || !unsupported.Conservative {
					t.Fatalf("lost conservative contract: %v", err)
				}
			} else if err == nil || calls != 0 || legacyCalls != 0 {
				t.Fatalf("unsupported version allowed fallback: err=%v calls=%d legacy=%d", err, calls, legacyCalls)
			}
			before := legacyCalls
			if err := s.Query(context.Background(), srv.URL+"/graphql?plane=admin", `{item{name}}`, nil, nil); err == nil || legacyCalls != before {
				t.Fatalf("version binding escaped its exact endpoint: err=%v legacy=%d", err, legacyCalls)
			}
			// Configuration order must not discard the independently supplied version.
			a := NewAdminSession(srv.Client(), srv.URL, "", "").WithLegacyEndpointVersion(known, tc.version).WithSchemaNegotiation(provider)
			a.identity, a.expiresAt = "identity", time.Now().Add(time.Hour)
			if err := a.Query(context.Background(), known, `{item{name}}`, nil, nil); (err == nil) != tc.allow {
				t.Fatalf("admin legacy policy differs: %v", err)
			}
		})
	}
}

func TestSchemaNegotiationRetriesFailedDiscovery(t *testing.T) {
	for _, body := range []string{"outage", `{"format":1,"schema":"broken"}`} {
		t.Run(body, func(t *testing.T) {
			discoveries, operations := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodOptions {
					discoveries++
					if discoveries == 1 {
						if body == "outage" {
							w.WriteHeader(http.StatusServiceUnavailable)
						}
						_, _ = w.Write([]byte(body))
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"format": 1, "schema": skewNewSchema})
					return
				}
				operations++
				_, _ = w.Write([]byte(`{"data":{}}`))
			}))
			defer srv.Close()
			s := NewTenantSession(srv.Client(), srv.URL, "", "", "tenant").WithSchemaNegotiation(nil)
			s.access, s.expiresAt = "token", time.Now().Add(time.Hour)
			if err := s.Query(context.Background(), srv.URL, `{item{modern}}`, nil, nil); err == nil || operations != 0 {
				t.Fatalf("failed discovery dispatched: err=%v operations=%d", err, operations)
			}
			for range 2 {
				if err := s.Query(context.Background(), srv.URL, `{item{modern}}`, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			if discoveries != 2 || operations != 2 {
				t.Fatalf("failed discovery cached or success not cached: discoveries=%d operations=%d", discoveries, operations)
			}
			first, err := s.schemaNegotiation.contractFor(context.Background(), s.httpc, srv.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			second, err := s.schemaNegotiation.contractFor(context.Background(), s.httpc, srv.URL, nil)
			if err != nil || first.schema != second.schema || discoveries != 2 {
				t.Fatalf("parsed contract was not reused: err=%v discoveries=%d", err, discoveries)
			}
		})
	}
}

func TestSchemaNegotiationWaiterCanCancel(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var discoveries, operations atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			if discoveries.Add(1) == 1 {
				close(entered)
				<-release
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"format": 1, "schema": skewNewSchema})
			return
		}
		operations.Add(1)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s := NewTenantSession(srv.Client(), srv.URL, "", "", "tenant").WithSchemaNegotiation(nil)
	s.access, s.expiresAt = "token", time.Now().Add(time.Hour)
	owner := make(chan error, 1)
	go func() { owner <- s.Query(context.Background(), srv.URL, `{item{modern}}`, nil, nil) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery never started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	waiter := make(chan error, 1)
	go func() { waiter <- s.Query(ctx, srv.URL, `{item{modern}}`, nil, nil) }()
	select {
	case err := <-waiter:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter ignored cancellation")
	}
	unblock()
	if err := <-owner; err != nil {
		t.Fatal(err)
	}
	if discoveries.Load() != 1 || operations.Load() != 1 {
		t.Fatalf("cancelled waiter disturbed owner: discoveries=%d operations=%d", discoveries.Load(), operations.Load())
	}
}
