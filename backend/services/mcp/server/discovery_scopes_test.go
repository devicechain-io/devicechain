// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Follow the challenge's scope selection rule, rather than hand-configuring a
// token with scopes a real client would never discover.
func TestDiscoveryOffersGrantableScopes(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "default off"
		var opts []Option
		if enabled {
			name = "actuation on"
			opts = []Option{WithActuation(true)}
		}
		t.Run(name, func(t *testing.T) {
			iss, validator := mustIssuerValidator(t)
			mux := http.NewServeMux()
			Routes(mux, testResource, "https://as.example.com", validator, NewGraphQLClient(), DefaultLimits(), opts...)
			ts := httptest.NewServer(mux)
			defer ts.Close()
			resp, err := http.Post(ts.URL, "application/json", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("discovery status=%d, want 401", resp.StatusCode)
			}
			challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
			if err != nil || len(challenges) != 1 {
				t.Fatalf("challenges=%v err=%v", challenges, err)
			}
			challenge := challenges[0]
			u, err := url.Parse(challenge.Params["resource_metadata"])
			if err != nil || !u.IsAbs() {
				t.Fatalf("invalid resource_metadata: %v", challenge.Params)
			}
			resp, err = http.Get(ts.URL + u.EscapedPath())
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var md protectedResourceMetadata
			if err := json.NewDecoder(resp.Body).Decode(&md); err != nil {
				t.Fatal(err)
			}
			want := []string{coreauth.ScopeReadOnly, coreauth.ScopeLocation}
			if enabled {
				want = append(want, coreauth.ActScopes...)
			}
			if !slices.Equal(md.ScopesSupported, want) {
				t.Fatalf("metadata scopes=%v want %v", md.ScopesSupported, want)
			}
			selected := md.ScopesSupported
			if scope, present := challenge.Params["scope"]; present {
				selected = strings.Fields(scope)
			}
			if !slices.Equal(selected, want) {
				t.Fatalf("client selected scopes=%v want %v", selected, want)
			}
			tok, err := iss.IssueOAuthAccess("acme", "a@b.c", nil, []string{"device:read", "location:read", "alarm:propose"}, strings.Join(selected, " "), []string{testResource}, false, "discovery-client", "j-discovery")
			if err != nil {
				t.Fatal(err)
			}
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, HTTPClient: &http.Client{Transport: bearerTransport{token: tok.Token}}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			list, err := cs.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(toolNames(list.Tools), "list_suppression_windows") {
				t.Error("read-only discovery omitted suppression windows")
			}
			for _, tool := range []string{"propose_alarm_action", "get_proposal", "list_my_proposals"} {
				if got := slices.Contains(toolNames(list.Tools), tool); got != enabled {
					t.Errorf("%s offered=%v want %v", tool, got, enabled)
				}
			}
		})
	}
}

func TestReadOnlyAdmissionFailsClosedWithoutVerifiedIdentity(t *testing.T) {
	h := requireReadOnly(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unverified request admitted")
	}), testResource)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rec.Code)
	}
}
