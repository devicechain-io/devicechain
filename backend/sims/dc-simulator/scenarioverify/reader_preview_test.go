// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dc-simulator/sim"
)

// previewRule's server may spend config.PreviewBudget on one replay and then answers with a
// typed truncated result. The verifier's client must outwait that, or the truncated answer is
// replaced by a transport timeout and the day has NO VERDICT.
func TestPreviewClientOutwaitsTheServerBudget(t *testing.T) {
	c := previewHTTP("")
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("preview transport is %T, want *http.Transport", c.Transport)
	}
	if tr.ResponseHeaderTimeout <= config.PreviewBudget {
		t.Fatalf("preview header timeout %v does not outwait the server budget %v", tr.ResponseHeaderTimeout, config.PreviewBudget)
	}
	if tr.ResponseHeaderTimeout != config.PreviewClientHeaderTimeout {
		t.Fatalf("preview header timeout %v, want %v", tr.ResponseHeaderTimeout, config.PreviewClientHeaderTimeout)
	}
	if c.Timeout < tr.ResponseHeaderTimeout {
		t.Fatalf("preview call timeout %v is below its header timeout %v", c.Timeout, tr.ResponseHeaderTimeout)
	}
}

func TestPreviewClientIsBuiltByNewReaderAndIsNotTheSharedOne(t *testing.T) {
	shared := &http.Client{Timeout: 60 * time.Second}
	hs := &sim.Handshake{Tenant: "t", SimEmail: "e", SimPassword: "p", Endpoints: sim.Endpoints{
		UserGraphQL: "http://x/u", UpdateMgmtGraphQL: "http://x/up", DeviceMgmtGraphQL: "http://x/d", CommandMgmtGraphQL: "http://x/c",
		EventProcessingGraphQL: "http://x/e", EventMgmtWS: "ws://x/m", DeviceStateGraphQL: "http://x/s",
	}}
	r, err := NewReader(hs, shared)
	if err != nil {
		t.Fatal(err)
	}
	if r.previewClient == nil || r.previewClient == shared {
		t.Fatal("NewReader must give previewRule its own client")
	}
	if tr, ok := r.previewClient.Transport.(*http.Transport); !ok || tr.ResponseHeaderTimeout != config.PreviewClientHeaderTimeout {
		t.Fatalf("preview client transport = %#v", r.previewClient.Transport)
	}
}

// Preview must travel over the preview session, not the shared one.
func TestPreviewUsesThePreviewSession(t *testing.T) {
	serve := func(tok string, onPreview func(auth string), t *testing.T) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct{ Query string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			var data any
			switch {
			case strings.Contains(req.Query, "login("):
				data = map[string]any{"login": map[string]any{"identityToken": "identity"}}
			case strings.Contains(req.Query, "selectTenant("), strings.Contains(req.Query, "refresh("):
				data = map[string]any{"selectTenant": map[string]any{"accessToken": tok, "refreshToken": "r", "expiresAt": "2099-01-01T00:00:00Z"}, "refresh": map[string]any{"accessToken": tok, "refreshToken": "r", "expiresAt": "2099-01-01T00:00:00Z"}}
			default:
				onPreview(r.Header.Get("Authorization"))
				data = map[string]any{"previewRule": map[string]any{"ok": true, "firings": []any{}}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		}))
	}
	var got []string
	a := serve("shared", func(string) {}, t)
	b := serve("preview", func(auth string) { got = append(got, auth) }, t)
	defer a.Close()
	defer b.Close()
	r := &Reader{
		session:        userclient.NewTenantSession(a.Client(), a.URL, "e", "p", "t"),
		previewSession: userclient.NewTenantSession(b.Client(), b.URL, "e", "p", "t"),
		endpoints:      sim.Endpoints{EventProcessingGraphQL: b.URL},
	}
	if _, err := r.Preview(t.Context(), "{}", "profile", time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "Bearer preview" {
		t.Fatalf("previewRule was sent with %v, want the preview session's token", got)
	}
}
