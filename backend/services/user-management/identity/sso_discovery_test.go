// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/stretchr/testify/require"
)

// fakeIdp serves a discovery document whose fields are set by the test. hits counts
// requests, so a test can prove a refusal happened BEFORE any request was served.
type fakeIdp struct {
	srv  *httptest.Server
	doc  map[string]any
	hits int
	// status and raw override the answer when set.
	status int
	raw    []byte
}

func newFakeIdp(t *testing.T, mutate func(base string, doc map[string]any)) *fakeIdp {
	t.Helper()
	f := &fakeIdp{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits++
		if !strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
			http.NotFound(w, r)
			return
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
		}
		if f.raw != nil {
			_, _ = w.Write(f.raw)
			return
		}
		_ = json.NewEncoder(w).Encode(f.doc)
	}))
	t.Cleanup(f.srv.Close)
	base := f.srv.URL
	f.doc = map[string]any{
		"issuer":                 base,
		"authorization_endpoint": base + "/authorize",
		"token_endpoint":         base + "/token",
		"jwks_uri":               base + "/keys",
	}
	if mutate != nil {
		mutate(base, f.doc)
	}
	return f
}

// plainClient dials without the egress guard: most tests here are about the DOCUMENT, and
// the guard has its own tests below.
func plainClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDiscoveryIssuerMustMatchExactly(t *testing.T) {
	ctx := context.Background()

	// The control: a document that names the issuer it was fetched for is accepted.
	ok := newFakeIdp(t, nil)
	snap, err := FetchDiscovery(ctx, plainClient(), ok.srv.URL)
	require.NoError(t, err)
	require.Equal(t, ok.srv.URL, snap.Issuer)
	require.Equal(t, ok.srv.URL+"/token", snap.TokenEndpoint)
	require.Equal(t, ok.srv.URL+"/keys", snap.JwksURI)
	require.False(t, snap.FetchedAt.IsZero())

	// A trailing slash is a DIFFERENT issuer: both directions.
	slash := newFakeIdp(t, func(base string, doc map[string]any) { doc["issuer"] = base + "/" })
	_, err = FetchDiscovery(ctx, plainClient(), slash.srv.URL)
	require.ErrorIs(t, err, ErrDiscoveryIssuerMismatch, "document adds a trailing slash")

	bare := newFakeIdp(t, nil)
	_, err = FetchDiscovery(ctx, plainClient(), bare.srv.URL+"/")
	require.ErrorIs(t, err, ErrDiscoveryIssuerMismatch, "configured with a trailing slash, document without")

	// And a different issuer entirely.
	other := newFakeIdp(t, func(_ string, doc map[string]any) { doc["issuer"] = "https://evil.example" })
	_, err = FetchDiscovery(ctx, plainClient(), other.srv.URL)
	require.ErrorIs(t, err, ErrDiscoveryIssuerMismatch)

	// A configured trailing slash that the document honours is accepted, and the well-known
	// path does not double the slash.
	both := newFakeIdp(t, func(base string, doc map[string]any) { doc["issuer"] = base + "/" })
	_, err = FetchDiscovery(ctx, plainClient(), both.srv.URL+"/")
	require.NoError(t, err)
}

func TestDiscoveryRefusesAnUnsafeIssuerBeforeAnyFetch(t *testing.T) {
	boom := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("the client was used for an issuer that should have been refused before any fetch")
		return nil, nil
	})}
	for _, bad := range []string{
		"http://idp.example", "ftp://idp.example", "idp.example", "https://", "https://user:pw@idp.example",
		"https://idp.example?x=1", "https://idp.example#frag", "",
	} {
		_, err := FetchDiscovery(context.Background(), boom, bad)
		require.ErrorIsf(t, err, ErrIssuerRefused, "%q", bad)
	}
}

func TestDiscoveryNeedsAClient(t *testing.T) {
	_, err := FetchDiscovery(context.Background(), nil, "https://idp.example")
	require.ErrorIs(t, err, ErrDiscoveryNoClient, "a nil client must refuse, not default to one that dials anywhere")
}

func TestDiscoveryRefusesBadDocuments(t *testing.T) {
	ctx := context.Background()

	for name, mutate := range map[string]func(string, map[string]any){
		"no token endpoint":   func(_ string, d map[string]any) { delete(d, "token_endpoint") },
		"no jwks":             func(_ string, d map[string]any) { delete(d, "jwks_uri") },
		"no authorization":    func(_ string, d map[string]any) { delete(d, "authorization_endpoint") },
		"relative endpoint":   func(_ string, d map[string]any) { d["token_endpoint"] = "/token" },
		"plain-http endpoint": func(_ string, d map[string]any) { d["token_endpoint"] = "http://idp.example/token" },
		"javascript scheme":   func(_ string, d map[string]any) { d["jwks_uri"] = "javascript:alert(1)" },
		"credentials in url":  func(_ string, d map[string]any) { d["token_endpoint"] = "https://u:p@idp.example/token" },
		"bad end session":     func(_ string, d map[string]any) { d["end_session_endpoint"] = "http://idp.example/logout" },
	} {
		f := newFakeIdp(t, mutate)
		_, err := FetchDiscovery(ctx, plainClient(), f.srv.URL)
		require.ErrorIsf(t, err, ErrDiscoveryInvalid, name)
	}

	// The optional endpoint is carried through when it is valid.
	f := newFakeIdp(t, func(base string, d map[string]any) { d["end_session_endpoint"] = base + "/logout" })
	snap, err := FetchDiscovery(ctx, plainClient(), f.srv.URL)
	require.NoError(t, err)
	require.Equal(t, f.srv.URL+"/logout", snap.EndSessionEndpoint)

	notJSON := newFakeIdp(t, nil)
	notJSON.raw = []byte("<html>")
	_, err = FetchDiscovery(ctx, plainClient(), notJSON.srv.URL)
	require.ErrorIs(t, err, ErrDiscoveryInvalid)

	down := newFakeIdp(t, nil)
	down.status = http.StatusInternalServerError
	_, err = FetchDiscovery(ctx, plainClient(), down.srv.URL)
	require.ErrorIs(t, err, ErrDiscoveryInvalid)

	// A body past the bound is refused, not truncated into something that parses.
	huge := newFakeIdp(t, nil)
	huge.raw = []byte(`{"issuer":"` + huge.srv.URL + `","pad":"` + strings.Repeat("x", maxDiscoveryBytes) + `"}`)
	_, err = FetchDiscovery(ctx, plainClient(), huge.srv.URL)
	require.ErrorIs(t, err, ErrDiscoveryInvalid)
	require.Contains(t, err.Error(), "larger than")
}

func TestDiscoveryRefusesARedirect(t *testing.T) {
	target := newFakeIdp(t, nil)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.srv.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	client, err := NewSsoHTTPClient(mscfg.InfrastructureConfiguration{
		Egress: mscfg.EgressConfiguration{AllowedDestinations: []string{"127.0.0.0/8"}},
	})
	require.NoError(t, err)
	_, err = FetchDiscovery(context.Background(), client, redirector.URL)
	require.ErrorIs(t, err, ErrDiscoveryInvalid, "a discovery document is served where the issuer says; a redirect is not followed")
	require.Zero(t, target.hits, "the redirect target must never be requested")
}

func TestDiscoveryGoesThroughTheEgressGuard(t *testing.T) {
	f := newFakeIdp(t, nil)

	// The default guard refuses a loopback destination: the fake IdP is on 127.0.0.1, so a
	// refusal here is the DIAL being refused, before any request reaches it.
	guarded, err := NewSsoHTTPClient(mscfg.InfrastructureConfiguration{})
	require.NoError(t, err)
	_, err = FetchDiscovery(context.Background(), guarded, f.srv.URL)
	require.ErrorIs(t, err, egress.ErrBlocked)
	require.ErrorIs(t, err, ErrDiscoveryInvalid)
	require.Zero(t, f.hits, "the guard must refuse at the dial, not after the request was served")

	// The control: the operator's allow list opens exactly that address, and the same fetch
	// succeeds — proving the refusal above was the guard and not a malformed URL.
	allowed, err := NewSsoHTTPClient(mscfg.InfrastructureConfiguration{
		Egress: mscfg.EgressConfiguration{AllowedDestinations: []string{"127.0.0.0/8"}},
	})
	require.NoError(t, err)
	_, err = FetchDiscovery(context.Background(), allowed, f.srv.URL)
	require.NoError(t, err)
	require.Equal(t, 1, f.hits)

	// A malformed allow entry fails construction rather than being skipped.
	_, err = NewSsoHTTPClient(mscfg.InfrastructureConfiguration{
		Egress: mscfg.EgressConfiguration{AllowedDestinations: []string{"not-a-cidr"}},
	})
	require.Error(t, err)
}

func TestManagerWithoutAClientRefusesDiscovery(t *testing.T) {
	m := NewManager(nil, nil, nil, nil, 0, 0, "", BootstrapConfig{}, nil)
	_, err := m.FetchIdpDiscovery(context.Background(), "https://idp.example")
	require.ErrorIs(t, err, ErrDiscoveryNoClient)
}

func TestGoogleIssuerRequiresHd(t *testing.T) {
	const google = "https://accounts.google.com"
	for name, claims := range map[string]map[string]string{
		"none": nil, "empty map": {}, "blank hd": {"hd": "  "}, "wildcard hd": {"hd": "*"}, "wrong claim": {"tid": "x"},
	} {
		require.ErrorIsf(t, ValidateConnectionIssuer(google, claims), ErrIssuerRefused, name)
	}
	require.NoError(t, ValidateConnectionIssuer(google, map[string]string{"hd": "acme.example"}))
}

func TestEntraCommonRefused(t *testing.T) {
	const guid = "72f988bf-86f1-41af-91ab-2d7cd011db47"
	pin := map[string]string{"tid": guid}

	for _, issuer := range []string{
		"https://login.microsoftonline.com/common/v2.0",
		"https://login.microsoftonline.com/organizations/v2.0",
		"https://login.microsoftonline.com/consumers/v2.0",
		"https://login.microsoftonline.com/9188040d-6c67-4c5b-b112-36a304b66dad/v2.0", // the personal-account tenant
		"https://login.microsoftonline.com/",
		"https://login.microsoftonline.com",
		"https://login.microsoftonline.com/not-a-guid/v2.0",
		"https://sts.windows.net/common/",
		"https://login.microsoftonline.com/" + guid + "/v2.0/extra",
	} {
		require.ErrorIsf(t, ValidateConnectionIssuer(issuer, map[string]string{"tid": "common"}), ErrIssuerRefused, "%s", issuer)
		require.ErrorIsf(t, ValidateConnectionIssuer(issuer, pin), ErrIssuerRefused, "%s (even with a tid pin)", issuer)
	}

	// A single-tenant issuer WITHOUT the tid pin, or pinned to a different tenant, is refused.
	issuer := "https://login.microsoftonline.com/" + guid + "/v2.0"
	require.ErrorIs(t, ValidateConnectionIssuer(issuer, nil), ErrIssuerRefused)
	require.ErrorIs(t, ValidateConnectionIssuer(issuer, map[string]string{"tid": "00000000-0000-0000-0000-000000000000"}), ErrIssuerRefused)

	// The controls: the same issuer pinned to its own tenant is accepted (case-insensitively),
	// as is the sts form.
	require.NoError(t, ValidateConnectionIssuer(issuer, pin))
	require.NoError(t, ValidateConnectionIssuer(issuer, map[string]string{"tid": strings.ToUpper(guid)}))
	require.NoError(t, ValidateConnectionIssuer("https://sts.windows.net/"+guid+"/", pin))
}

func TestOtherIssuersPassThroughAndMalformedOnesAreRefused(t *testing.T) {
	require.NoError(t, ValidateConnectionIssuer("https://acme.okta.com", nil))
	require.NoError(t, ValidateConnectionIssuer("https://sso.acme.example/realms/main", nil))
	require.NoError(t, ValidateConnectionIssuer("http://127.0.0.1:5556/dex", nil), "loopback http is for local development")
	require.ErrorIs(t, ValidateConnectionIssuer("http://sso.acme.example", nil), ErrIssuerRefused)
	require.ErrorIs(t, ValidateConnectionIssuer("not a url", nil), ErrIssuerRefused)
}
