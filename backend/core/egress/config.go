// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/rs/zerolog/log"
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
	dev, err := ParseDevPrefixes(cfg.DevAllowedDestinations)
	if err != nil {
		return nil, err
	}
	g := NewGuard(prefixes)
	if len(dev) > 0 {
		// Loud on purpose: this is a development-only relaxation, and a production log that
		// carries this line is the fastest way to find an instance that should not have it.
		log.Warn().Str("devAllowedDestinations", FormatDevPrefixes(dev)).
			Msg("DEVELOPMENT EGRESS ALLOWANCE ACTIVE: tenant webhooks and connectors may reach these private " +
				"destinations. This is for a local development cluster only; the cloud metadata, loopback and " +
				"link-local ranges stay blocked")
		g = g.WithDevAllow(dev)
	}
	return g, nil
}

// ParsePrefixes validates operator-supplied CIDR strings.
//
// A prefix is rejected unless it is in canonical masked form — `10.1.2.0/24`, never
// `10.1.2.3/24`. netip.ParsePrefix accepts the second and silently ignores the host bits,
// so an operator who meant to allow one address and mistyped the length would grant a
// whole /24 with nothing anywhere to show it. That is the error worth being pedantic
// about: it is silent, it is in the widening direction, and the written config still
// reads like the narrow grant that was intended.
//
// It widens more than it looks like it does, because an allow entry is not a grant — it
// is an EXEMPTION from `denied`, checked before every refusal the guard can make. A /24
// mistyped here can therefore re-open private space to a tenant, which is the whole
// boundary, from a line that reads as a single host.
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
