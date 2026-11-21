// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"slices"
	"testing"
)

// The early vocabulary entry lets upgraded pods accept custom roles before any
// fleet API ships, while keeping the capability within one tenant.
func TestFleetOperateVocabularyAndTier(t *testing.T) {
	if string(FleetOperate) != "fleet:operate" || !ValidAuthority("fleet:operate") {
		t.Fatal("fleet:operate must be a grantable authority")
	}
	tiers, ok := TiersOf(FleetOperate)
	if !ok || len(tiers) != 1 || tiers[0] != TierTenant {
		t.Fatalf("fleet:operate must be tenant-only, got %v (known=%v)", tiers, ok)
	}
	if !slices.Contains(Authorities(), "fleet:operate") {
		t.Fatal("the complete vocabulary must offer fleet:operate")
	}
	if !slices.Contains(AuthoritiesForScope(TierTenant), "fleet:operate") {
		t.Fatal("tenant roles must be able to grant fleet:operate")
	}
	if slices.Contains(AuthoritiesForScope(TierSystem), "fleet:operate") {
		t.Fatal("system roles must not offer a tenant-only authority")
	}
}

// Consent to read tenant data must never permit a fleet-wide change. Check all
// OAuth ceilings as well as read-only so no other scope can acquire this power.
func TestFleetOperateAbsentFromOAuthCeilings(t *testing.T) {
	for scope, ceiling := range scopeAllowances {
		if slices.Contains(ceiling, string(FleetOperate)) {
			t.Errorf("scope %s contains fleet:operate", scope)
		}
	}
	for scope, ceiling := range actScopeCeilings {
		if slices.Contains(ceiling, string(FleetOperate)) {
			t.Errorf("action scope %s contains fleet:operate", scope)
		}
	}
}

// Granting fleet operation control must not implicitly give an agent a proposal
// capability; any future delegation needs its own explicit decision.
func TestFleetOperateIsNotAProposalSource(t *testing.T) {
	for proposal, source := range proposalSources {
		if source == FleetOperate || proposal == FleetOperate {
			t.Errorf("fleet:operate appears in proposalSources: %s -> %s", proposal, source)
		}
	}
}

// A tenant administrator's wildcard must satisfy this reserved capability when
// it is eventually checked; device editing alone must not imply fleet control.
func TestFleetOperateRequiresAnExplicitGrantOrWildcard(t *testing.T) {
	for _, authority := range []Authority{FleetOperate, AuthorityAll} {
		if err := Authorize(ctxWith(tenantAccessClaims(string(authority))), FleetOperate); err != nil {
			t.Errorf("tenant grant %s did not satisfy fleet:operate: %v", authority, err)
		}
	}
	if err := Authorize(ctxWith(tenantAccessClaims(string(DeviceWrite))), FleetOperate); err == nil {
		t.Fatal("device:write must not imply fleet:operate")
	}
}

// Machine callers have no fleet operations to perform in this release. The
// repository's service-ceiling AST guard also checks these lists against every
// production mint site, including the lazy dead-letter lifecycle client.
func TestFleetOperateAbsentFromServiceTokenLists(t *testing.T) {
	for caller, authorities := range serviceCallers {
		if slices.Contains(authorities, FleetOperate) || slices.Contains(authorities, AuthorityAll) {
			t.Errorf("service %s has ambient fleet operation authority: %v", caller, authorities)
		}
	}
}
