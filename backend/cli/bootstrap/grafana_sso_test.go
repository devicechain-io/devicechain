// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "testing"

// grafanaSSOEnabled gates on: requested, monitoring on, and a valid issuer (https, or
// http only for a loopback host).
func TestGrafanaSSOEnabled(t *testing.T) {
	cases := []struct {
		name string
		st   State
		want bool
	}{
		{"not requested", State{GrafanaSSO: false, NoMonitoring: false}, false},
		{"requested, monitoring off", State{GrafanaSSO: true, NoMonitoring: true}, false},
		{"https default host", State{GrafanaSSO: true, IngressHost: "dc.example.com"}, true},
		{"https empty host (defaults)", State{GrafanaSSO: true}, true},
		{"http localhost", State{GrafanaSSO: true, NoTLS: true, IngressHost: "localhost"}, true},
		{"http 127.0.0.1", State{GrafanaSSO: true, NoTLS: true, IngressHost: "127.0.0.1"}, true},
		{"http non-localhost invalid", State{GrafanaSSO: true, NoTLS: true, IngressHost: "dc.example.com"}, false},
		{"http default host invalid", State{GrafanaSSO: true, NoTLS: true}, false}, // default host is devicechain.local
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := grafanaSSOEnabled(&tc.st); got != tc.want {
				t.Errorf("grafanaSSOEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// The requested-but-invalid warn condition fires only when SSO was asked for (with
// monitoring) yet the issuer would be invalid.
func TestGrafanaSSORequestedButInvalid(t *testing.T) {
	if !grafanaSSORequestedButInvalid(&State{GrafanaSSO: true, NoTLS: true, IngressHost: "dc.example.com"}) {
		t.Error("http + non-localhost host should warn")
	}
	if grafanaSSORequestedButInvalid(&State{GrafanaSSO: true, IngressHost: "dc.example.com"}) {
		t.Error("https is valid — no warning")
	}
	if grafanaSSORequestedButInvalid(&State{GrafanaSSO: false}) {
		t.Error("not requested — no warning")
	}
}

// The URL split: browser authorize URL on the public host; token/userinfo in-cluster.
func TestGrafanaSSOURLsFor(t *testing.T) {
	// Local http flow.
	u := grafanaSSOURLsFor(&State{GrafanaSSO: true, NoTLS: true, IngressHost: "localhost", Instance: "devicechain"})
	if u.Issuer != "http://localhost/api/user-management" {
		t.Errorf("issuer = %q", u.Issuer)
	}
	if u.AuthURL != "http://localhost/api/user-management/oauth/authorize" {
		t.Errorf("auth_url = %q", u.AuthURL)
	}
	// Server-side calls go to the in-cluster Service (name.instance:8080), at the root
	// path — NOT the public host or the /api prefix.
	if u.TokenURL != "http://user-management.devicechain:8080/oauth/token" {
		t.Errorf("token_url = %q", u.TokenURL)
	}
	if u.APIURL != "http://user-management.devicechain:8080/oauth/userinfo" {
		t.Errorf("api_url = %q", u.APIURL)
	}
	if u.RootURL != "http://localhost/grafana" || u.Redirect != "http://localhost/grafana/login/generic_oauth" {
		t.Errorf("root/redirect = %q / %q", u.RootURL, u.Redirect)
	}
	if u.TLS {
		t.Error("TLS should be false with --no-tls")
	}

	// Real https deploy: public scheme flows into issuer/authorize/root; in-cluster
	// token/userinfo stay plain-http to the Service.
	h := grafanaSSOURLsFor(&State{GrafanaSSO: true, IngressHost: "dc.example.com", Instance: "prod"})
	if h.Issuer != "https://dc.example.com/api/user-management" || !h.TLS {
		t.Errorf("https issuer/tls = %q / %v", h.Issuer, h.TLS)
	}
	if h.TokenURL != "http://user-management.prod:8080/oauth/token" {
		t.Errorf("https deploy in-cluster token_url = %q (must stay in-cluster http)", h.TokenURL)
	}
}
