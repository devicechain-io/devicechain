// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"testing"
	"time"
)

// The session epoch rides the two exchangeable tiers — refresh (tenant and OAuth)
// and identity — and comes back out of the SIGNED token through the same validator
// user-management's redemption paths use. Reading it back rather than inspecting the
// spec is the point: a claim the signer dropped would pass any test of the inputs.
func TestSessionEpochRoundTripsOnTheExchangeableTiers(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "test", time.Minute, time.Hour)
	v := NewValidator(&key.PublicKey)
	const epoch SessionEpoch = "epoch-abc"

	refresh, err := iss.IssueRefresh("tenant-a", "alice@example.com", epoch, nil, nil, "jti-r")
	if err != nil {
		t.Fatalf("IssueRefresh: %v", err)
	}
	oauthRefresh, err := iss.IssueOAuthRefresh("tenant-a", "alice@example.com", epoch, nil,
		[]string{string(DeviceRead)}, ScopeReadOnly, nil, "mcp", "jti-o")
	if err != nil {
		t.Fatalf("IssueOAuthRefresh: %v", err)
	}
	identity, err := iss.IssueIdentity("alice@example.com", epoch, nil, nil, "jti-i")
	if err != nil {
		t.Fatalf("IssueIdentity: %v", err)
	}

	for name, tc := range map[string]struct {
		token    string
		validate func(string) (*Claims, error)
	}{
		"tenant refresh": {refresh.Token, v.ValidateRefresh},
		"oauth refresh":  {oauthRefresh.Token, v.ValidateRefresh},
		"identity":       {identity.Token, v.ValidateIdentity},
	} {
		claims, err := tc.validate(tc.token)
		if err != nil {
			t.Fatalf("%s: validate: %v", name, err)
		}
		if claims.SessionEpoch != epoch {
			t.Errorf("%s: sep = %q, want %q", name, claims.SessionEpoch, epoch)
		}
	}
}

// The 15-minute data-plane tier is deliberately untouched: no access token carries
// an epoch, so nothing that validates one can come to depend on it.
func TestAccessTokensCarryNoSessionEpoch(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "test", time.Minute, time.Hour)
	v := NewValidator(&key.PublicKey)

	plain, err := iss.IssueAccess("tenant-a", "alice", nil, nil, "a1")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := iss.IssueTenantAccess("tenant-a", "alice@example.com", nil, nil, false, "a2")
	if err != nil {
		t.Fatal(err)
	}
	oauth, err := iss.IssueOAuthAccess("tenant-a", "alice@example.com", nil, nil, ScopeReadOnly, nil, false, "mcp", "a3")
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"access": plain.Token, "tenant access": tenant.Token, "oauth access": oauth.Token} {
		claims, err := v.Validate(tok)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if claims.SessionEpoch != "" {
			t.Errorf("%s token carries sep %q; access tokens must carry none", name, claims.SessionEpoch)
		}
	}
}

// An empty epoch is refused at the MINT. A token minted without one could never be
// redeemed, so issuing it would be a success that fails later as a generic "invalid
// token", far from the missing value that caused it.
func TestTheExchangeableTiersRefuseAnEmptySessionEpoch(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "test", time.Minute, time.Hour)

	_, errRefresh := iss.IssueRefresh("tenant-a", "alice", "", nil, nil, "r")
	_, errOAuth := iss.IssueOAuthRefresh("tenant-a", "alice", "", nil, nil, ScopeReadOnly, nil, "mcp", "o")
	_, errIdentity := iss.IssueIdentity("alice", "", nil, nil, "i")
	for name, err := range map[string]error{"IssueRefresh": errRefresh, "IssueOAuthRefresh": errOAuth, "IssueIdentity": errIdentity} {
		if !errors.Is(err, ErrNoSessionEpoch) {
			t.Errorf("%s with an empty epoch returned %v, want ErrNoSessionEpoch", name, err)
		}
	}
}
