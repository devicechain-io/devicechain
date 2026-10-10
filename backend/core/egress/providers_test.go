// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"net/netip"
	"strings"
	"testing"
)

// Every provider in the table is covered by the guard, or says why it is not.
func TestEveryMetadataProviderIsCoveredOrExplained(t *testing.T) {
	if len(MetadataProviders) == 0 {
		t.Fatal("precondition: the provider table is empty, so this asserts nothing")
	}
	if gaps := ProviderGaps(MetadataProviders, NewGuard(nil)); len(gaps) != 0 {
		t.Fatalf("provider coverage gaps:\n%s", strings.Join(gaps, "\n"))
	}
}

// The check can fail: a provider with no entry, one marked both ways, and an address the guard
// admits are each reported.
func TestProviderGapsReportsEachKindOfGap(t *testing.T) {
	g := NewGuard(nil)
	bad := []MetadataProvider{
		{Name: "empty"},
		{Name: "both", Addresses: []netip.Addr{netip.MustParseAddr("169.254.169.254")}, Unverified: "x"},
		{Name: "public", Addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}},
	}
	gaps := strings.Join(ProviderGaps(bad, g), "\n")
	for _, want := range []string{"empty: no address", "both: lists addresses AND", "public: the guard admits"} {
		if !strings.Contains(gaps, want) {
			t.Errorf("gaps %q lack %q", gaps, want)
		}
	}
}
