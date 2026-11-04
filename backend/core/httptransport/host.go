// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package httptransport

import (
	"crypto/tls"
	"net"
	"net/http"
)

// PresentHost returns a RoundTripper that sends every request with host as its Host
// header and, over TLS, as the SNI and certificate-verification name — whatever address
// the request URL dials.
//
// It is for a caller that reaches an instance through an address (a load balancer, a
// port-forward) while the instance's Gateway routes on a hostname: the connection goes
// where the URL says, and the routing decision sees the hostname the instance was
// bootstrapped under. t is modified (its TLS server name is set), so pass a transport
// this caller owns, such as the result of New.
func PresentHost(t *http.Transport, host string) http.RoundTripper {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if t.TLSClientConfig != nil {
		cfg = t.TLSClientConfig.Clone()
		if cfg.MinVersion == 0 {
			cfg.MinVersion = tls.VersionTLS12
		}
	}
	cfg.ServerName = HostName(host)
	t.TLSClientConfig = cfg
	return &hostTransport{host: host, next: t}
}

// HostName is the host part of a host or host:port string.
func HostName(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

type hostTransport struct {
	host string
	next http.RoundTripper
}

func (t *hostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Host = t.host
	return t.next.RoundTrip(r)
}
