// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

// The source address a resolver sees is the nearest hop's: the peer when nothing
// proxied the request, the LAST X-Forwarded-For entry when something did (the earlier
// entries are whatever the client sent).
func TestSourceAddrIsTheNearestHop(t *testing.T) {
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.RemoteAddr = "192.0.2.10:5555"
	require.Equal(t, "192.0.2.10", requestSourceAddr(r))

	r.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.7")
	require.Equal(t, "198.51.100.7", requestSourceAddr(r), "a client-supplied leading entry must not win")

	r.Header.Set("X-Forwarded-For", " ")
	require.Equal(t, "192.0.2.10", requestSourceAddr(r))
}

func req(peer string, xff ...string) *http.Request {
	r := httptest.NewRequest("POST", "/provisioning/register", nil)
	r.RemoteAddr = peer
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func prefixes(t *testing.T, cidrs ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, c := range cidrs {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}

// A spoofed X-Forwarded-For from a peer that is not a trusted proxy is ignored: the client
// reaching the service directly cannot choose its source.
func TestTrustedSourceAddrIgnoresASpoofedHeaderFromAnUntrustedPeer(t *testing.T) {
	trusted := prefixes(t, "10.0.0.0/8")
	require.Equal(t, "203.0.113.9", TrustedSourceAddr(req("203.0.113.9:4000", "198.51.100.7"), trusted))
	// Even the shape requestSourceAddr believes.
	require.Equal(t, "198.51.100.7", requestSourceAddr(req("203.0.113.9:4000", "198.51.100.7")),
		"control: the existing helper does believe it, so the test above proves the difference")
}

// From a trusted peer the right-most entry that is not itself a trusted proxy is the client.
func TestTrustedSourceAddrTakesTheRightmostUntrustedEntry(t *testing.T) {
	trusted := prefixes(t, "10.0.0.0/8", "fd00::/8")
	// client-supplied "6.6.6.6" on the left must not win; the ingress appended the real address.
	require.Equal(t, "198.51.100.7", TrustedSourceAddr(req("10.1.2.3:80", "6.6.6.6, 198.51.100.7"), trusted))
	// A second trusted hop (a CDN or an inner proxy) is walked through.
	require.Equal(t, "198.51.100.7", TrustedSourceAddr(req("10.1.2.3:80", "6.6.6.6, 198.51.100.7, 10.9.9.9"), trusted))
	// Several header lines read as one list.
	require.Equal(t, "198.51.100.7", TrustedSourceAddr(req("10.1.2.3:80", "6.6.6.6", "198.51.100.7, 10.9.9.9"), trusted))
	// An entry with a port, a bracketed IPv6 address and a mapped IPv4 address are read.
	require.Equal(t, "198.51.100.7", TrustedSourceAddr(req("10.1.2.3:80", "198.51.100.7:51000"), trusted))
	require.Equal(t, "2001:db8::5", TrustedSourceAddr(req("[fd00::1]:80", "[2001:db8::5]:443"), trusted))
	require.Equal(t, "198.51.100.7", TrustedSourceAddr(req("10.1.2.3:80", "::ffff:198.51.100.7"), trusted))
}

// With an empty or nil trusted list, the peer is used and the header never read.
func TestTrustedSourceAddrWithNoTrustedProxiesUsesThePeer(t *testing.T) {
	r := req("10.1.2.3:80", "198.51.100.7")
	require.Equal(t, "10.1.2.3", TrustedSourceAddr(r, nil))
	require.Equal(t, "10.1.2.3", TrustedSourceAddr(r, []netip.Prefix{}))
}

// A trusted peer that sent nothing usable is its own answer: no header, an empty one, all
// entries trusted, or an entry that is not an address (which a sender could vary for free).
func TestTrustedSourceAddrFallsBackToThePeer(t *testing.T) {
	trusted := prefixes(t, "10.0.0.0/8")
	require.Equal(t, "10.1.2.3", TrustedSourceAddr(req("10.1.2.3:80"), trusted))
	require.Equal(t, "10.1.2.3", TrustedSourceAddr(req("10.1.2.3:80", " "), trusted))
	require.Equal(t, "10.1.2.3", TrustedSourceAddr(req("10.1.2.3:80", "10.4.4.4, 10.5.5.5"), trusted))
	require.Equal(t, "10.1.2.3", TrustedSourceAddr(req("10.1.2.3:80", "198.51.100.7, not-an-address"), trusted))
}

// A peer without a port and an unparseable one still produce an answer, never a panic.
func TestTrustedSourceAddrToleratesAnOddPeer(t *testing.T) {
	trusted := prefixes(t, "10.0.0.0/8")
	require.Equal(t, "198.51.100.7", TrustedSourceAddr(req("10.1.2.3", "198.51.100.7"), trusted))
	require.Equal(t, "pipe", TrustedSourceAddr(req("pipe", "198.51.100.7"), trusted))
}
