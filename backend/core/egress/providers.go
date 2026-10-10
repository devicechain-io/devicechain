// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"fmt"
	"net/netip"
)

// MetadataProvider is one hosting provider's instance-metadata surface: the addresses a workload
// on that provider can read credentials from, and that a tenant destination must therefore never
// reach. The deny table in ranges.go is the boundary; this table is its OWNER, the thing that
// answers "which provider is each row there for, and is any provider uncovered?". Without it the
// set's completeness depended on whoever last remembered a provider.
//
// A provider is either covered (Addresses non-empty, each refused by the guard) or explicitly
// Unverified with the reason it is not, so an absent provider can never be mistaken for a
// covered one.
type MetadataProvider struct {
	Name      string
	Addresses []netip.Addr
	// Unverified is non-empty exactly when Addresses is empty: why no address is denied for this
	// provider yet.
	Unverified string
}

func addrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, 0, len(s))
	for _, a := range s {
		out = append(out, netip.MustParseAddr(a))
	}
	return out
}

// MetadataProviders is the provider table. Add a provider here BEFORE (or with) the deny row that
// covers it; check-egress-ranges.sh --providers fails when a listed provider's address is admitted
// or a provider has neither addresses nor a stated reason.
var MetadataProviders = []MetadataProvider{
	// 169.254.170.2 is the ECS task credentials endpoint; 169.254.170.23 and fd00:ec2::23 are the EKS
	// Pod Identity agent (all checked against AWS documentation).
	{Name: "aws", Addresses: addrs("169.254.169.254", "fd00:ec2::254", "169.254.170.2", "169.254.170.23", "fd00:ec2::23")},
	{Name: "gcp", Addresses: addrs("169.254.169.254", "fd20:ce::254")},
	{Name: "azure", Addresses: addrs("169.254.169.254", "168.63.129.16")},
	// Oracle's current documentation gives 169.254.169.254 only; 192.0.0.192 (an older documented
	// address) is dropped because it is no longer documented, and is denied anyway as part of
	// 192.0.0.0/24.
	{Name: "oracle", Addresses: addrs("169.254.169.254")},
	{Name: "digitalocean", Addresses: addrs("169.254.169.254")},
	{Name: "ibm", Addresses: addrs("169.254.169.254")},
	{Name: "alibaba", Addresses: addrs("100.100.100.200")},
	{Name: "linode", Addresses: addrs("169.254.169.254")},
	{Name: "hetzner", Addresses: addrs("169.254.169.254")},
	{Name: "vultr", Addresses: addrs("169.254.169.254")},
	{Name: "scaleway", Addresses: addrs("169.254.42.42")},
	{Name: "openstack", Addresses: addrs("169.254.169.254", "fe80::a9fe:a9fe")},
	{
		Name: "equinix-metal",
		Unverified: "Equinix Metal reached end of life on 2026-06-30 and its resources were removed on " +
			"2026-07-01; its metadata hostname resolved to a PUBLIC address that no range row covers, and " +
			"there is no reason to ever deny a public address for a service that no longer exists",
	},
}

// ProviderGaps reports, for the given table, every provider the guard does not cover: one with
// neither addresses nor a stated reason, one with both, and every listed address the guard admits.
// Empty means the table and the guard agree.
func ProviderGaps(table []MetadataProvider, g *Guard) []string {
	var gaps []string
	for _, p := range table {
		switch {
		case len(p.Addresses) == 0 && p.Unverified == "":
			gaps = append(gaps, fmt.Sprintf("%s: no address and no stated reason", p.Name))
		case len(p.Addresses) > 0 && p.Unverified != "":
			gaps = append(gaps, fmt.Sprintf("%s: lists addresses AND is marked unverified", p.Name))
		}
		for _, a := range p.Addresses {
			if err := g.CheckAddr(a); err == nil {
				gaps = append(gaps, fmt.Sprintf("%s: the guard admits its metadata address %s", p.Name, a))
			}
		}
	}
	return gaps
}
