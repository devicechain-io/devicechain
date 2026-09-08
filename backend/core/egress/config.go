// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/devicechain-io/dc-microservice/config"
)

// FromConfig builds a guard from an instance's egress configuration.
//
// It fails closed on a malformed prefix rather than skipping it. A typo'd CIDR that was
// silently dropped would produce a guard refusing the destination the operator believed
// they had allowed, and the only symptom would be deliveries failing for a reason the
// configuration appears to have already handled.
func FromConfig(cfg config.EgressConfiguration) (*Guard, error) {
	prefixes, err := ParsePrefixes(cfg.AllowedDestinations)
	if err != nil {
		return nil, err
	}
	return NewGuard(prefixes), nil
}

// ParsePrefixes validates operator-supplied CIDR strings.
//
// A prefix is rejected unless it is in canonical masked form — `10.1.2.0/24`, never
// `10.1.2.3/24`. netip.ParsePrefix accepts the second and silently ignores the host bits,
// so an operator who meant to allow one address and mistyped the length would grant a
// whole /24 with nothing anywhere to show it. On a deny-by-default boundary that is the
// error worth being pedantic about: it is silent, it is in the widening direction, and
// the written config still reads like the narrow grant that was intended.
func ParsePrefixes(raw []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(raw))
	for _, entry := range raw {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		p, err := netip.ParsePrefix(trimmed)
		if err != nil {
			return nil, fmt.Errorf("egress allowed destination %q is not a CIDR prefix "+
				"(want a form like 10.1.2.3/32 or fd00::1/128): %w", entry, err)
		}
		if p.Masked() != p {
			return nil, fmt.Errorf("egress allowed destination %q has bits set below its prefix "+
				"length, so it grants %s — a wider range than it reads as. Write it masked, or use "+
				"/%d if you meant the single address", entry, p.Masked(), p.Addr().BitLen())
		}
		out = append(out, p)
	}
	return out, nil
}
