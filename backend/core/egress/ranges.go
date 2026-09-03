// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package egress

import "net/netip"

// denied is the address space a tenant destination may never resolve into.
//
// # Why this is written out rather than derived
//
// The obvious construction is to take IANA's special-purpose registries and keep every
// row whose "Globally Reachable" column is False. That construction is wrong, and
// wrong in the direction that matters: three of the six IPv6 forms that carry an IPv4
// address inside them escape it. 64:ff9b::/96 is marked Globally Reachable **True**,
// and 2001::/32 (Teredo) and 2002::/16 (6to4) are marked **N/A**. IPv4 multicast is not
// in the registry at all. A list generated from that filter looks complete, passes
// review, and admits an address that reaches 169.254.169.254.
//
// # Why it is not fetched
//
// A boundary that depends on a network fetch is not a boundary — it fails open on the
// day the fetch fails, which is the day everything else is going wrong too. The cost is
// that this table drifts as the registries change. That is a maintenance item with a
// date on it, not a silent failure, and it is the trade worth making.
var denied = []struct {
	prefix netip.Prefix
	why    string
}{
	// ---- IPv4 ----------------------------------------------------------------
	// The whole /8, not just 0.0.0.0/32. RFC 1122 §3.2.1.3 makes every address here an
	// illegal destination. Note what is NOT the reason: Linux does not route 0.x.y.z to
	// loopback — net/ipv4/route.c tests for a full 32-bit zero, so only 0.0.0.0 itself
	// is rewritten. The rest is emitted onto the wire toward the default gateway, which
	// is an egress nobody authorized either.
	{netip.MustParsePrefix("0.0.0.0/8"), "0.0.0.0/8, which is not a legal destination"},
	{netip.MustParsePrefix("10.0.0.0/8"), "the 10.0.0.0/8 private range"},
	// CGNAT. Alibaba Cloud's metadata service lives at 100.100.100.200, which is in
	// here — and Alibaba's own documentation calls it "a link-local address", which it
	// is not. A deny list built from that vendor's wording plus 169.254.0.0/16 misses
	// it entirely.
	{netip.MustParsePrefix("100.64.0.0/10"), "the 100.64.0.0/10 shared address space"},
	{netip.MustParsePrefix("127.0.0.0/8"), "the loopback range"},
	// Link-local, and the address every major cloud puts its instance metadata service
	// on: AWS, GCP, Oracle, DigitalOcean and IBM all answer at 169.254.169.254.
	{netip.MustParsePrefix("169.254.0.0/16"), "the link-local range"},
	{netip.MustParsePrefix("172.16.0.0/12"), "the 172.16.0.0/12 private range"},
	{netip.MustParsePrefix("192.0.0.0/24"), "the IETF protocol assignments range"},
	{netip.MustParsePrefix("192.0.2.0/24"), "a documentation range"},
	{netip.MustParsePrefix("192.88.99.0/24"), "the deprecated 6to4 relay anycast range"},
	{netip.MustParsePrefix("192.168.0.0/16"), "the 192.168.0.0/16 private range"},
	{netip.MustParsePrefix("198.18.0.0/15"), "the benchmarking range"},
	{netip.MustParsePrefix("198.51.100.0/24"), "a documentation range"},
	{netip.MustParsePrefix("203.0.113.0/24"), "a documentation range"},
	{netip.MustParsePrefix("224.0.0.0/4"), "the multicast range"},
	// Covers 255.255.255.255 as well as the reserved class E space.
	{netip.MustParsePrefix("240.0.0.0/4"), "the reserved 240.0.0.0/4 range"},

	// 🔴 The one entry no range check would ever produce. Azure's wireserver is at
	// 168.63.129.16, and it is ORDINARY GLOBAL UNICAST — Microsoft calls it "a virtual
	// public IP address … used in all regions and all national clouds", and ARIN has it
	// inside a direct allocation to Microsoft (168.61.0.0–168.63.255.255). It sits in
	// none of the special-purpose rows above. It serves platform goal-state and
	// credentials on ports 80 and 32526, so the address is denied rather than a port
	// set.
	//
	// This is a /32 on purpose. The polarity of a deny list is that an over-broad entry
	// costs a false DENY — a legitimate tenant webhook refused with no recourse. A
	// single address Microsoft states is reserved everywhere costs nothing to deny. A
	// /16 of ordinary allocated space would not be free, which is why IBM's documented
	// VPC-internal ranges (161.26.0.0/16, 166.8.0.0/14) are NOT here: they are the same
	// shape of hazard, but denying them by default refuses real hosts to defend against
	// a service we did not confirm is listening. They belong in an operator's deny
	// configuration, and they are named here so the next person does not have to
	// rediscover them.
	{netip.MustParsePrefix("168.63.129.16/32"), "the Azure platform wireserver address"},

	// ---- IPv6 ----------------------------------------------------------------
	{netip.MustParsePrefix("::1/128"), "the IPv6 loopback address"},
	{netip.MustParsePrefix("::/128"), "the unspecified address"},
	// Reached only if Unmap did not already flatten it — belt and braces, because the
	// cost of being wrong here is an allowed 169.254.169.254.
	{netip.MustParsePrefix("::ffff:0:0/96"), "the IPv4-mapped range"},
	// 🔴 NAT64 local-use. Unlike the well-known prefix, the embedded IPv4 address sits
	// at an offset that depends on a prefix length (32/40/48/56/64/96) we cannot know
	// from the address alone — RFC 6052 §2.2. There is no way to extract and judge the
	// address that will actually be reached, so the whole /48 is refused. Guessing an
	// offset would be worse than refusing: it would produce a confident answer about
	// the wrong four bytes.
	{netip.MustParsePrefix("64:ff9b:1::/48"), "the NAT64 local-use range, whose embedded address cannot be located"},
	{netip.MustParsePrefix("100::/64"), "the discard-only range"},
	{netip.MustParsePrefix("100:0:0:1::/64"), "the dummy IPv6 prefix"},
	{netip.MustParsePrefix("2001:2::/48"), "the benchmarking range"},
	{netip.MustParsePrefix("2001:db8::/32"), "a documentation range"},
	{netip.MustParsePrefix("3fff::/20"), "a documentation range"},
	{netip.MustParsePrefix("5f00::/16"), "the segment-routing SID range"},
	{netip.MustParsePrefix("fc00::/7"), "the unique-local range"},
	{netip.MustParsePrefix("fe80::/10"), "the IPv6 link-local range"},
	{netip.MustParsePrefix("ff00::/8"), "the IPv6 multicast range"},
}

// The IPv6 forms that carry an IPv4 address at a known offset. Each is extracted and the
// embedded address judged on its own terms, rather than the wrapper being denied
// wholesale — a NAT64 or 6to4 address wrapping a public host is a legitimate
// destination, and refusing it would be a false deny.
var (
	prefixV4Compatible   = netip.MustParsePrefix("::/96")
	prefixNAT64WellKnown = netip.MustParsePrefix("64:ff9b::/96")
	prefix6to4           = netip.MustParsePrefix("2002::/16")
	prefixTeredo         = netip.MustParsePrefix("2001::/32")
)

// extractV4 returns the IPv4 address embedded in an IPv6 address, if there is one at a
// known offset, along with a word naming the form for the refusal message.
//
// IPv4-mapped (::ffff:0:0/96) is absent on purpose: Unmap has already turned those into
// IPv4 addresses before this is reached, and Go's own formatting flattens them before
// ControlContext ever sees one.
func extractV4(addr netip.Addr) (netip.Addr, string, bool) {
	b := addr.As16()
	switch {
	case prefixNAT64WellKnown.Contains(addr):
		// RFC 6052 §2.1: for a /96 prefix the IPv4 address is the trailing 32 bits.
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), "NAT64", true
	case prefix6to4.Contains(addr):
		// RFC 3056 §2: 2002:V4ADDR::/48 — the address begins immediately after the
		// 16-bit prefix, NOT at the end like every other form here.
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), "6to4", true
	case prefixTeredo.Contains(addr):
		// RFC 4380 §4: the client's IPv4 address is the trailing 32 bits with every bit
		// inverted. The relay server's address sits at bytes 4-7 in the clear; the caller
		// checks it separately via teredoServer, since only one address can be returned
		// from here.
		return netip.AddrFrom4([4]byte{^b[12], ^b[13], ^b[14], ^b[15]}), "Teredo client", true
	case prefixV4Compatible.Contains(addr):
		// The deprecated IPv4-compatible form, and also where ::1 and :: land — both of
		// which extract to addresses the IPv4 table already refuses.
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), "IPv4-compatible", true
	}
	return netip.Addr{}, "", false
}

// teredoServer returns the plaintext server IPv4 address a Teredo address carries at
// bytes 4-7, which is a SECOND reachable address inside one IPv6 address. Checking only
// the client address would leave half the form unexamined.
func teredoServer(addr netip.Addr) (netip.Addr, bool) {
	if !prefixTeredo.Contains(addr) {
		return netip.Addr{}, false
	}
	b := addr.As16()
	return netip.AddrFrom4([4]byte{b[4], b[5], b[6], b[7]}), true
}
