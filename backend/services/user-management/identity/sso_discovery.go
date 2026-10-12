// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-user-management/iam"
)

// Fetching an identity provider's discovery document is the one place user-management
// makes an outbound request to an address a person typed in. Three properties make that
// safe enough to be a superuser feature:
//
//   - THE CLIENT IS HANDED IN, never defaulted. A nil client is an error here, not
//     http.DefaultClient: the default would dial anywhere, and the guard that stops it
//     reaching a private address lives in the client's transport (NewSsoHTTPClient), so
//     the way to lose the guard is to not pass one.
//   - THE RESPONSE IS BOUNDED and a redirect is refused. A discovery document is served
//     where the issuer says it is.
//   - THE RESULT IS A SNAPSHOT. The sign-in flow dials only the endpoints recorded in
//     it, so a provider that later moves an endpoint changes nothing until a person
//     approves the new snapshot.

var (
	// ErrDiscoveryNoClient refuses a fetch with no HTTP client configured.
	ErrDiscoveryNoClient = errors.New("no HTTP client is configured for identity provider discovery")
	// ErrDiscoveryIssuerMismatch refuses a document whose issuer is not EXACTLY the issuer it
	// was fetched for. Exact means byte for byte: a trailing slash is a different issuer.
	ErrDiscoveryIssuerMismatch = errors.New("the discovery document's issuer does not match the configured issuer")
	// ErrDiscoveryInvalid refuses a document that is unreachable, malformed, oversized or
	// missing an endpoint the sign-in flow needs.
	ErrDiscoveryInvalid = errors.New("identity provider discovery failed")
	// ErrIssuerRefused refuses an issuer that cannot be pinned to one organisation.
	ErrIssuerRefused = errors.New("issuer refused")
)

const (
	// maxDiscoveryBytes bounds the document. Real ones are a few KiB.
	maxDiscoveryBytes = 64 << 10
	// discoveryTimeout bounds one fetch end to end.
	discoveryTimeout = 15 * time.Second
)

// discoveryClock is the time source for FetchedAt; a test may replace it.
var discoveryClock = func() time.Time { return time.Now().UTC() }

// NewSsoHTTPClient builds the HTTP client every identity-provider fetch goes through.
//
// Its transport is the egress guard's: it refuses, at the dial and on the RESOLVED
// address, anything the guard denies (loopback, private, link-local, metadata) unless the
// operator's instance-wide allow list permits it, and it never uses a proxy from the
// environment (a proxy's address is public, so it would be dialed, and it would then fetch
// whatever the URL said). It follows no redirects: a discovery document, a token endpoint
// and a key set are served where the snapshot says they are.
func NewSsoHTTPClient(infra mscfg.InfrastructureConfiguration) (*http.Client, error) {
	guard, err := egress.FromConfig(infra.Egress)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Transport: boundResponses(guard.Transport(), maxIdpResponseBytes),
		Timeout:   discoveryTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// SetSsoHTTPClient installs the client identity-provider fetches go through. It is called
// once at startup, from main, with NewSsoHTTPClient's result.
func (m *Manager) SetSsoHTTPClient(c *http.Client) { m.ssoClient = c }

// FetchIdpDiscovery fetches and validates an issuer's discovery document through the
// Manager's own client. A Manager that was never given one refuses (ErrDiscoveryNoClient)
// rather than reaching for a default.
func (m *Manager) FetchIdpDiscovery(ctx context.Context, issuer string) (*iam.DiscoverySnapshot, error) {
	return FetchDiscovery(ctx, m.ssoClient, issuer)
}

// FetchDiscovery fetches issuer's OpenID Connect discovery document with client and
// returns it as a snapshot.
//
// The issuer must be an https URL (http is accepted only for a loopback host, which is
// for tests and local development — the egress guard still decides whether a loopback
// dial is permitted). The document's own "issuer" must equal the issuer string EXACTLY:
// that equality is what stops a provider from vouching for a different one. The three
// endpoints a sign-in needs must be present and absolute; an optional end_session_endpoint
// is validated the same way when it is.
func FetchDiscovery(ctx context.Context, client *http.Client, issuer string) (*iam.DiscoverySnapshot, error) {
	if client == nil {
		return nil, ErrDiscoveryNoClient
	}
	if err := checkIssuerURL(issuer); err != nil {
		return nil, err
	}

	// Per the discovery spec the well-known path is appended to the issuer; a trailing slash
	// on the issuer does not double up.
	endpoint := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDiscoveryInvalid, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		// Wrapped with %w on BOTH so a blocked destination stays errors.Is(egress.ErrBlocked):
		// the caller classifies that as a refusal of the address, not an unreachable provider.
		return nil, fmt.Errorf("%w: %w", ErrDiscoveryInvalid, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s answered %d", ErrDiscoveryInvalid, endpoint, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: reading the document: %v", ErrDiscoveryInvalid, err)
	}
	if len(body) > maxDiscoveryBytes {
		return nil, fmt.Errorf("%w: the document is larger than %d bytes", ErrDiscoveryInvalid, maxDiscoveryBytes)
	}

	var doc struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		JwksURI               string `json:"jwks_uri"`
		EndSessionEndpoint    string `json:"end_session_endpoint"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w: the document is not JSON: %v", ErrDiscoveryInvalid, err)
	}
	if doc.Issuer != issuer {
		return nil, fmt.Errorf("%w: configured %q, document says %q", ErrDiscoveryIssuerMismatch, issuer, doc.Issuer)
	}
	for _, e := range []struct{ name, value string }{
		{"authorization_endpoint", doc.AuthorizationEndpoint},
		{"token_endpoint", doc.TokenEndpoint},
		{"jwks_uri", doc.JwksURI},
	} {
		if e.value == "" {
			return nil, fmt.Errorf("%w: the document has no %s", ErrDiscoveryInvalid, e.name)
		}
		if err := checkEndpointURL(e.name, e.value); err != nil {
			return nil, err
		}
	}
	if doc.EndSessionEndpoint != "" {
		if err := checkEndpointURL("end_session_endpoint", doc.EndSessionEndpoint); err != nil {
			return nil, err
		}
	}

	return &iam.DiscoverySnapshot{
		Issuer:                doc.Issuer,
		AuthorizationEndpoint: doc.AuthorizationEndpoint,
		TokenEndpoint:         doc.TokenEndpoint,
		JwksURI:               doc.JwksURI,
		EndSessionEndpoint:    doc.EndSessionEndpoint,
		FetchedAt:             discoveryClock(),
	}, nil
}

// checkIssuerURL requires an https issuer (http only on loopback) with no query,
// fragment or credentials.
func checkIssuerURL(issuer string) error {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: %q is not an absolute URL", ErrIssuerRefused, issuer)
	}
	if err := checkScheme(u); err != nil {
		return fmt.Errorf("%w: issuer %q: %v", ErrIssuerRefused, issuer, err)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("%w: issuer %q must not carry a query, fragment or credentials", ErrIssuerRefused, issuer)
	}
	// A fully-qualified host ("login.microsoftonline.com.") resolves to the same provider but
	// is a different string, so it would slip past every host-keyed rule below.
	if strings.HasSuffix(u.Hostname(), ".") {
		return fmt.Errorf("%w: issuer %q must not end its host with a dot", ErrIssuerRefused, issuer)
	}
	return nil
}

// checkEndpointURL applies the same scheme rule to an endpoint a document names.
func checkEndpointURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: %s %q is not an absolute URL", ErrDiscoveryInvalid, name, raw)
	}
	if err := checkScheme(u); err != nil {
		return fmt.Errorf("%w: %s %q: %v", ErrDiscoveryInvalid, name, raw, err)
	}
	if u.User != nil {
		return fmt.Errorf("%w: %s %q carries credentials", ErrDiscoveryInvalid, name, raw)
	}
	return nil
}

func checkScheme(u *url.URL) error {
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return errors.New("must be https (http is accepted only for a loopback host)")
	}
	return fmt.Errorf("scheme %q is not allowed", u.Scheme)
}

// microsoftIssuerHosts are the hosts Microsoft Entra signs tokens from across its sovereign
// clouds. Every one fronts MANY organisations under one signing identity, so "this token
// came from the issuer" says nothing about WHOSE token it is.
var microsoftIssuerHosts = map[string]struct{}{
	"login.microsoftonline.com":        {},
	"login.microsoft.com":              {},
	"login.microsoftonline.us":         {},
	"login.microsoftonline.de":         {},
	"login.partner.microsoftonline.cn": {},
	"login.chinacloudapi.cn":           {},
	"login.windows.net":                {},
	"sts.windows.net":                  {},
}

// entraConsumersTenant is the fixed tenant id of personal Microsoft accounts, which anyone
// can create.
const entraConsumersTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"

// ValidateConnectionIssuer refuses an issuer that cannot be pinned to one organisation.
//
// A shared multi-tenant issuer signs tokens for every customer of the provider, so trusting
// it without pinning the customer lets any customer's user sign in. The refusals:
//
//   - Microsoft Entra: the issuer must name one tenant (a GUID). The generic endpoints
//     "common", "organizations" and "consumers" are refused outright — their tokens carry a
//     per-tenant issuer, so the configured one can never match and, worse, would invite
//     loosening the match. A "tid" required claim equal to the tenant GUID is mandatory.
//   - Google: accounts.google.com signs for every Google account. A Workspace "hd" required
//     claim is mandatory; without it any @gmail.com user passes.
//
// requiredClaims is the connection's required-claims map (claim name to expected value).
func ValidateConnectionIssuer(issuer string, requiredClaims map[string]string) error {
	if err := checkIssuerURL(issuer); err != nil {
		return err
	}
	u, _ := url.Parse(issuer) // checkIssuerURL parsed it
	host := strings.ToLower(u.Hostname())

	if _, ok := microsoftIssuerHosts[host]; ok {
		segs := strings.Split(strings.Trim(u.Path, "/"), "/")
		tenant := strings.ToLower(segs[0])
		switch tenant {
		case "", "common", "organizations", "consumers", entraConsumersTenant:
			return fmt.Errorf("%w: a Microsoft issuer must name one organisation's tenant id; "+
				"%q (common, organizations and consumers) admits accounts from every organisation", ErrIssuerRefused, u.Path)
		}
		if !isGUID(tenant) {
			return fmt.Errorf("%w: %q is not a Microsoft tenant id (a GUID)", ErrIssuerRefused, segs[0])
		}
		if len(segs) > 2 || (len(segs) == 2 && segs[1] != "v2.0") {
			return fmt.Errorf("%w: unexpected path %q after the tenant id", ErrIssuerRefused, u.Path)
		}
		if !strings.EqualFold(requiredClaims["tid"], tenant) {
			return fmt.Errorf("%w: a Microsoft connection must require the claim tid equal to its tenant id %s", ErrIssuerRefused, tenant)
		}
		return nil
	}

	if host == "accounts.google.com" {
		if hd := strings.TrimSpace(requiredClaims["hd"]); hd == "" || hd == "*" {
			return fmt.Errorf("%w: a Google connection must require the claim hd (the Workspace domain); "+
				"without it any Google account, including personal ones, can sign in", ErrIssuerRefused)
		}
		return nil
	}
	return nil
}

// isGUID reports whether s is a canonical lowercase-or-uppercase GUID.
func isGUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'):
		default:
			return false
		}
	}
	return true
}

// errIdpResponseTooLarge is what a read of an identity provider's response returns once it
// passes the bound.
var errIdpResponseTooLarge = errors.New("the identity provider's response is larger than the bound")

// boundResponses wraps rt so that no response body can be read past max bytes. go-oidc reads
// a key set with io.ReadAll and oauth2 reads a token response likewise, neither with a
// bound of its own; the bound has to live where both pass, which is the transport. Reading
// past it fails the read with errIdpResponseTooLarge rather than truncating, because a
// silently truncated key set or token response would parse as something it is not.
func boundResponses(rt http.RoundTripper, max int64) http.RoundTripper {
	return boundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := rt.RoundTrip(r)
		if err != nil || resp.Body == nil {
			return resp, err
		}
		if resp.ContentLength > max {
			resp.Body.Close()
			return nil, errIdpResponseTooLarge
		}
		resp.Body = &boundedBody{rc: resp.Body, left: max}
		return resp, nil
	})
}

type boundTripFunc func(*http.Request) (*http.Response, error)

func (f boundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type boundedBody struct {
	rc   io.ReadCloser
	left int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// Exactly max bytes were read; one more byte existing is the overrun.
		var one [1]byte
		n, err := b.rc.Read(one[:])
		if n > 0 {
			return 0, errIdpResponseTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *boundedBody) Close() error { return b.rc.Close() }
