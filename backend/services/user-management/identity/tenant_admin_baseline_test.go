// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"slices"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// The authorities a tenant uses to administer itself (ADR-081) are granted by a role,
// never by the baseline every enabled member receives — at EITHER mint path that unions
// it. member:read and member:write are new; role:read and role:write are now dual-tier,
// so a tenant access token can satisfy them, and the baseline is the one place that
// would hand them to every member at once.
//
// Asserted on both callers, not on the shared list: the console login
// (issueTenantTokens) is checked on the minted, decoded token, and the OAuth grant on
// effectiveAuthorities, the set its scope cap intersects. A shared list keeps their
// contents equal but does not make either one perform (or not perform) the union.
func TestTenantAdministrationIsNotInEitherBaseline(t *testing.T) {
	administration := []string{
		string(auth.MemberRead), string(auth.MemberWrite),
		string(auth.RoleRead), string(auth.RoleWrite),
	}

	e := newMintTestEnv(t)
	pair, err := e.m.issueTenantTokens("acme", "someone@acme.example", "test-epoch", "test-sid", nil, nil, auth.Scope{}, false, nil)
	if err != nil {
		t.Fatalf("issueTenantTokens: %v", err)
	}
	claims, err := e.validator.Validate(pair.AccessToken)
	if err != nil {
		t.Fatalf("the minted access token does not validate: %v", err)
	}
	oauth := effectiveAuthorities(nil, false)

	// Precondition: both paths really do union a baseline, so an absence is a real one.
	if !slices.Contains(claims.Authorities, string(auth.DeviceRead)) || !slices.Contains(oauth, string(auth.DeviceRead)) {
		t.Fatalf("precondition: a roleless member's console token %v or OAuth set %v carries no baseline",
			claims.Authorities, oauth)
	}
	for _, a := range administration {
		if slices.Contains(claims.Authorities, a) {
			t.Errorf("a roleless member's console token carries %q: every member could administer the tenant", a)
		}
		if slices.Contains(oauth, a) {
			t.Errorf("a roleless member's OAuth grant carries %q: every member could administer the tenant", a)
		}
	}
}
