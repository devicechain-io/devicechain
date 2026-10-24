// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// TrustedSourceAddr is the address r came from, believing X-Forwarded-For ONLY when the
// connection's own peer is one of the trusted proxies.
//
// It is for the endpoints that ration or attribute per source and must not be steered by a
// header any client can write. requestSourceAddr (SourceAddr, RequestSourceAddr) takes the
// last X-Forwarded-For entry from ANY peer, which is right behind a proxy that always
// appends and wrong everywhere else: a client that reaches the service directly, or through
// a proxy that does not append, chooses its own source. This one has the notion that was
// missing, a set of proxies whose word is accepted.
//
//   - The peer is outside trusted (or trusted is empty): the peer is the answer, and
//     X-Forwarded-For is ignored entirely.
//   - The peer is inside trusted: the X-Forwarded-For entries are walked from the RIGHT, and
//     the first one that is not itself a trusted proxy is the answer. Each hop appends the
//     address it received from, so the right-most entries are written by proxies the
//     operator named and everything to the left of the first untrusted one is only whatever
//     that untrusted hop, or the client, said.
//   - An entry that is not an address stops the walk and the peer is the answer. It cannot be
//     classified, and returning it would let a sender vary the key for free.
//   - Every entry trusted: the peer.
//
// Several X-Forwarded-For header lines read as one list, in order, as RFC 7239's
// predecessor practice treats them.
//
// A fairness key, not an identity: a determined sender behind a distinct trusted route can
// still be anyone the proxy believes. See SourceKey in core/sourcelimit for turning the
// result into a bucket key.
func TrustedSourceAddr(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, ok := parseAddr(host)
	if !ok || !inPrefixes(peer, trusted) {
		return host
	}
	var chain []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		chain = append(chain, strings.Split(line, ",")...)
	}
	for i := len(chain) - 1; i >= 0; i-- {
		entry := strings.TrimSpace(chain[i])
		if entry == "" {
			continue
		}
		a, ok := parseAddr(entry)
		if !ok {
			return host
		}
		if !inPrefixes(a, trusted) {
			return a.String()
		}
	}
	return host
}

// parseAddr reads an address as a proxy writes it into X-Forwarded-For: bare, bracketed, or
// with a port, an IPv4-mapped IPv6 address as the IPv4 address it is, and no zone.
func parseAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().WithZone("").Unmap(), true
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.WithZone("").Unmap(), true
}

func inPrefixes(a netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
