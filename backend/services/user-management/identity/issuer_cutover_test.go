// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
)

// resolveIssuerName is the cutover switch: a configured OAuth issuer URL wins and
// becomes every token's "iss"; absent one, tokens keep the legacy per-instance
// identifier (ADR-047). This is the behavior a refactor of Initialize could break
// silently, so it is pinned directly.
func TestResolveIssuerName(t *testing.T) {
	if got := resolveIssuerName("https://as.example.com", "inst-1"); got != "https://as.example.com" {
		t.Errorf("configured issuer: got %q, want the URL", got)
	}
	if got := resolveIssuerName("", "inst-1"); got != "dc-user-management:inst-1" {
		t.Errorf("legacy issuer: got %q, want dc-user-management:inst-1", got)
	}
}

// An OAuth-scoped refresh token must never be redeemable at the ordinary refresh
// path: that path re-resolves the identity's full authorities and mints an
// unscoped, unbound pair, so honoring a scoped token there would escalate a
// read-only, audience-bound session to a full-authority one (ADR-047). The guard
// rejects it before any store access, so a Manager with no KV wired still enforces
// it.
func TestRefreshRejectsScopedToken(t *testing.T) {
	key, err := auth.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	iss := auth.NewIssuer(key, "https://as.example.com", time.Minute, time.Hour)
	m := &Manager{validator: auth.NewValidator(&key.PublicKey)}

	scoped, err := iss.IssueOAuthRefresh("tenant-a", "alice@example.com",
		[]string{"viewer"}, []string{"device:read"},
		auth.ScopeReadOnly, []string{"https://mcp.example.com"}, "jti-scoped")
	if err != nil {
		t.Fatalf("IssueOAuthRefresh: %v", err)
	}
	if _, err := m.Refresh(context.Background(), scoped.Token); err != ErrInvalidToken {
		t.Fatalf("Refresh(scoped) error = %v, want ErrInvalidToken", err)
	}
}
