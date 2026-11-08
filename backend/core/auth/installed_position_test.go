// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"testing"
)

// TestCanReadInstalledPosition pins the implication direction. location:read implies the
// installed authority; the installed authority never implies location:read.
//
// Negative control (run): drop the `|| satisfies(claims, LocationRead)` arm and the
// location:read case goes red; make Authorize(LocationRead) accept the installed
// authority and TestInstalledAuthorityDoesNotImplyLocationRead goes red.
func TestCanReadInstalledPosition(t *testing.T) {
	tiers, known := TiersOf(LocationInstalledRead)
	if !known || !tiers.Has(TierTenant) {
		t.Fatalf("location:installed:read must be a known tenant-tier authority, got %v known=%v", tiers, known)
	}
	for _, c := range []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"installed authority", ctxWith(tenantAccessClaims(string(LocationInstalledRead))), true},
		{"location:read implies it", ctxWith(tenantAccessClaims(string(LocationRead))), true},
		{"tenant-admin star", ctxWith(tenantAccessClaims(string(AuthorityAll))), true},
		{"the read-only baseline", ctxWith(tenantAccessClaims(string(DeviceRead), string(StateRead), string(EventRead))), false},
		{"no claims", context.Background(), false},
	} {
		if got := CanReadInstalledPosition(c.ctx); got != c.want {
			t.Errorf("%s: CanReadInstalledPosition = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestInstalledAuthorityDoesNotImplyLocationRead(t *testing.T) {
	ctx := ctxWith(tenantAccessClaims(string(LocationInstalledRead)))
	if err := Authorize(ctx, LocationRead); err == nil {
		t.Fatal("location:installed:read satisfied location:read: an installed-only caller would be handed the effective or reported position")
	}
}
