// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"fmt"
	"net/netip"
	"strings"
)

// The local-development allowance (EgressConfiguration.DevAllowedDestinations).
//
// It exists so a developer on a local cluster can point a webhook or connector at a service
// on their own machine or LAN. It is a different list from the operator's allow list for one
// reason that matters: an operator's entry is an EXEMPTION from everything the guard
// refuses, while a dev entry is an exemption from everything EXCEPT the addresses that are
// never a legitimate destination for a tenant on any host — the cloud instance-metadata
// services, loopback, link-local, multicast and the unspecified address. The first is a
// deliberate decision by an operator who has read the sharp edges; the second is typed into
// a flag on a laptop, so it is built so that no value of it can reach credentials.

// neverDevAllowed are prefixes a dev entry may not overlap and an address in one is refused
// even when a dev prefix covers it. The metadata addresses come from MetadataProviders, so a
// provider added there is covered here without anybody remembering to.
var neverDevAllowed = func() []netip.Prefix {
	out := []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("224.0.0.0/4"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("::/128"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("fe80::/10"),
		netip.MustParsePrefix("ff00::/8"),
		// The cloud IPv6 metadata services live in unique-local space, which is otherwise
		// a legitimate private range, so the two provider /32 blocks are named whole.
		netip.MustParsePrefix("fd00:ec2::/32"),
		netip.MustParsePrefix("fd20:ce::/32"),
	}
	for _, p := range MetadataProviders {
		for _, a := range p.Addresses {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return out
}()

// neverDevAllowedAddr reports why addr can never be reached through a dev allowance, or "".
// An IPv6 form that carries an IPv4 address is judged on the address inside it as well as
// on the wrapper: a dev prefix of 64:ff9b::/96 must not be a way to 169.254.169.254.
func neverDevAllowedAddr(addr netip.Addr) string {
	addr = addr.Unmap().WithZone("")
	for _, p := range neverDevAllowed {
		if p.Contains(addr) {
			return p.String()
		}
	}
	if addr.Is6() {
		if server, ok := teredoServer(addr); ok {
			if why := neverDevAllowedAddr(server); why != "" {
				return why
			}
		}
		if embedded, _, ok := extractV4(addr); ok {
			return neverDevAllowedAddr(embedded)
		}
	}
	return ""
}

// ParseDevPrefixes validates the local-development allowance: the same canonical-masked
// rule as ParsePrefixes, and in addition an entry that overlaps any never-allowed range is
// refused outright (not trimmed), so 0.0.0.0/0 or 169.254.0.0/16 cannot be written down.
func ParseDevPrefixes(raw []string) ([]netip.Prefix, error) {
	out, err := ParsePrefixes(raw)
	if err != nil {
		return nil, fmt.Errorf("dev %w", err)
	}
	for _, p := range out {
		for _, n := range neverDevAllowed {
			if p.Overlaps(n) {
				return nil, fmt.Errorf("egress dev allowed destination %s overlaps %s, which is never "+
					"allowed: that range holds the cloud metadata services, loopback, link-local, multicast "+
					"or the unspecified address. Name the single private address or the narrow LAN range "+
					"you mean", p, n)
			}
		}
		// Embedded forms: refuse a wrapper that carries a refused address at a known offset.
		if p.Addr().Is6() && p.Bits() <= 96 {
			if why := neverDevAllowedWrapper(p); why != "" {
				return nil, fmt.Errorf("egress dev allowed destination %s wraps %s, which is never allowed", p, why)
			}
		}
	}
	return out, nil
}

// neverDevAllowedWrapper catches a prefix that is wide enough to contain a wrapped
// never-allowed address (NAT64 / 6to4 / Teredo forms) without itself overlapping a refused
// IPv4 range, which the Overlaps loop cannot see because the wrapper lives in IPv6 space.
func neverDevAllowedWrapper(p netip.Prefix) string {
	for _, w := range []netip.Prefix{prefixNAT64WellKnown, prefix6to4, prefixTeredo, prefixV4Compatible} {
		if p.Overlaps(w) {
			return w.String() + " (an IPv6 form that embeds an IPv4 address)"
		}
	}
	return ""
}

// FormatDevPrefixes is the canonical comma-joined form used in warnings and listings.
func FormatDevPrefixes(p []netip.Prefix) string {
	s := make([]string, len(p))
	for i, x := range p {
		s[i] = x.String()
	}
	return strings.Join(s, ", ")
}
