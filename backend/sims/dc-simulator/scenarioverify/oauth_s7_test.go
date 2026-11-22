// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

func s7TestToken(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "header." + base64.RawURLEncoding.EncodeToString(b) + ".signature"
}

func s7TestClaims() map[string]any {
	return map[string]any{"tenant": "tenant-b", "username": "member@example.invalid", "client_id": "per-run",
		"scope": "read-only location", "aud": []string{"https://mcp.example.invalid/api/mcp"}, "exp": time.Now().Add(time.Hour).Unix()}
}

func TestS7PKCEUserFlowUsesRealHTTPAuthorizationSteps(t *testing.T) {
	var challenge, state string
	calls := []string{}
	hs := &sim.Handshake{Tenant: "tenant-b", SimEmail: "member@example.invalid", SimPassword: "member-password"}
	resource := "https://mcp.example.invalid/api/mcp"
	token := s7TestToken(s7TestClaims())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Method != http.MethodPost {
			t.Error("flow used a non-POST")
		}
		f := r.PostForm
		if f.Get("client_id") != "per-run" || f.Get("resource") != resource || f.Get("redirect_uri") != s7Redirect {
			t.Error("grant binding changed")
		}
		if r.URL.Path == "/oauth/authorize" {
			if f.Get("code_challenge_method") != "S256" || f.Get("scope") != "read-only location" {
				t.Error("PKCE or scope omitted")
			}
			switch f.Get("step") {
			case "login":
				calls = append(calls, "login")
				challenge, state = f.Get("code_challenge"), f.Get("state")
				if challenge == "" || state == "" || f.Get("email") != hs.SimEmail || f.Get("password") != hs.SimPassword {
					t.Error("member did not authenticate")
				}
				w.Write([]byte(`<input type="hidden" name="identity_token" value="identity-proof">`))
			case "consent":
				calls = append(calls, "consent")
				if f.Get("tenant") != hs.Tenant || f.Get("identity_token") != "identity-proof" || f.Get("action") != "allow" || f.Get("state") != state || f.Get("code_challenge") != challenge {
					t.Error("consent did not preserve member or PKCE proof")
				}
				if f.Get("password") != "" {
					t.Error("password unnecessarily repeated at consent")
				}
				w.Header().Set("Location", s7Redirect+"?"+url.Values{"state": {state}, "code": {"one-shot-code"}}.Encode())
				w.WriteHeader(http.StatusFound)
			default:
				t.Error("unexpected authorization step")
			}
			return
		}
		if r.URL.Path != "/oauth/token" {
			t.Error("unexpected flow endpoint")
			return
		}
		calls = append(calls, "token")
		if f.Get("grant_type") != "authorization_code" || f.Get("code") != "one-shot-code" || f.Get("client_secret") != "" {
			t.Error("not a public user code grant")
		}
		sum := sha256.Sum256([]byte(f.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			t.Error("PKCE verifier does not match")
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer", "refresh_token": "never-persist-this"})
	}))
	defer server.Close()
	hs.Endpoints.UserGraphQL = server.URL + "/graphql"
	got, err := s7Authorize(context.Background(), server.Client(), hs, "per-run", resource)
	if err != nil || got != token || strings.Join(calls, ",") != "login,consent,token" {
		t.Fatalf("flow steps %v, error %v", calls, err)
	}
}

func TestS7RefusesOperatorAgentWrongTenantAndUnboundTokens(t *testing.T) {
	hs := &sim.Handshake{Tenant: "tenant-b", SimEmail: "member@example.invalid"}
	for _, change := range []map[string]any{
		{"tenant": "tenant-a"}, {"username": "operator@example.invalid"}, {"sudo": true},
		{"client_id": "other"}, {"scope": "read-only location act:alarms"}, {"scope": "read-only"}, {"scope": "location"}, {"aud": []string{"another-resource"}},
		{"scp": []string{"customer"}}, {"scp": nil}, {"exp": 1},
	} {
		claims := s7TestClaims()
		for k, v := range change {
			claims[k] = v
		}
		token := s7TestToken(claims)
		err := s7MemberClaims(token, hs, "per-run", "https://mcp.example.invalid/api/mcp")
		if err == nil {
			t.Fatalf("wrong user grant passed: %v", change)
		}
		if strings.Contains(err.Error(), token) {
			t.Fatal("credential appeared in error")
		}
	}
}

func TestS7RegistersLocationConsentForFullAlarmTruth(t *testing.T) {
	t.Setenv("DC_ADMIN_PASSWORD", "operator-password")
	registered := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path == "/graphql" {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"login": map[string]any{"identityToken": "operator", "expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339)}}})
			return
		}
		if r.URL.Path != "/admin/graphql" || r.Header.Get("Authorization") != "Bearer operator" {
			t.Error("client registration did not use the operator registry")
		}
		registered = true
		if !strings.Contains(req.Query, `scopes:["read-only","location"]`) || !strings.Contains(req.Query, "confidential:false") {
			t.Error("full alarm comparison requires a public client registered for separate location consent")
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"createOauthClient": map[string]any{"client": map[string]any{"clientId": "per-run"}}}})
	}))
	defer server.Close()
	hs := &sim.Handshake{Endpoints: sim.Endpoints{UserGraphQL: server.URL + "/graphql"}}
	if err := S7RegisterClient(context.Background(), hs, server.Client(), "per-run", false); err != nil || !registered {
		t.Fatalf("registration=%v err=%v", registered, err)
	}
}

func TestS7OAuthErrorsNeverEchoServerBodiesOrCredentials(t *testing.T) {
	secret := "private-pkce-password-client-secret-access-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(secret))
	}))
	defer server.Close()
	hs := &sim.Handshake{Tenant: "b", SimEmail: "member", SimPassword: secret, Endpoints: sim.Endpoints{UserGraphQL: server.URL + "/graphql"}}
	_, err := s7Authorize(context.Background(), server.Client(), hs, "per-run", "resource")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("OAuth failure leaked credentials or passed")
	}
}
