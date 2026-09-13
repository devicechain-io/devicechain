// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
)

// 🔴 THE CHARACTER SET IS A CONTRACT WITH SYSTEMS THIS PROJECT DOES NOT OWN. These
// values are handed to a database, an object store and a dashboard stack, each of
// which parses credentials its own way, and to a keyword connection string, a shell
// word and a YAML scalar on the way. Every character below is syntax in at least one
// of those, so a minted value carrying one turns a working credential into a parse
// error somewhere downstream — reported, if at all, as an authentication failure.
func TestAMintedCredentialCarriesNothingThatIsSyntaxAnywhere(t *testing.T) {
	const forbidden = "@/?#%:&=+ '\"\\$`|;<>(){}[]*!~^,\n\t\r="

	for i := 0; i < 200; i++ {
		v, err := mintPassword()
		if err != nil {
			t.Fatalf("minting: %v", err)
		}
		if got := strings.IndexAny(v, forbidden); got >= 0 {
			t.Fatalf("a minted credential contains %q, which is syntax in at least one "+
				"consumer's parser: %s", v[got], v)
		}
	}
}

// Entropy, stated as a property rather than trusted from the constant: 32 bytes in
// base64url without padding is 43 characters, and two mints must never agree.
func TestAMintedCredentialIsLongAndUnique(t *testing.T) {
	const want = 43
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		v, err := mintPassword()
		if err != nil {
			t.Fatalf("minting: %v", err)
		}
		if len(v) != want {
			t.Fatalf("a minted credential is %d characters, want %d — the entropy budget moved", len(v), want)
		}
		if seen[v] {
			t.Fatalf("two mints produced the same value, which means this is not random: %s", v)
		}
		seen[v] = true
	}
}

// 🔴 MinIO's floor used to be enforced by an OpenTofu variable validation. A Secret
// written by dcctl never passes through that, so the check has to travel with the
// value — below the floor the object store crash-loops, and the first thing anyone
// notices is that the write-ahead log stopped being archived.
func TestTheObjectStoreFloorRefusesAValueThatWouldCrashLoopIt(t *testing.T) {
	for _, short := range []string{"", "a", "1234567"} {
		if err := validateObjectStoreSecret(short); err == nil {
			t.Errorf("accepted a %d-character object-store credential; the object store "+
				"will not start on it", len(short))
		}
	}
	// The counterweight: a floor that refuses everything is an outage, not a guard.
	if err := validateObjectStoreSecret("12345678"); err != nil {
		t.Errorf("refused a credential exactly at the floor: %v", err)
	}
	v, err := mintObjectStoreSecret()
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	if err := validateObjectStoreSecret(v); err != nil {
		t.Fatalf("the mint produced a value its own floor refuses: %v", err)
	}
}

func TestAnEmptyEntropyBudgetIsRefused(t *testing.T) {
	for _, n := range []int{0, -1} {
		if _, err := mintPasswordOfBytes(n); err == nil {
			t.Errorf("minted a credential from %d bytes of entropy", n)
		}
	}
}
