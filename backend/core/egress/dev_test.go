// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
)

func devGuard(t *testing.T, dev ...string) *Guard {
	t.Helper()
	g, err := FromConfig(config.EgressConfiguration{DevAllowedDestinations: dev})
	if err != nil {
		t.Fatalf("FromConfig(%v): %v", dev, err)
	}
	return g
}

func TestDevAllowAdmitsOnlyWhatItNames(t *testing.T) {
	g := devGuard(t, "192.168.1.0/24", "172.18.0.1/32", "fd12:3456::/48")
	for _, ok := range []string{"192.168.1.20", "172.18.0.1", "fd12:3456::7"} {
		if err := g.CheckAddr(netip.MustParseAddr(ok)); err != nil {
			t.Errorf("%s should be admitted by the dev allowance: %v", ok, err)
		}
	}
	for _, no := range []string{"192.168.2.1", "172.18.0.2", "10.0.0.5", "fd12:3457::1", "127.0.0.1"} {
		err := g.CheckAddr(netip.MustParseAddr(no))
		if !errors.Is(err, ErrBlocked) {
			t.Errorf("%s must stay blocked, got %v", no, err)
		}
	}
}

func TestNoDevAllowIsTheStrictGuard(t *testing.T) {
	g := devGuard(t)
	if err := g.CheckAddr(netip.MustParseAddr("192.168.1.20")); !errors.Is(err, ErrBlocked) {
		t.Fatalf("default must be fail closed, got %v", err)
	}
}

// The cloud metadata addresses are refused when named, when covered by a wider prefix,
// and when reached through an IPv6 wrapper.
func TestDevAllowRefusesMetadataAndOtherNeverRanges(t *testing.T) {
	for _, p := range []string{
		"169.254.169.254/32", "169.254.0.0/16", "0.0.0.0/0", "::/0", "100.100.100.200/32",
		"168.63.129.16/32", "127.0.0.0/8", "127.0.0.1/32", "fd00:ec2::254/128", "fd00::/8", "fe80::/10",
		"224.0.0.0/4", "64:ff9b::/96", "2002::/16", "::/96", "169.254.170.23/32", "0.0.0.0/1", "64:ff9b:1::/48", "64:ff9b::a9fe:a9fe/128", "::a9fe:a9fe/128", "2001:0:a9fe:a9fe::/100", "192.0.0.192/32", "192.0.0.0/24", "64:ff9b:1:a9fe:a9fe::/96", "::ffff:169.254.0.0/112",
	} {
		_, err := FromConfig(config.EgressConfiguration{DevAllowedDestinations: []string{p}})
		if err == nil {
			t.Errorf("%s must be refused at startup", p)
		}
	}
	// Defence in depth: even a guard built around a prefix that slipped past parsing refuses.
	g := NewGuard(nil).WithDevAllow([]netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0"),
	})
	for _, a := range []string{
		"169.254.169.254", "100.100.100.200", "168.63.129.16", "127.0.0.1", "fd00:ec2::254",
		"::ffff:169.254.169.254", "64:ff9b::a9fe:a9fe", "64:ff9b:1::a9fe:a9fe", "2002:a9fe:a9fe::1", "fe80::1",
	} {
		err := g.CheckAddr(netip.MustParseAddr(a))
		if !errors.Is(err, ErrBlocked) {
			t.Errorf("%s must be blocked even under a 0/0 dev prefix, got %v", a, err)
		}
	}
}

func TestEveryMetadataProviderAddressIsNeverDevAllowed(t *testing.T) {
	for _, p := range MetadataProviders {
		for _, a := range p.Addresses {
			if neverDevAllowedAddr(a) == "" {
				t.Errorf("%s metadata address %s is not in the never-allowed set", p.Name, a)
			}
		}
	}
}

func TestDevPrefixMustBeMasked(t *testing.T) {
	_, err := ParseDevPrefixes([]string{"192.168.1.5/24"})
	if err == nil || !strings.Contains(err.Error(), "bits set below") {
		t.Fatalf("want the masked-form refusal, got %v", err)
	}
}

// A dev allowance is not an operator exemption: it must not change what the operator list does.
func TestDevAllowDoesNotWidenOperatorList(t *testing.T) {
	g, err := FromConfig(config.EgressConfiguration{
		AllowedDestinations:    []string{"10.1.2.3/32"},
		DevAllowedDestinations: []string{"192.168.1.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.CheckAddr(netip.MustParseAddr("10.1.2.3")); err != nil {
		t.Errorf("operator allow lost: %v", err)
	}
	if err := g.CheckAddr(netip.MustParseAddr("10.1.2.4")); err == nil {
		t.Error("10.1.2.4 must stay blocked")
	}
}
