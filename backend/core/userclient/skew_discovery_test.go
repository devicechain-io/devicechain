// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
