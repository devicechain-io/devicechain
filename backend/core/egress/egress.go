// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package egress refuses a connection to an address a tenant should not be able to
// reach, at the moment the kernel is about to connect to it.
//
// # Why the check is at the dial and not at the URL
//
// Validating a hostname and then dialing it is two lookups with a gap between them. An
// attacker who controls the DNS answer makes the first lookup return a public address
// and the second a private one; both are honest answers and the check passes. The gap
// cannot be closed by checking harder, only by checking somewhere else.
//
// net.Dialer.ControlContext runs after the resolver and before connect(2), with the
// address the kernel is actually about to use. There is no second lookup to disagree
// with, so there is nothing to race.
//
// # What this package is not
//
// It is not an egress firewall. It sees one address per dial and can say no; it cannot
// see a protocol that redirects, re-resolves, or hands back a second address to connect
// to. What bounds such a protocol is that EVERY connection it makes comes through this
// guard, which is a property of the caller, not of this package.
//
// DeviceChain's tenant paths all have it: the webhook and httpCall senders and the mail
// relay dial through Dialer or Transport, and the connectors service hands its MQTT
// (including WebSocket), Kafka and SNS/SQS clients one dial function built on Dialer —
// so a Kafka cluster's advertised brokers, the second hop no URL check could see, are
// judged here like the seeds. A new tenant path that builds a client which dials for
// itself (a proxy from the environment, an SDK's own transport) is outside this boundary
// until it is handed this guard, whatever its configuration says.
//
// The chart's NetworkPolicy (networkPolicy.enabled, off by default) is a second layer,
// not the boundary. Two things about it are worth knowing: a NetworkPolicy compares
// prefixes and cannot look inside an IPv6 address that CARRIES an IPv4 one, which this
// package can; and the policy is a CEILING over this package's allowed-destination
// configuration — an address permitted here is still dropped by the network unless it is
// permitted there too.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlocked is the sentinel every refusal wraps. Callers classify on it — a blocked
// destination is TERMINAL, never retryable. Waiting does not make an address public, so
// a retry only burns the redelivery budget and delays the dead-letter that tells an
// operator what happened.
var ErrBlocked = errors.New("destination address is not permitted")

// BlockedError names the address that was refused and the rule that refused it, so an
// operator reading a dead-letter can tell "you pointed this at 10.0.0.5" apart from
// "your endpoint is down". The address is the RESOLVED one, which is the useful fact:
// the configured hostname is already in the record, and the resolved address is what the
// operator does not otherwise get to see.
type BlockedError struct {
	Addr   netip.Addr
	Reason string
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("%v: %s resolved to %s", ErrBlocked, e.Reason, e.Addr)
}

func (e *BlockedError) Unwrap() error { return ErrBlocked }

// Guard decides whether one resolved address may be dialed.
//
// The decision order is allow, then deny, then PERMIT. The allow list is the operator's
// escape hatch and is checked FIRST, because an operator who has deliberately permitted
// an address has more context than this package does.
//
// 🔴 THE TERMINAL IS A PERMIT, NOT A REFUSAL, and that is the single most important
// thing to know about this type. An address matching no allow prefix, no categorical
// check and no row of `denied` is DIALABLE — check returns nil at the bottom. It has to:
// a tenant's webhook destination is an arbitrary public host, and a guard that refused
// what it did not recognize would refuse the entire legitimate use.
//
// What follows from it is the part that bites. Under a default-deny reading the deny
// table looks like a second opinion — belt and braces over the categorical checks, where
// a missing row costs nothing. It is the opposite: outside the five categorical checks
// the table IS the boundary, and a hazardous range absent from it is reachable by every
// tenant. `denied` is not a list of extra refusals; it is the list of refusals. Treat an
// addition to it as a fix, not as tidying.
type Guard struct {
	allow []netip.Prefix
}

// NewGuard builds a guard that refuses every address in the built-in deny set except
// those covered by allow.
//
// 🔴 On the shape of the allow list, because the sharp edge is not visible from here.
// In Kubernetes the smallest CIDR that reaches one in-cluster relay is often the whole
// Service CIDR — and granting that re-opens every private address in the cluster for
// every tenant, which is the entire boundary. Document /32s. An operator who needs a
// name rather than an address needs a different mechanism: Control sees only ip:port, so
// a hostname allowance is not expressible here at all.
func NewGuard(allow []netip.Prefix) *Guard {
	// Copy, so a caller mutating its slice afterwards cannot widen a live boundary.
	cp := make([]netip.Prefix, len(allow))
	copy(cp, allow)
	return &Guard{allow: cp}
}

// CheckAddr reports whether addr may be dialed.
func (g *Guard) CheckAddr(addr netip.Addr) error {
	return g.check(addr, "destination")
}

func (g *Guard) check(addr netip.Addr, what string) error {
	// 🔴 NORMALIZE FIRST, and both halves matter. netip.Prefix.Contains documents that
	// it returns false for an address carrying an IPv6 zone and false when an
	// IPv4-mapped address is tested against an IPv4 prefix — so an un-normalized check
	// FAILS OPEN in exactly the two cases an attacker would reach for. Measured:
	// fe80::/10 does not contain fe80::1%eth0, and 169.254.0.0/16 does not contain
	// ::ffff:169.254.169.254. ControlContext does deliver zoned addresses.
	addr = addr.Unmap().WithZone("")

	if !addr.IsValid() {
		return &BlockedError{Addr: addr, Reason: what + " is not a valid address"}
	}

	for _, p := range g.allow {
		if p.Contains(addr) {
			return nil
		}
	}

	// The categorical checks come first. They overlap the prefix table below
	// deliberately: the table is a transcription of two registries and transcriptions
	// have typos, while these are the standard library's own classification of the same
	// addresses. Either one alone would be a single point of failure for the boundary.
	//
	// They also run BEFORE the embedded-address extraction so that ::1 is reported as a
	// loopback address rather than as an IPv4-compatible address wrapping 0.0.0.1. Both
	// refuse it; only one of them tells the operator what they actually configured.
	switch {
	case addr.IsLoopback():
		return &BlockedError{Addr: addr, Reason: what + " is a loopback address"}
	case addr.IsUnspecified():
		return &BlockedError{Addr: addr, Reason: what + " is the unspecified address"}
	case addr.IsMulticast(), addr.IsInterfaceLocalMulticast(), addr.IsLinkLocalMulticast():
		return &BlockedError{Addr: addr, Reason: what + " is a multicast address"}
	case addr.IsLinkLocalUnicast():
		return &BlockedError{Addr: addr, Reason: what + " is a link-local address"}
	case addr.IsPrivate():
		return &BlockedError{Addr: addr, Reason: what + " is a private address"}
	}

	// An IPv6 address may carry an IPv4 address inside it. Pull it out and judge the
	// address that will actually be reached, rather than the wrapper — a NAT64 or 6to4
	// address wrapping a public host is a legitimate destination, and refusing the whole
	// form would be a false deny.
	//
	// This recurses at most once: the extractors return only IPv4 addresses, and an IPv4
	// address matches none of the embedding prefixes.
	if addr.Is6() {
		// A Teredo address carries TWO reachable IPv4 addresses — the relay server at
		// bytes 4-7 in the clear, and the client at bytes 12-15 obfuscated. Judging only
		// the client would leave half the address unexamined.
		if server, ok := teredoServer(addr); ok {
			if err := g.check(server, what+" (Teredo server)"); err != nil {
				return err
			}
		}
		if embedded, form, ok := extractV4(addr); ok {
			return g.check(embedded, what+" ("+form+")")
		}
	}

	for _, d := range denied {
		if d.prefix.Contains(addr) {
			return &BlockedError{Addr: addr, Reason: what + " is in " + d.why}
		}
	}
	return nil
}

// ControlContext is the net.Dialer hook. Its signature is fixed by the standard library.
//
// A parse failure is a refusal, not a pass. The address string comes from the standard
// library and should always parse; if it ever does not, the honest response to "I cannot
// tell what this is" is no.
func (g *Guard) ControlContext(_ context.Context, _, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: could not parse dial address %q", ErrBlocked, address)
	}
	return g.CheckAddr(ap.Addr())
}

// Dialer returns a net.Dialer that refuses a blocked address before connect(2).
func (g *Guard) Dialer() *net.Dialer {
	return &net.Dialer{
		Timeout:        30 * time.Second,
		KeepAlive:      30 * time.Second,
		ControlContext: g.ControlContext,
	}
}

// Transport returns an http.Transport that dials through this guard.
//
// 🔴 Two things here are load-bearing and neither is a default.
//
// Proxy is set to nil EXPLICITLY. http.DefaultTransport uses ProxyFromEnvironment, and
// the Helm chart lets an operator set arbitrary environment variables per functional
// area. An HTTPS_PROXY there would make the proxy the dial target — every request would
// pass this guard, because the proxy's address is public, and the proxy would then
// fetch whatever the URL said. The boundary would be gone with nothing in any log to
// say so.
//
// This transport is also never installed on http.DefaultTransport. Service-to-service
// calls, the JWKS fetch and the governance resolvers all dial private ClusterIPs
// legitimately, and several of them run in the SAME process as tenant egress. A global
// install would break the platform rather than secure it.
func (g *Guard) Transport() *http.Transport {
	t := &http.Transport{
		Proxy:                 nil,
		DialContext:           g.Dialer().DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return t
}
