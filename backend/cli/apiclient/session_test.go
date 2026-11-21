// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
)

func TestCommandClientsNegotiateBeforeDispatch(t *testing.T) {
	for _, admin := range []bool{false, true} {
		t.Run(map[bool]string{false: "tenant", true: "admin"}[admin], func(t *testing.T) {
			calls, discoveries := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodOptions {
					discoveries++
					want := "Bearer access"
					if admin {
						want = "Bearer identity"
					}
					if r.Header.Get("Authorization") != want {
						t.Errorf("wrong discovery credential: %s", r.Header.Get("Authorization"))
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"format": 1, "schema": "type Query { item: String! }"})
					return
				}
				var body struct{ Query string }
				_ = json.NewDecoder(r.Body).Decode(&body)
				expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
				switch {
				case strings.Contains(body.Query, "login("):
					_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"login": map[string]any{"identityToken": "identity", "expiresAt": expires}}})
				case strings.Contains(body.Query, "selectTenant("):
					_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"selectTenant": map[string]any{"accessToken": "access", "refreshToken": "refresh", "expiresAt": expires}}})
				default:
					calls++
					_, _ = w.Write([]byte(`{"errors":[{"message":"unknown field modern"}]}`))
				}
			}))
			defer srv.Close()
			var err error
			if admin {
				err = NewAdminSession(srv.Client(), srv.URL, "email", "password").Query(context.Background(), srv.URL, `{modern}`, nil, nil)
			} else {
				err = NewTenantSession(srv.Client(), srv.URL, "email", "password", "tenant").Query(context.Background(), srv.URL, `{modern}`, nil, nil)
			}
			var unsupported *userclient.UnsupportedFeatureError
			if !errors.As(err, &unsupported) || discoveries != 1 || calls != 0 {
				t.Fatalf("command bypassed negotiation: err=%v discoveries=%d operations=%d", err, discoveries, calls)
			}
		})
	}
}
