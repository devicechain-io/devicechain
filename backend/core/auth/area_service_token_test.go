// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"path/filepath"
	"slices"
	"testing"
)

// The area-bound list is closed and, in this release, holds exactly the automation area. Moving an
// existing area onto the area-bound mint is a decision of its own, not a side effect of an edit here.
func TestAreaBoundCallersAreExactlyAutomation(t *testing.T) {
	if got := AreaBoundCallerNames(); !slices.Equal(got, []string{"automation"}) {
		t.Fatalf("AreaBoundCallerNames() = %v, want [automation]", got)
	}
	if !IsAreaBoundCaller("automation") {
		t.Fatal("automation is not area-bound")
	}
	for _, a := range []string{"", "device-management", "lwm2m-ingest", "Automation", "automation "} {
		if IsAreaBoundCaller(a) {
			t.Errorf("IsAreaBoundCaller(%q) = true", a)
		}
	}
}

// An area-bound area's ceiling — the ONLY thing its credential can ever be turned into — may hold
// read authorities and nothing else. The list below is the whole of what this test admits; a
// ceiling entry that widens an area-bound caller (a write, the super-authority, a command or bulk
// authority) fails here, in the same review that makes it.
func TestAreaBoundCeilingsHoldNoWriteAuthority(t *testing.T) {
	admitted := map[Authority]bool{DeviceRead: true, TenantRead: true}
	for _, area := range AreaBoundCallerNames() {
		ceiling, ok := ServiceCeiling(area)
		if !ok {
			// No ceiling yet: the area-bound mint refuses it everything (an empty ceiling).
			continue
		}
		for _, a := range ceiling {
			if !admitted[a] {
				t.Errorf("area-bound caller %s has %s in its ceiling; only %v are admitted", area, a, admitted)
			}
		}
	}
}

func TestAreaMintCredentialFile(t *testing.T) {
	got := AreaMintCredentialFile("/etc/dci-mint", "automation")
	if want := filepath.Join("/etc/dci-mint", "automation", "credential"); got != want {
		t.Fatalf("AreaMintCredentialFile = %q, want %q", got, want)
	}
	if AreaMintCredentialKey != "credential" || AreaMintCredentialDir != "/etc/dci-mint" {
		t.Fatal("the mount contract moved; the chart restates it (templates/deployment.yaml)")
	}
}
