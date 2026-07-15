// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

// Grafana SSO wiring (ADR-047): when --grafana-sso is set, the bring-up turns on
// user-management's OAuth 2.1 AS (sets the issuer), seeds a confidential Grafana
// client, and configures Grafana's generic_oauth + a /grafana ingress — gated to the
// operator/superuser tier. The URLs are computed here so the tofu (monitoring module)
// and helm (user-management config) steps agree on one source of truth.

// grafanaSSOURLs are the endpoint URLs the Grafana SSO flow needs. The browser-facing
// authorize URL rides the public ingress host; the server-side token/userinfo URLs
// are IN-CLUSTER service URLs because Grafana's pod (in the monitoring namespace)
// cannot reach the public ingress host. That host split is fine: Grafana reads
// identity from userinfo (not an id_token), so it never validates the token issuer.
type grafanaSSOURLs struct {
	Issuer   string // <scheme>://<host>/api/user-management  (also every token's iss)
	AuthURL  string // browser: issuer + /oauth/authorize
	TokenURL string // in-cluster: http://user-management.<instance>:8080/oauth/token
	APIURL   string // in-cluster: .../oauth/userinfo
	RootURL  string // <scheme>://<host>/grafana
	Redirect string // rootURL + /login/generic_oauth
	Host     string
	TLS      bool
}

// grafanaHost resolves the ingress host the SSO URLs are built on (the same host the
// app ingress uses).
func grafanaHost(st *State) string {
	if st.IngressHost != "" {
		return st.IngressHost
	}
	return DefaultIngressHost
}

// grafanaSSOEnabled reports whether Grafana SSO should be wired: it was requested,
// the monitoring stack is being installed (Grafana exists), AND the resulting issuer
// is valid — issuer validation requires https, tolerating http only for a loopback
// host. A requested-but-invalid combination (http on a non-localhost host) is left
// OFF so user-management does not fail startup on a bad issuer; the render step warns.
func grafanaSSOEnabled(st *State) bool {
	if !st.GrafanaSSO || st.NoMonitoring {
		return false
	}
	return !st.NoTLS || isLoopbackHost(grafanaHost(st))
}

// grafanaSSORequestedButInvalid is the warn condition: SSO was asked for (with
// monitoring on) but the issuer would be invalid, so it was silently left off.
func grafanaSSORequestedButInvalid(st *State) bool {
	return st.GrafanaSSO && !st.NoMonitoring && !grafanaSSOEnabled(st)
}

// grafanaSSOURLsFor computes the endpoint URLs for the current host/scheme/instance.
func grafanaSSOURLsFor(st *State) grafanaSSOURLs {
	host := grafanaHost(st)
	scheme := "https"
	if st.NoTLS {
		scheme = "http"
	}
	pub := scheme + "://" + host
	issuer := pub + "/api/user-management"
	root := pub + "/grafana"
	// The in-cluster Service is named for its functional area (user-management) in the
	// instance namespace, fronting the graphql/HTTP port 8080 at the service root
	// (the /api/<area> prefix is an ingress construct, absent in-cluster).
	inCluster := "http://user-management." + st.Instance + ":8080"
	return grafanaSSOURLs{
		Issuer:   issuer,
		AuthURL:  issuer + "/oauth/authorize",
		TokenURL: inCluster + "/oauth/token",
		APIURL:   inCluster + "/oauth/userinfo",
		RootURL:  root,
		Redirect: root + "/login/generic_oauth",
		Host:     host,
		TLS:      !st.NoTLS,
	}
}
