// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func addr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	require.NoError(t, err, "test fixture %q is not an address", s)
	return a
}

// TestBlockedAddresses is the deny table. Each case names WHY it is here, because a bare
// list of addresses decays into a list nobody dares to change.
func TestBlockedAddresses(t *testing.T) {
	g := NewGuard(nil)
	cases := []struct{ addr, why string }{
		{"127.0.0.1", "loopback"},
		{"127.1.2.3", "the whole loopback /8, not just .0.1"},
		{"10.1.2.3", "RFC1918"},
		{"172.16.0.1", "RFC1918"},
		{"172.31.255.254", "the top of the RFC1918 /12 — a /16 here would miss it"},
		{"192.168.1.1", "RFC1918"},
		{"169.254.169.254", "the metadata address on AWS, GCP, Oracle, DigitalOcean and IBM"},
		{"169.254.1.1", "link-local generally, not just the metadata address"},
		{"100.100.100.200", "Alibaba Cloud metadata — CGNAT, which its own docs miscall link-local"},
		{"100.64.0.1", "CGNAT generally"},
		{"0.0.0.0", "the unspecified address, which Linux rewrites to loopback"},
		{"0.1.2.3", "the rest of 0.0.0.0/8 — an illegal destination Linux still puts on the wire"},
		{"224.0.0.1", "multicast, which is absent from the IANA v4 registry"},
		{"239.255.255.250", "SSDP multicast"},
		{"255.255.255.255", "broadcast"},
		{"240.0.0.1", "reserved class E"},
		{"192.0.2.1", "TEST-NET-1"},
		{"198.51.100.1", "TEST-NET-2"},
		{"203.0.113.1", "TEST-NET-3"},
		{"198.18.0.1", "benchmarking"},
		{"192.0.0.1", "IETF protocol assignments"},

		{"::1", "IPv6 loopback"},
		{"::", "IPv6 unspecified"},
		{"fe80::1", "IPv6 link-local"},
		{"fc00::1", "unique-local"},
		{"fd00:ec2::254", "the AWS IPv6 metadata address, which is unique-local"},
		{"fd20:ce::254", "the GCP IPv6 metadata address"},
		{"ff02::1", "IPv6 multicast"},
		{"2001:db8::1", "IPv6 documentation"},
	}
	for _, c := range cases {
		t.Run(c.addr, func(t *testing.T) {
			err := g.CheckAddr(addr(t, c.addr))
			require.Error(t, err, "must be refused: %s", c.why)
			assert.ErrorIs(t, err, ErrBlocked, "every refusal must carry the sentinel so callers can classify it terminal")
		})
	}
}

// 🔴 The Azure wireserver is the one address no range check produces. It is ordinary
// global unicast inside a Microsoft allocation, so every categorical test above passes it
// and only the explicit entry refuses it. If this test ever fails because someone tidied
// the "redundant" /32 out of the table, that is the bug.
func TestAzureWireserverIsBlockedThoughItIsPublicUnicast(t *testing.T) {
	a := addr(t, "168.63.129.16")

	require.False(t, a.IsPrivate(), "precondition: it is not private")
	require.False(t, a.IsLoopback(), "precondition: it is not loopback")
	require.False(t, a.IsLinkLocalUnicast(), "precondition: it is not link-local")
	require.False(t, a.IsMulticast(), "precondition: it is not multicast")

	assert.ErrorIs(t, NewGuard(nil).CheckAddr(a), ErrBlocked)

	// Its neighbours are ordinary Microsoft-allocated space and must NOT be refused —
	// the entry is a /32 on purpose, because an over-broad deny costs a false refusal
	// with no recourse for the tenant.
	assert.NoError(t, NewGuard(nil).CheckAddr(addr(t, "168.63.129.15")))
	assert.NoError(t, NewGuard(nil).CheckAddr(addr(t, "168.63.129.17")))
}

// 🔴 The two documented netip.Prefix.Contains fail-opens. These are the reason the guard
// normalizes before comparing, and they are the tests most likely to be "simplified" by
// someone who does not know that. Each asserts the raw Contains really does fail open, so
// the normalization is shown to be load-bearing rather than asserted to be.
func TestZonedIPv6DoesNotEscape(t *testing.T) {
	// 🔴 THE FIXTURE IS THE TEST, and the obvious one is worthless. fe80::1%eth0 is
	// caught by IsLinkLocalUnicast, which ignores zones — so it passes with or without
	// the normalization, and a mutation removing WithZone("") survives. Mutation testing
	// found exactly that.
	//
	// The address that actually depends on it is one only the PREFIX TABLE refuses.
	// 2001:db8::1 is in no categorical class, so with a zone attached Contains returns
	// false, no other check fires, and the guard says yes. Go accepts
	// http://[2001:db8::1%25eth0]/ and dials [2001:db8::1%eth0]:80.
	zoned := addr(t, "2001:db8::1").WithZone("eth0")

	require.False(t, netip.MustParsePrefix("2001:db8::/32").Contains(zoned),
		"precondition: Prefix.Contains fails open on a zoned address — if this ever becomes "+
			"true, Go changed and this guard's normalization can be revisited")
	// "Refused only by the table" stated as a property rather than asserted in prose:
	// every categorical predicate must be false for this address, so the table is the
	// only thing that can catch it — and the table is the only check a zone defeats.
	bare := addr(t, "2001:db8::1")
	require.False(t, bare.IsPrivate() || bare.IsLoopback() || bare.IsLinkLocalUnicast() ||
		bare.IsMulticast() || bare.IsUnspecified(),
		"precondition: this fixture must fall into no categorical class")
	require.ErrorIs(t, NewGuard(nil).CheckAddr(bare), ErrBlocked,
		"precondition: unzoned, the table refuses it")

	assert.ErrorIs(t, NewGuard(nil).CheckAddr(zoned), ErrBlocked)

	// The categorical case too, so the coverage is not narrowed by the fix — it just no
	// longer stands in for the case it could not detect.
	assert.ErrorIs(t, NewGuard(nil).CheckAddr(addr(t, "fe80::1").WithZone("eth0")), ErrBlocked)
}

func TestIPv4MappedIPv6DoesNotEscape(t *testing.T) {
	mapped := netip.AddrFrom16(addr(t, "::ffff:169.254.169.254").As16())

	require.False(t, netip.MustParsePrefix("169.254.0.0/16").Contains(mapped),
		"precondition: an IPv4-mapped address does not match an IPv4 prefix")

	assert.ErrorIs(t, NewGuard(nil).CheckAddr(mapped), ErrBlocked)
}

// The IPv6 forms that carry an IPv4 address. Each wraps 169.254.169.254, so a guard that
// judged the wrapper instead of the payload would let every one of them through.
func TestIPv6FormsEmbeddingAPrivateIPv4AreBlocked(t *testing.T) {
	g := NewGuard(nil)
	cases := map[string]string{
		"NAT64 well-known":           "64:ff9b::a9fe:a9fe",
		"NAT64 local-use":            "64:ff9b:1::a9fe:a9fe",
		"6to4, offset 2-5 not 12-15": "2002:a9fe:a9fe::1",
		"IPv4-compatible":            "::169.254.169.254",
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			assert.ErrorIs(t, g.CheckAddr(addr(t, s)), ErrBlocked)
		})
	}
}

// Teredo obfuscates the client address by inverting every bit, so the bytes on the wire
// for 169.254.169.254 are 56:01:56:01 and a guard reading them literally sees a public
// address. This fixture is built by inverting rather than pasted, so the test cannot
// drift from the transformation it is checking.
func TestTeredoClientAddressIsDeobfuscated(t *testing.T) {
	var b [16]byte
	copy(b[:4], []byte{0x20, 0x01, 0x00, 0x00}) // the 2001::/32 prefix
	copy(b[4:8], []byte{8, 8, 8, 8})            // a public relay server
	target := addr(t, "169.254.169.254").As4()
	for i, v := range target {
		b[12+i] = ^v
	}
	teredo := netip.AddrFrom16(b)

	require.NotEqual(t, target[:], b[12:16], "precondition: the fixture is obfuscated, not literal")
	assert.ErrorIs(t, NewGuard(nil).CheckAddr(teredo), ErrBlocked)
}

// The Teredo SERVER address is a second reachable address in the same 128 bits, in the
// clear at bytes 4-7. A guard that only de-obfuscated the client would miss it.
func TestTeredoServerAddressIsAlsoChecked(t *testing.T) {
	var b [16]byte
	copy(b[:4], []byte{0x20, 0x01, 0x00, 0x00})
	copy(b[4:8], []byte{169, 254, 169, 254}) // the SERVER is the private one here
	// Bytes 8-11 are the flags and the obfuscated port. They are given a value on
	// purpose: left zero, a guard reading the server from the WRONG offset would extract
	// 0.0.0.0, refuse it, and the test would pass while proving nothing about where the
	// server address is. Mutation testing found that — an offset of 8 survived.
	copy(b[8:12], []byte{0x80, 0x00, 0x12, 0x34})
	public := addr(t, "93.184.216.34").As4()
	for i, v := range public {
		b[12+i] = ^v // the client is public, so only the server can refuse this
	}

	err := NewGuard(nil).CheckAddr(netip.AddrFrom16(b))
	require.ErrorIs(t, err, ErrBlocked)
	assert.Contains(t, err.Error(), "Teredo server", "the refusal must name which half was bad")
}

// The counterweight, and it matters as much as the deny cases: a boundary that refuses
// everything is not a boundary, it is an outage. Ordinary public addresses must pass, and
// so must the IPv6 wrappers when what they wrap is public.
func TestPublicAddressesArePermitted(t *testing.T) {
	g := NewGuard(nil)
	for _, s := range []string{
		"93.184.216.34",
		"1.1.1.1",
		"8.8.8.8",
		"168.63.129.15",
		"2606:2800:220:1:248:1893:25c8:1946",
		"2001:4860:4860::8888",
		"64:ff9b::5db8:d822", // NAT64 wrapping 93.184.216.34
		"2002:5db8:d822::1",  // 6to4 wrapping 93.184.216.34
		"100:0:0:2::1",       // adjacent to the discard /64, not in it
	} {
		t.Run(s, func(t *testing.T) {
			assert.NoError(t, g.CheckAddr(addr(t, s)))
		})
	}
}

func TestAllowListOverridesTheDenyList(t *testing.T) {
	blocked := addr(t, "10.1.2.3")
	require.Error(t, NewGuard(nil).CheckAddr(blocked), "precondition: refused without the allowance")

	g := NewGuard([]netip.Prefix{netip.MustParsePrefix("10.1.2.3/32")})
	assert.NoError(t, g.CheckAddr(blocked))
	assert.Error(t, g.CheckAddr(addr(t, "10.1.2.4")), "a /32 allowance must not widen to its neighbours")
}

// An allow list the caller keeps a reference to must not be able to widen a live boundary
// afterwards. This is the kind of aliasing that is invisible until it is a vulnerability.
func TestAllowListIsCopied(t *testing.T) {
	allow := []netip.Prefix{netip.MustParsePrefix("10.1.2.3/32")}
	g := NewGuard(allow)
	allow[0] = netip.MustParsePrefix("0.0.0.0/0")
	assert.Error(t, g.CheckAddr(addr(t, "192.168.1.1")))
}

func TestControlContextRefusesAnUnparseableAddress(t *testing.T) {
	err := NewGuard(nil).ControlContext(context.Background(), "tcp", "not-an-address", nil)
	require.Error(t, err, "an address the guard cannot read must be refused, not waved through")
	assert.ErrorIs(t, err, ErrBlocked)
}

func TestControlContextParsesTheFormsGoActuallyDelivers(t *testing.T) {
	g := NewGuard(nil)
	for _, s := range []string{"169.254.169.254:80", "[fe80::1]:80", "[fe80::1%eth0]:80", "[::1]:443"} {
		t.Run(s, func(t *testing.T) {
			assert.ErrorIs(t, g.ControlContext(context.Background(), "tcp", s, nil), ErrBlocked)
		})
	}
	assert.NoError(t, g.ControlContext(context.Background(), "tcp", "93.184.216.34:443", nil))
}

// 🔴 The proxy setting is a security property, not a performance one: http.DefaultTransport
// honours HTTPS_PROXY, and the chart lets an operator set arbitrary env per area. With a
// proxy configured, every dial goes to the proxy's (public) address and the proxy fetches
// whatever the URL said — the guard would pass everything and log nothing.
func TestTransportDoesNotHonourProxyEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:3128")

	tr := NewGuard(nil).Transport()
	require.Nil(t, tr.Proxy, "Proxy must be nil explicitly, not left to the default")

	// The comparison that gives the assertion above its meaning: http.DefaultTransport
	// DOES route through the environment, so "nil" here is a decision this code made
	// rather than a property of transports in general.
	req, err := http.NewRequest(http.MethodGet, "http://example.com/x", nil)
	require.NoError(t, err)
	def, ok := http.DefaultTransport.(*http.Transport)
	require.True(t, ok)
	viaEnv, err := def.Proxy(req)
	require.NoError(t, err)
	assert.NotNil(t, viaEnv, "precondition: the default transport honours HTTP_PROXY, so "+
		"inheriting it would have sent every tenant request to the proxy and passed the guard")
}

// The end-to-end claim: refused BEFORE a byte is written. A real listener is started on
// loopback and the guarded client must fail without the handler ever running — an
// assertion about the server's state, not about the error text.
func TestTheConnectionIsRefusedBeforeAnyByteIsWritten(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewGuard(nil).Transport()}
	resp, err := client.Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}

	require.Error(t, err, "a loopback destination must be refused")
	assert.ErrorIs(t, err, ErrBlocked, "the sentinel must survive net/http's error wrapping")
	assert.False(t, reached, "the handler ran, so bytes reached the destination")
}

// And the counterweight to that one: the same guarded transport reaches a server it is
// allowed to reach. Without this, a Transport that refused every connection for an
// unrelated reason would pass the test above.
func TestTheGuardedTransportStillReachesAnAllowedServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	host, _, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	allow := []netip.Prefix{netip.PrefixFrom(addr(t, host), addr(t, host).BitLen())}

	client := &http.Client{Transport: NewGuard(allow).Transport()}
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// BlockedError has to keep working through errors.As as well as errors.Is, because the
// address it carries is the only place the RESOLVED address is available to a caller
// building an operator-facing message.
func TestBlockedErrorCarriesTheResolvedAddress(t *testing.T) {
	err := NewGuard(nil).CheckAddr(addr(t, "169.254.169.254"))

	var be *BlockedError
	require.True(t, errors.As(err, &be))
	assert.Equal(t, "169.254.169.254", be.Addr.String())
	assert.Contains(t, be.Error(), "link-local")
}

// EVERY row of the deny table is actually refused, rather than the sample above.
//
// 🔴 THIS EXISTS BECAUSE THE DRIFT GATE COMPARES THE TABLE AND NOT THE DECISION.
// hack/check-egress-ranges.sh relates `denied` to the chart's NetworkPolicy, so it
// notices a prefix leaving the table — but a prefix can stay in the table while the
// guard stops consulting it, and the gate reads exactly the same bytes either way.
// A review made that concrete: adding `continue` for one row inside the guard's loop
// left the gate green AND this package's tests green, because TestBlockedAddresses
// samples. Seven rows had no case at all: 192.88.99.0/24, 64:ff9b:1::/48, 100::/64,
// 100:0:0:1::/64, 2001:2::/48, 3fff::/20 and 5f00::/16.
//
// Sampling was the right shape for that test — its cases carry the REASONS, and a
// table-driven sweep cannot explain why 100.100.100.200 matters. So this does not
// replace it; it makes the link total, and the two together mean a prefix cannot be
// in the table and unenforced.
//
// The addresses tested are derived from each prefix, so this needs no per-row fixture
// that could drift from the row.
//
// 🔴 TWO ADDRESSES PER ROW, AND THE SECOND ONE IS WHY THIS TEST WORKS. The network
// address alone is not enough: for 0.0.0.0/8 it is 0.0.0.0, which the categorical
// IsUnspecified() check refuses BEFORE the table is consulted — so deleting that row from
// the guard's loop left this sweep green while 0.1.2.3 became reachable. Thirteen of the
// probes are shadowed by a categorical check that way; for twelve of them the categorical
// check covers the whole prefix and skipping the row is harmless, and 0.0.0.0/8 was the
// one where it is not. The second address is outside every such shadow.
func TestEveryDeniedPrefixIsRefused(t *testing.T) {
	g := NewGuard(nil)
	prefixes := DeniedPrefixes()
	if len(prefixes) == 0 {
		t.Fatal("precondition: the deny table is empty, so this test asserts nothing")
	}
	for _, p := range prefixes {
		masked := p.Masked()
		probes := []netip.Addr{masked.Addr()}
		// A single-address prefix has no second member; anything else does, and
		// Next() of the network address is inside the prefix by construction.
		if masked.Bits() < masked.Addr().BitLen() {
			probes = append(probes, masked.Addr().Next())
		}
		for _, addr := range probes {
			if err := g.CheckAddr(addr); err == nil {
				t.Errorf("the deny table carries %s but the guard ADMITS %s — the table and the "+
					"decision have come apart, and the drift gate cannot see that because it "+
					"reads the table", p, addr)
			}
		}
	}
}

// The counterweight: the sweep above is only meaningful while the guard still admits
// ordinary public addresses. A guard that refused everything would satisfy it perfectly.
func TestTheDenySweepIsNotVacuous(t *testing.T) {
	g := NewGuard(nil)
	for _, addr := range []string{"1.1.1.1", "8.8.8.8", "93.184.216.34", "2606:4700:4700::1111"} {
		if err := g.CheckAddr(netip.MustParseAddr(addr)); err != nil {
			t.Errorf("the guard refuses the ordinary public address %s (%v); "+
				"TestEveryDeniedPrefixIsRefused would pass for the wrong reason", addr, err)
		}
	}
}
