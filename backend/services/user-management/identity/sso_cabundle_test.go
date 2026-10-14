// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// testCA is a private certificate authority and the leaf it signed for 127.0.0.1.
type testCA struct {
	certPEM string
	leaf    tls.Certificate
	der     []byte
}

func newTestCA(t *testing.T, name string) *testCA {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano() + 1), Subject: pkix.Name{CommonName: "idp.internal"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	require.NoError(t, err)
	return &testCA{
		certPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		leaf:    tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey},
		der:     caDER,
	}
}

func (c *testCA) leafPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.leaf.Certificate[0]}))
}

// startTLS serves h over TLS with the CA's leaf. Handshake failures (the client refusing
// the certificate) are expected in the negative controls and are not logged.
func (c *testCA) startTLS(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{c.leaf}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// tlsOP is the harness's stub provider, served over TLS by the private CA.
func (c *testCA) tlsOP(t *testing.T) *stubOP {
	t.Helper()
	op := &stubOP{t: t, key: sharedOPKey(t), kid: "op-key-1", grants: map[string]*opGrant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", op.discovery)
	mux.HandleFunc("/keys", op.keys)
	mux.HandleFunc("/token", op.token)
	op.srv = c.startTLS(t, mux)
	return op
}

var allowLoopback = mscfg.InfrastructureConfiguration{
	Egress: mscfg.EgressConfiguration{AllowedDestinations: []string{"127.0.0.0/8"}},
}

func installFactory(t *testing.T, m *Manager, infra mscfg.InfrastructureConfiguration) {
	t.Helper()
	f, err := NewSsoClientFactory(infra)
	require.NoError(t, err)
	base, err := NewSsoHTTPClient(infra)
	require.NoError(t, err)
	m.SetSsoHTTPClient(base)
	m.SetSsoClientFactory(f)
}

func requireUnknownAuthority(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var ua x509.UnknownAuthorityError
	require.ErrorAs(t, err, &ua, "the refusal must be the certificate's, not some other failure: %v", err)
}

func TestParseCaBundleAcceptsCertificatesAndCanonicalisesThem(t *testing.T) {
	ca := newTestCA(t, "Acme Root")
	other := newTestCA(t, "Acme Issuing")

	// Comments between blocks and a duplicate are dropped; the stored text is the
	// certificates alone.
	in := "# Acme roots\n" + ca.certPEM + "\nsome note\n" + other.certPEM + ca.certPEM
	canon, certs, err := ParseCaBundle(in)
	require.NoError(t, err)
	require.Len(t, certs, 2)
	require.Equal(t, ca.certPEM+other.certPEM, canon)
	require.NotContains(t, canon, "note")

	sum := sha256.Sum256(ca.der)
	require.Equal(t, hex.EncodeToString(sum[:]), certs[0].FingerprintSHA256)
	require.Equal(t, "CN=Acme Root", certs[0].Subject)
	require.True(t, certs[0].IsCA)
	require.True(t, certs[0].NotAfter.After(time.Now()))
}

func TestParseCaBundleEmptyMeansNoBundle(t *testing.T) {
	for _, in := range []string{"", "  \n\t"} {
		canon, certs, err := ParseCaBundle(in)
		require.NoError(t, err)
		require.Empty(t, canon)
		require.Empty(t, certs)
	}
}

// A certificate that is not a CA is accepted (pinning a provider's own certificate is
// legitimate) and reported as such, so the console can say so.
func TestParseCaBundleAcceptsAnExactCertificateAndReportsItIsNotACA(t *testing.T) {
	_, certs, err := ParseCaBundle(newTestCA(t, "r").leafPEM())
	require.NoError(t, err)
	require.Len(t, certs, 1)
	require.False(t, certs[0].IsCA)
}

func TestParseCaBundleRefusesAPrivateKeyWithoutEchoingIt(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	pkcs8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	ec, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	sec1 := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ec}))
	ca := newTestCA(t, "r")

	cases := map[string]string{
		"pkcs8 alone":           pkcs8,
		"sec1 alone":            sec1,
		"after a certificate":   ca.certPEM + pkcs8,
		"before a certificate":  sec1 + ca.certPEM,
		"truncated key block":   ca.certPEM + "-----BEGIN PRIVATE KEY-----\nMIGHAgEAMBMGByqGSM49\n",
		"lower-case marker":     ca.certPEM + "-----begin private key-----\nAAAA\n",
		"encrypted key marker":  ca.certPEM + "-----BEGIN ENCRYPTED PRIVATE KEY-----\nAAAA\n-----END ENCRYPTED PRIVATE KEY-----\n",
		"rsa key marker, junk":  ca.certPEM + "-----BEGIN RSA PRIVATE KEY-----\n!!!\n",
		"key between two certs": ca.certPEM + pkcs8 + ca.leafPEM(),
	}
	for name, in := range cases {
		canon, certs, err := ParseCaBundle(in)
		require.ErrorIs(t, err, ErrCaBundleInvalid, name)
		require.Empty(t, canon, name)
		require.Empty(t, certs, name)
		// Neither the key's body nor the certificate bytes may travel in the error.
		body := strings.TrimSpace(strings.SplitN(strings.SplitN(pkcs8, "\n", 2)[1], "\n-----END", 2)[0])
		require.NotContains(t, err.Error(), body, name)
		require.NotContains(t, err.Error(), "MIGH", name)
	}
}

func TestParseCaBundleRefusesWhatIsNotACertificate(t *testing.T) {
	ca := newTestCA(t, "r")
	badDER := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not a certificate")}))
	pub := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: ca.der}))
	withHeaders := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"}, Bytes: ca.der}))

	for name, in := range map[string]string{
		"prose":              "this is not PEM at all",
		"unterminated block": "-----BEGIN CERTIFICATE-----\nMIIB\n",
		"garbage base64":     "-----BEGIN CERTIFICATE-----\n!!!!\n-----END CERTIFICATE-----\n",
		"valid PEM, bad DER": badDER,
		"a public key":       pub,
		"block with headers": withHeaders,
		"a URL":              "https://pki.example.com/root.pem",
		"one bad of two":     ca.certPEM + badDER,
	} {
		_, _, err := ParseCaBundle(in)
		require.ErrorIs(t, err, ErrCaBundleInvalid, name)
		require.NotContains(t, err.Error(), "pki.example.com", name)
	}
}

func TestParseCaBundleRefusesAnOversizedBundle(t *testing.T) {
	ca := newTestCA(t, "r")
	_, _, err := ParseCaBundle(ca.certPEM + strings.Repeat("# padding\n", maxCaBundleBytes/10))
	require.ErrorIs(t, err, ErrCaBundleInvalid)
	require.Contains(t, err.Error(), "64 KiB")
}

// fakeDiscoveryHandler answers discovery for the server it is mounted on.
func tlsDiscovery(baseOf func() string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := baseOf()
		_, _ = io.WriteString(w, `{"issuer":"`+b+`","authorization_endpoint":"`+b+`/authorize","token_endpoint":"`+b+`/token","jwks_uri":"`+b+`/keys"}`)
	})
}

// THE NEGATIVE CONTROL. The same provider, the same Manager and the same egress policy:
// discovery FAILS on the certificate without the bundle and SUCCEEDS with it, so the
// success below is the bundle's doing and not a client that skips verification.
func TestDiscoveryOfAPrivateCAProviderNeedsTheBundle(t *testing.T) {
	ca := newTestCA(t, "Acme Root")
	var srv *httptest.Server
	srv = ca.startTLS(t, tlsDiscovery(func() string { return srv.URL }))

	m := NewManager(nil, nil, nil, nil, 0, 0, "", BootstrapConfig{}, nil)
	installFactory(t, m, allowLoopback)
	ctx := context.Background()

	_, err := m.FetchIdpDiscovery(ctx, srv.URL, "")
	requireUnknownAuthority(t, err)

	canon, _, err := ParseCaBundle(ca.certPEM)
	require.NoError(t, err)
	snap, err := m.FetchIdpDiscovery(ctx, srv.URL, canon)
	require.NoError(t, err)
	require.Equal(t, srv.URL, snap.Issuer)

	// A bundle for a different CA does not vouch for this provider.
	otherCanon, _, err := ParseCaBundle(newTestCA(t, "Somebody Else").certPEM)
	require.NoError(t, err)
	_, err = m.FetchIdpDiscovery(ctx, srv.URL, otherCanon)
	requireUnknownAuthority(t, err)
}

// The bundle adds to the system roots; it is not a way around the egress guard. The CA
// below DOES vouch for the loopback provider, and the default guard still refuses the dial
// before any TLS happens.
func TestABundleCannotMakeAForbiddenDestinationReachable(t *testing.T) {
	ca := newTestCA(t, "Acme Root")
	hits := 0
	srv := ca.startTLS(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	canon, _, err := ParseCaBundle(ca.certPEM)
	require.NoError(t, err)

	m := NewManager(nil, nil, nil, nil, 0, 0, "", BootstrapConfig{}, nil)
	installFactory(t, m, mscfg.InfrastructureConfiguration{}) // the default guard: no allowance
	_, err = m.FetchIdpDiscovery(context.Background(), srv.URL, canon)
	require.ErrorIs(t, err, egress.ErrBlocked)
	require.Zero(t, hits, "refused at the dial, before the provider was reached")
}

func TestABundleWithNoClientFactoryRefusesRatherThanIgnoringIt(t *testing.T) {
	ca := newTestCA(t, "Acme Root")
	m := NewManager(nil, nil, nil, nil, 0, 0, "", BootstrapConfig{}, nil)
	base, err := NewSsoHTTPClient(allowLoopback)
	require.NoError(t, err)
	m.SetSsoHTTPClient(base)

	_, err = m.FetchIdpDiscovery(context.Background(), "https://127.0.0.1:1", ca.certPEM)
	require.ErrorIs(t, err, errCaNoFactory)
}

func TestTheClientIsCachedPerConnectionVersionAndRebuiltOnChange(t *testing.T) {
	ca := newTestCA(t, "Acme Root")
	canon, _, err := ParseCaBundle(ca.certPEM)
	require.NoError(t, err)
	m := NewManager(nil, nil, nil, nil, 0, 0, "", BootstrapConfig{}, nil)
	installFactory(t, m, allowLoopback)

	plain := &iam.IdpConnection{ID: 1}
	c, err := m.ssoClientFor(plain)
	require.NoError(t, err)
	require.Same(t, m.ssoClient, c, "no bundle: the system-roots client")

	now := time.Now()
	conn := &iam.IdpConnection{ID: 2, CaBundle: canon, UpdatedAt: now}
	first, err := m.ssoClientFor(conn)
	require.NoError(t, err)
	require.NotSame(t, m.ssoClient, first)
	again, err := m.ssoClientFor(conn)
	require.NoError(t, err)
	require.Same(t, first, again, "unchanged connection: the cached client")

	conn.UpdatedAt = now.Add(time.Second)
	rebuilt, err := m.ssoClientFor(conn)
	require.NoError(t, err)
	require.NotSame(t, first, rebuilt, "an update to the connection rebuilds the client")

	conn.CaBundle = ""
	cleared, err := m.ssoClientFor(conn)
	require.NoError(t, err)
	require.Same(t, m.ssoClient, cleared, "cleared: back to the system roots")
}

// The whole sign-in over TLS: discovery at create, the token exchange and the key-set
// fetch at sign-in all verify the private CA through the connection's client.
func TestSignInThroughAPrivateCAProvider(t *testing.T) {
	e := newSsoEnv(t)
	installFactory(t, e.m, allowLoopback)
	ca := newTestCA(t, "Acme Root")
	op := ca.tlsOP(t)
	ctx := context.Background()

	// Without the bundle the connection cannot even be created.
	_, err := e.m.CreateIdpConnection(ctx, IdpConnectionInput{
		Token: "tls-idp", Name: "tls", Kind: iam.IdpKindTenant, Issuer: op.url(), ClientID: ssoTestClientID,
		ClientSecret: ssoTestSecret,
	})
	requireUnknownAuthority(t, err)
	_, err = e.m.IdpConnection(ctx, "tls-idp")
	require.ErrorIs(t, err, ErrIdpNotFound, "a refused create leaves nothing behind")

	d, err := e.m.CreateIdpConnection(ctx, IdpConnectionInput{
		Token: "tls-idp", Name: "tls", Kind: iam.IdpKindTenant, Issuer: op.url(), ClientID: ssoTestClientID,
		ClientSecret: ssoTestSecret, CaBundle: "# the root\n" + ca.certPEM,
	})
	require.NoError(t, err)
	require.Len(t, d.CaCertificates, 1)
	require.Equal(t, ca.certPEM, d.Connection.CaBundle, "stored canonical")
	_, err = e.m.ApproveIdpDiscovery(ctx, "tls-idp", d.Fingerprint)
	require.NoError(t, err)
	_, err = e.m.SetIdpConnectionTenants(ctx, "tls-idp", []string{"acme"})
	require.NoError(t, err)
	_, err = e.m.UpdateIdpConnection(ctx, "tls-idp", &IdpConnectionUpdateRequest{Enabled: optBool(true)})
	require.NoError(t, err)
	e.ssoIdentity(t, "pat@"+ssoTestDomain, "tls-idp", "tls-sub-1", "acme")

	e.op = op
	auth, _ := e.signInOK(t, "tls-idp", "tls-sub-1", "pat@"+ssoTestDomain)
	require.NotNil(t, auth)
	require.NotZero(t, op.tokenHits.Load(), "the token exchange went over TLS")
	require.NotZero(t, op.jwksHits.Load(), "the key set was fetched over TLS")

	// Refresh goes through the connection's own client too: with the system-roots client it
	// would fail on the certificate.
	_, err = e.m.RefreshIdpDiscovery(ctx, "tls-idp")
	require.NoError(t, err, "a refresh of a private-CA connection must use its bundle")

	// The create is audited with the fingerprint it added.
	rows := e.auditRows(t, rdb.AuditOpIdpCaBundleChanged)
	require.Len(t, rows, 1)
	require.Contains(t, rows[0].EntityLabel, "tls-idp")
	require.Contains(t, rows[0].EntityLabel, "added=["+d.CaCertificates[0].FingerprintSHA256+"]")
	require.Contains(t, rows[0].EntityLabel, "removed=[]")

	// Clearing the bundle returns the connection to the system roots: the provider is no
	// longer reachable, and nothing the bundle said lingers in a cached client.
	d, err = e.m.SetIdpCaBundle(ctx, "tls-idp", "")
	require.NoError(t, err)
	require.Empty(t, d.CaCertificates)
	require.Empty(t, d.Connection.CaBundle)
	rows = e.auditRows(t, rdb.AuditOpIdpCaBundleChanged)
	require.Len(t, rows, 2)
	require.Contains(t, rows[1].EntityLabel, "added=[]")
	require.Contains(t, rows[1].EntityLabel, "removed=["+d2fp(t, ca)+"]")
	l, _, _, _ := e.signIn(t, "tls-idp", "tls-sub-1", "pat@"+ssoTestDomain)
	require.NotEmpty(t, l.Error, "without the bundle the sign-in must fail")
	require.Empty(t, l.Code)
	_, err = e.m.FetchIdpDiscovery(ctx, op.url(), "")
	requireUnknownAuthority(t, err)
}

func TestSetIdpCaBundleVerifiesTheProviderBeforeSaving(t *testing.T) {
	e := newSsoEnv(t)
	installFactory(t, e.m, allowLoopback)
	ca := newTestCA(t, "Acme Root")
	op := ca.tlsOP(t)
	ctx := context.Background()

	// A connection to the TLS provider with a bundle; approved.
	d, err := e.m.CreateIdpConnection(ctx, IdpConnectionInput{
		Token: "tls-idp", Name: "tls", Kind: iam.IdpKindTenant, Issuer: op.url(), ClientID: ssoTestClientID,
		CaBundle: ca.certPEM,
	})
	require.NoError(t, err)
	d, err = e.m.ApproveIdpDiscovery(ctx, "tls-idp", d.Fingerprint)
	require.NoError(t, err)
	require.NotNil(t, d.Connection.DiscoveryApprovedAt)

	// A bundle for another CA is refused with the certificate's reason and changes nothing.
	_, err = e.m.SetIdpCaBundle(ctx, "tls-idp", newTestCA(t, "Wrong").certPEM)
	requireUnknownAuthority(t, err)
	// So is a private key, which is not echoed.
	_, err = e.m.SetIdpCaBundle(ctx, "tls-idp", "-----BEGIN PRIVATE KEY-----\nQUJDREVGR0g=\n-----END PRIVATE KEY-----\n")
	require.ErrorIs(t, err, ErrCaBundleInvalid)
	require.NotContains(t, err.Error(), "QUJDREVGR0g")
	d, err = e.m.IdpConnection(ctx, "tls-idp")
	require.NoError(t, err)
	require.Equal(t, ca.certPEM, d.Connection.CaBundle)
	require.NotNil(t, d.Connection.DiscoveryApprovedAt, "a refused change leaves the approval alone")

	// Re-saving the same bundle, which names the same endpoints, keeps the approval.
	d, err = e.m.SetIdpCaBundle(ctx, "tls-idp", ca.certPEM)
	require.NoError(t, err)
	require.NotNil(t, d.Connection.DiscoveryApprovedAt)
	require.Len(t, e.auditRows(t, rdb.AuditOpIdpCaBundleChanged), 1, "an identical bundle is not a change and is not audited")

	// A bundle under which the provider answers with DIFFERENT endpoints withdraws it.
	op.authorizePath = "/authorize-moved"
	d, err = e.m.SetIdpCaBundle(ctx, "tls-idp", ca.certPEM)
	require.NoError(t, err)
	require.Nil(t, d.Connection.DiscoveryApprovedAt, "endpoints that moved are reviewed again")
	require.Contains(t, d.Connection.DiscoverySnapshot.AuthorizationEndpoint, "/authorize-moved")
	require.Len(t, e.auditRows(t, rdb.AuditOpIdpCaBundleChanged), 1, "moved endpoints under the same bundle are not a bundle change")
}

// d2fp is the SHA-256 fingerprint of a test CA's certificate.
func d2fp(t *testing.T, ca *testCA) string {
	t.Helper()
	_, certs, err := ParseCaBundle(ca.certPEM)
	require.NoError(t, err)
	return certs[0].FingerprintSHA256
}

// SetIdpCaBundle writes only the columns it changes: a connection disabled while the
// administrator was composing the bundle stays disabled.
func TestSetIdpCaBundleDoesNotRevertAConcurrentEdit(t *testing.T) {
	e := newSsoEnv(t)
	installFactory(t, e.m, allowLoopback)
	ca := newTestCA(t, "Acme Root")
	op := ca.tlsOP(t)
	ctx := context.Background()
	_, err := e.m.CreateIdpConnection(ctx, IdpConnectionInput{
		Token: "tls-idp", Name: "tls", Kind: iam.IdpKindTenant, Issuer: op.url(), ClientID: ssoTestClientID, CaBundle: ca.certPEM,
	})
	require.NoError(t, err)

	// Another writer loaded the row, then this one changes the bundle from a stale copy.
	stale, err := e.store.IdpConnectionByToken(ctx, "tls-idp")
	require.NoError(t, err)
	live, err := e.store.IdpConnectionByToken(ctx, "tls-idp")
	require.NoError(t, err)
	live.Name = "renamed during an incident"
	require.NoError(t, e.store.UpdateIdpConnection(ctx, live))

	stale.CaBundle = ""
	require.NoError(t, e.store.UpdateIdpConnectionFields(ctx, stale, "CaBundle"))
	got, err := e.store.IdpConnectionByToken(ctx, "tls-idp")
	require.NoError(t, err)
	require.Empty(t, got.CaBundle)
	require.Equal(t, "renamed during an incident", got.Name, "the stale write reverted another column")
	require.True(t, got.UpdatedAt.After(live.UpdatedAt) || got.UpdatedAt.Equal(live.UpdatedAt))
}

// The bundle trusts the system roots in addition to its own: a provider signed by a
// "system" CA still verifies on a connection that has a bundle set.
func TestABundleIsTrustedInAdditionToTheSystemRoots(t *testing.T) {
	system := newTestCA(t, "System Root")
	private := newTestCA(t, "Private Root")
	stranger := newTestCA(t, "Stranger")
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM([]byte(system.certPEM)))
	saved := systemCertPool
	systemCertPool = func() (*x509.CertPool, error) { return pool.Clone(), nil }
	t.Cleanup(func() { systemCertPool = saved })

	m := NewManager(nil, nil, nil, nil, 0, 0, "", BootstrapConfig{}, nil)
	installFactory(t, m, allowLoopback)
	bundle, _, err := ParseCaBundle(private.certPEM)
	require.NoError(t, err)
	client, err := m.clientForBundle(bundle)
	require.NoError(t, err)

	get := func(ca *testCA) error {
		srv := ca.startTLS(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		resp, err := client.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	require.NoError(t, get(private), "the bundle's own CA")
	require.NoError(t, get(system), "a system root must still verify with a bundle set")
	requireUnknownAuthority(t, get(stranger))
}

func TestParseCaBundleCountsAndMarkers(t *testing.T) {
	var pems []string
	for i := 0; i < maxCaBundleCerts+1; i++ {
		pems = append(pems, newTestCA(t, "ca-"+string(rune('A'+i%26))+string(rune('a'+i/26))).certPEM)
	}
	_, certs, err := ParseCaBundle(strings.Join(pems[:maxCaBundleCerts], ""))
	require.NoError(t, err)
	require.Len(t, certs, maxCaBundleCerts)
	_, _, err = ParseCaBundle(strings.Join(pems, ""))
	require.ErrorIs(t, err, ErrCaBundleInvalid, "65 certificates")

	// A middle block corrupted past decoding is refused, not silently dropped.
	good := pems[0] + pems[1]
	broken := "-----BEGIN CERTIFICATE-----\n!!!not base64!!!\n-----END CERTIFICATE-----\n"
	_, _, err = ParseCaBundle(pems[0] + broken + pems[1])
	require.ErrorIs(t, err, ErrCaBundleInvalid)
	require.Contains(t, err.Error(), "could not be read")
	_, _, err = ParseCaBundle(good)
	require.NoError(t, err)
}
