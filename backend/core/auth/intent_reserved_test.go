// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"slices"
	"testing"
)

// The intent authorities are reserved a minor ahead of the API that checks them, so an
// upgraded user-management pod already accepts a role naming them. Spelled out rather
// than read back from the constants: a test that derives its expectation from the value
// it checks passes by construction.
var reservedIntentAuthorities = map[Authority]string{
	IntentRead:  "intent:read",
	IntentWrite: "intent:write",
}

func TestIntentAuthoritiesVocabularyAndTier(t *testing.T) {
	for authority, name := range reservedIntentAuthorities {
		t.Run(name, func(t *testing.T) {
			if string(authority) != name || !ValidAuthority(name) {
				t.Fatalf("%s must be a grantable authority", name)
			}
			tiers, ok := TiersOf(authority)
			if !ok || len(tiers) != 1 || tiers[0] != TierTenant {
				t.Fatalf("%s must be tenant-only, got %v (known=%v)", name, tiers, ok)
			}
			if !slices.Contains(Authorities(), name) {
				t.Fatalf("the complete vocabulary must offer %s", name)
			}
			if !slices.Contains(AuthoritiesForScope(TierTenant), name) {
				t.Fatalf("tenant roles must be able to grant %s", name)
			}
			if slices.Contains(AuthoritiesForScope(TierSystem), name) {
				t.Fatalf("system roles must not offer the tenant-only %s", name)
			}
			if IsProposalAuthority(name) {
				t.Fatalf("%s must not be a proposal authority", name)
			}
		})
	}
}

// Consent to read tenant data must not reach intents through a scope nobody chose for
// them. intent:read ends in ":read", so the read-only ceiling is the one most likely to
// acquire it by habit; check every ceiling all the same.
func TestIntentAuthoritiesAbsentFromOAuthCeilings(t *testing.T) {
	for authority := range reservedIntentAuthorities {
		for scope, ceiling := range scopeAllowances {
			if slices.Contains(ceiling, string(authority)) {
				t.Errorf("scope %s contains %s", scope, authority)
			}
		}
		for scope, ceiling := range actScopeCeilings {
			if slices.Contains(ceiling, string(authority)) {
				t.Errorf("action scope %s contains %s", scope, authority)
			}
		}
	}
}

// Agents propose intents through the approval path, never by holding intent:write; no
// proposal authority may be derived from (or be) either reserved name.
func TestIntentAuthoritiesAreNotProposalSources(t *testing.T) {
	for proposal, source := range proposalSources {
		if _, reserved := reservedIntentAuthorities[source]; reserved {
			t.Errorf("proposal %s is derived from %s", proposal, source)
		}
		if _, reserved := reservedIntentAuthorities[proposal]; reserved {
			t.Errorf("%s appears as a proposal authority", proposal)
		}
	}
}

// The tenant administrator's "*" satisfies each when it is eventually checked, and an
// explicit grant does too; no neighbouring authority implies it, in either direction.
func TestIntentAuthoritiesRequireAnExplicitGrantOrWildcard(t *testing.T) {
	for authority := range reservedIntentAuthorities {
		for _, held := range []Authority{authority, AuthorityAll} {
			if err := Authorize(ctxWith(tenantAccessClaims(string(held))), authority); err != nil {
				t.Errorf("tenant grant %s did not satisfy %s: %v", held, authority, err)
			}
		}
		for _, neighbour := range []Authority{DeviceRead, DeviceWrite, FleetOperate, AlarmWrite} {
			if err := Authorize(ctxWith(tenantAccessClaims(string(neighbour))), authority); err == nil {
				t.Errorf("%s must not imply %s", neighbour, authority)
			}
		}
	}
	if err := Authorize(ctxWith(tenantAccessClaims(string(IntentWrite))), IntentRead); err == nil {
		t.Error("intent:write must not imply intent:read; a role that needs both names both")
	}
	if err := Authorize(ctxWith(tenantAccessClaims(string(IntentRead))), IntentWrite); err == nil {
		t.Error("intent:read must not imply intent:write")
	}
}

// No service holds either: the area that will reconcile intents acts under an identity
// that conveys no authority.
func TestIntentAuthoritiesAbsentFromServiceTokenLists(t *testing.T) {
	for caller, authorities := range serviceCallers {
		for authority := range reservedIntentAuthorities {
			if slices.Contains(authorities, authority) {
				t.Errorf("service %s holds %s", caller, authority)
			}
		}
	}
}
