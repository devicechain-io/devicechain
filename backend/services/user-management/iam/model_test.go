// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"reflect"
	"testing"
)

func TestRoleScopeValid(t *testing.T) {
	for _, s := range []RoleScope{ScopeSystem, ScopeTenant} {
		if !s.Valid() {
			t.Errorf("scope %q should be valid", s)
		}
	}
	for _, s := range []RoleScope{"", "global", "Tenant", "SYSTEM"} {
		if RoleScope(s).Valid() {
			t.Errorf("scope %q should be invalid", s)
		}
	}
}

func TestUnionAuthoritiesDedupesAndPreservesOrder(t *testing.T) {
	roles := []Role{
		{Token: "a", Authorities: []string{"device:read", "device:write"}},
		{Token: "b", Authorities: []string{"device:write", "command:write"}}, // device:write dup
		{Token: "c", Authorities: nil},
	}
	got := unionAuthorities(roles)
	want := []string{"device:read", "device:write", "command:write"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unionAuthorities = %v, want %v", got, want)
	}
}

func TestUnionAuthoritiesEmpty(t *testing.T) {
	if got := unionAuthorities(nil); len(got) != 0 {
		t.Errorf("expected empty union, got %v", got)
	}
}

func TestSystemAuthorities(t *testing.T) {
	id := &Identity{SystemRoles: []Role{
		{Scope: ScopeSystem, Token: SuperuserRoleToken, Authorities: []string{"*"}},
	}}
	if got := id.SystemAuthorities(); !reflect.DeepEqual(got, []string{"*"}) {
		t.Errorf("SystemAuthorities = %v, want [*]", got)
	}
}

func TestTenantAuthorities(t *testing.T) {
	m := &Membership{
		TenantId: "acme",
		TenantRoles: []Role{
			{Scope: ScopeTenant, Token: "operator", Authorities: []string{"device:read", "device:write"}},
			{Scope: ScopeTenant, Token: "viewer", Authorities: []string{"device:read"}}, // dup read
		},
	}
	got := m.TenantAuthorities()
	want := []string{"device:read", "device:write"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TenantAuthorities = %v, want %v", got, want)
	}
}
