// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// The instance Gateway's listener is bound to the instance's recorded host
// (Instance.spec.host, "devicechain.local" when bootstrap got no --host), and every
// HTTPRoute carries the same hostname. That per-instance hostname isolation is
// deliberate, so a request is routed only when its Host header names the instance.
//
// --server is both where dcctl dials and, absent an override, what it sends as Host.
// Pointed at a load-balancer address or a port-forward (an IP, or localhost) that
// header is the address itself, which the listener rejects: the login fails with a
// 404 and nothing in it says the hostname was the problem. The functions below keep
// the dial target and the Host header apart, so a reachable address can be paired
// with the hostname the instance was bootstrapped under.

// serverHostname is the host part of a --server value, with any port dropped.
func serverHostname(server string) string {
	if h, _, err := net.SplitHostPort(server); err == nil {
		return h
	}
	return strings.Trim(server, "[]")
}

// NeedsHostHeader reports whether --server names the instance by something that can
// never match a Gateway listener hostname: an IP literal or a loopback name. A DNS
// name is the operator's own statement of which host the instance answers to, so it
// is left alone.
func NeedsHostHeader(server string) bool {
	h := serverHostname(server)
	if strings.EqualFold(h, "localhost") {
		return true
	}
	_, err := netip.ParseAddr(h)
	return err == nil
}

// PickHostHeader settles the Host header to send. explicit (--host-header) always
// wins. Otherwise the instance's recorded host is used, but only when --server is an
// address that cannot match a hostname and is not already that host; in every other
// case it returns "" and the request keeps --server as its Host.
func PickHostHeader(server, explicit, recorded string) string {
	if h := strings.TrimSpace(explicit); h != "" {
		return h
	}
	recorded = strings.TrimSpace(recorded)
	if recorded == "" || !NeedsHostHeader(server) || strings.EqualFold(serverHostname(server), recorded) {
		return ""
	}
	return recorded
}

// hostHeaderTransport sends every request with Host set to host, whatever address
// the URL dials.
type hostHeaderTransport struct {
	host string
	next http.RoundTripper
}

func (t *hostHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Host = t.host
	return t.next.RoundTrip(r)
}

// HostHeaderClient returns an HTTP client that presents host as the Host header and
// the TLS server name. host is what the Gateway routes on; the URL still decides
// where the connection goes.
func HostHeaderClient(host string) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverHostname(host)}
	return &http.Client{Timeout: 30 * time.Second, Transport: &hostHeaderTransport{host: host, next: base}}
}
