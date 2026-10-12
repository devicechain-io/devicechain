// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/secrets"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/golang-jwt/jwt/v5"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// An in-process OpenID provider and the harness that drives the whole sign-in through the
// HTTP surface: beginSso (the Manager call the resolver makes), the provider's authorization
// step (played by the test as the browser), the REAL callback handler on a mux, and
// completeSso. Nothing here calls an internal function of the flow.

const (
	ssoTestIssuerURL = "https://dc.example.com/api/user-management"
	ssoTestReturnTo  = "https://dc.example.com/login/sso/complete"
	ssoTestClientID  = "dc-client"
	ssoTestSecret    = "s3cret-value-of-the-client"
	ssoTestDomain    = "acme-corp.com"
)

var (
	opKeyOnce sync.Once
	opKey     *rsa.PrivateKey
)

// sharedOPKey is one RSA key for every stub provider in the package: generating a key per
// test would dominate the suite's runtime.
func sharedOPKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	opKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		opKey = k
	})
	return opKey
}

type opGrant struct {
	sub, email, nonce, challenge, redirect, client string
	used                                           bool
}

// stubOP is an OpenID provider: discovery, key set, and a token endpoint that checks the
// client's credentials, redirect URI and PKCE verifier like a real one.
type stubOP struct {
	t   *testing.T
	srv *httptest.Server
	key *rsa.PrivateKey
	kid string

	mu     sync.Mutex
	grants map[string]*opGrant

	discoveryHits, jwksHits, tokenHits atomic.Int32

	// mutate edits the ID token's claims before signing; method is how it is signed (RS256
	// when nil); jwks overrides the key-set body; issuerInDoc overrides the discovery
	// document's issuer.
	mutate      func(c jwt.MapClaims)
	method      jwt.SigningMethod
	jwks        func() []byte
	issuerInDoc string
	omitIDToken bool
	// authorizePath moves the authorization endpoint (default /authorize).
	authorizePath string
	// signKey signs with a key other than the published one.
	signKey *rsa.PrivateKey
	// endSession, when set, is published as the end_session_endpoint (a path and query on
	// the stub's own address).
	endSession string
}

func newStubOP(t *testing.T) *stubOP {
	t.Helper()
	op := &stubOP{t: t, key: sharedOPKey(t), kid: "op-key-1", grants: map[string]*opGrant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", op.discovery)
	mux.HandleFunc("/keys", op.keys)
	mux.HandleFunc("/token", op.token)
	op.srv = httptest.NewServer(mux)
	t.Cleanup(op.srv.Close)
	return op
}

func (op *stubOP) url() string { return op.srv.URL }

func (op *stubOP) authzPath() string {
	if op.authorizePath != "" {
		return op.authorizePath
	}
	return "/authorize"
}

func (op *stubOP) discovery(w http.ResponseWriter, r *http.Request) {
	op.discoveryHits.Add(1)
	iss := op.issuerInDoc
	if iss == "" {
		iss = op.url()
	}
	doc := map[string]string{
		"issuer":                 iss,
		"authorization_endpoint": op.url() + op.authzPath(),
		"token_endpoint":         op.url() + "/token",
		"jwks_uri":               op.url() + "/keys",
	}
	if op.endSession != "" {
		doc["end_session_endpoint"] = op.url() + op.endSession
	}
	_ = json.NewEncoder(w).Encode(doc)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (op *stubOP) keys(w http.ResponseWriter, r *http.Request) {
	op.jwksHits.Add(1)
	if op.jwks != nil {
		_, _ = w.Write(op.jwks())
		return
	}
	pub := op.key.PublicKey
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": op.kid, "use": "sig", "alg": "RS256",
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

func (op *stubOP) token(w http.ResponseWriter, r *http.Request) {
	op.tokenHits.Add(1)
	fail := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": msg})
	}
	if err := r.ParseForm(); err != nil {
		fail("form")
		return
	}
	// Client authentication: Basic or form, the way oauth2 may send it.
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	} else if u, err := url.QueryUnescape(id); err == nil {
		id = u
		secret, _ = url.QueryUnescape(secret)
	}
	if id != ssoTestClientID || secret != ssoTestSecret {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
		return
	}
	op.mu.Lock()
	g := op.grants[r.PostForm.Get("code")]
	if g == nil || g.used {
		op.mu.Unlock()
		fail("unknown or used code")
		return
	}
	g.used = true
	op.mu.Unlock()
	if r.PostForm.Get("redirect_uri") != g.redirect {
		fail("redirect_uri mismatch")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if b64(sum[:]) != g.challenge {
		fail("PKCE verification failed")
		return
	}

	claims := jwt.MapClaims{
		"iss": op.url(), "sub": g.sub, "aud": ssoTestClientID,
		"exp": time.Now().Add(5 * time.Minute).Unix(), "iat": time.Now().Unix(),
		"nonce": g.nonce, "email": g.email, "email_verified": true,
	}
	if op.mutate != nil {
		op.mutate(claims)
	}
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"access_token": "opaque", "token_type": "Bearer", "expires_in": 3600}
	if !op.omitIDToken {
		resp["id_token"] = op.sign(claims)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (op *stubOP) sign(claims jwt.MapClaims) string {
	method := op.method
	if method == nil {
		method = jwt.SigningMethodRS256
	}
	tok := jwt.NewWithClaims(method, claims)
	tok.Header["kid"] = op.kid
	var key any = op.key
	switch {
	case method == jwt.SigningMethodNone:
		key = jwt.UnsafeAllowNoneSignatureType
	case method == jwt.SigningMethodHS256:
		// Signed with the one secret the provider and the relying party share.
		key = []byte(ssoTestSecret)
	case op.signKey != nil:
		key = op.signKey
	}
	s, err := tok.SignedString(key)
	require.NoError(op.t, err)
	return s
}

// authorize plays the provider's sign-in page: the person authenticates as (sub, email), and
// the provider issues a code bound to what the authorization URL asked for. It returns the
// state and code the browser would be redirected back with. It also checks the URL itself.
func (op *stubOP) authorize(authURL, sub, email string) (state, code string) {
	op.t.Helper()
	u, err := url.Parse(authURL)
	require.NoError(op.t, err)
	require.True(op.t, strings.HasPrefix(authURL, op.url()+op.authzPath()+"?"), "the browser is sent to the approved authorization endpoint: %s", authURL)
	q := u.Query()
	require.Equal(op.t, "code", q.Get("response_type"))
	require.Equal(op.t, ssoTestClientID, q.Get("client_id"))
	require.Equal(op.t, "S256", q.Get("code_challenge_method"), "the upstream exchange uses PKCE")
	require.NotEmpty(op.t, q.Get("code_challenge"))
	require.NotEmpty(op.t, q.Get("nonce"))
	require.NotEmpty(op.t, q.Get("state"))
	require.Contains(op.t, strings.Fields(q.Get("scope")), "openid")
	code = b64(randBytes(24))
	op.mu.Lock()
	op.grants[code] = &opGrant{
		sub: sub, email: email, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"),
		redirect: q.Get("redirect_uri"), client: q.Get("client_id"),
	}
	op.mu.Unlock()
	return q.Get("state"), code
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// recMetrics records what the flow counts.
type recMetrics struct {
	mu     sync.Mutex
	logins map[string]int
	fetch  map[string]int
}

func (r *recMetrics) Login(connection, outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.logins == nil {
		r.logins = map[string]int{}
	}
	r.logins[connection+"/"+outcome]++
}

func (r *recMetrics) IdpFetchFailure(connection string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fetch == nil {
		r.fetch = map[string]int{}
	}
	r.fetch[connection]++
}

func (r *recMetrics) count(connection, outcome string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logins[connection+"/"+outcome]
}

// jetStream starts an in-process JetStream server and returns its context.
func jetStream(t *testing.T) nats.JetStreamContext {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, ServerName: "sso-test",
		JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "the test broker never became ready")
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	return js
}

// ssoEnv is a Manager wired for single sign-on over the session harness, with a stub
// provider, a tenant connection "acme-idp" serving acme (domain acme-corp.com), and the
// REAL callback handler on a mux.
type ssoEnv struct {
	*sessionEnv
	op      *stubOP
	js      nats.JetStreamContext
	mux     *http.ServeMux
	metrics *recMetrics
	sources atomic.Int32
}

func newSsoEnv(t *testing.T) *ssoEnv {
	t.Helper()
	base := newSessionEnv(t)
	e := &ssoEnv{sessionEnv: base, op: newStubOP(t), metrics: &recMetrics{}}
	e.js = jetStream(t)
	state, err := e.js.CreateKeyValue(&nats.KeyValueConfig{Bucket: "sso-state", TTL: SsoStateTTL})
	require.NoError(t, err)
	codes, err := e.js.CreateKeyValue(&nats.KeyValueConfig{Bucket: "sso-codes", TTL: SsoCodeTTL})
	require.NoError(t, err)

	m := base.m
	m.issuerUrl = ssoTestIssuerURL
	m.ssoClient = &http.Client{
		Timeout:       5 * time.Second,
		Transport:     boundResponses(http.DefaultTransport, maxIdpResponseBytes),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	m.SetSsoStores(state, codes)
	m.SetSsoMetrics(e.metrics)

	// The REAL secret store over the test database, so the client secret is sealed and
	// resolved the way the service does it.
	sys := m.db.Database.WithContext(core.WithSystemContext(context.Background()))
	require.NoError(t, secrets.NewSecretStoreSchema().Migrate(sys))
	store, err := secrets.NewFromConfig(context.Background(), rootKeyConfig(t), m.db.Database)
	require.NoError(t, err)
	m.secrets = store

	e.mux = http.NewServeMux()
	e.mux.Handle(SsoCallbackPattern, SsoCallbackHandler(m))

	e.addConnection(t, "acme-idp", iam.IdpKindTenant, ssoTestDomain, "acme")
	return e
}

// addConnection creates, approves and enables a connection through the administrator's
// calls, with the domain asserted and the tenants set.
func (e *ssoEnv) addConnection(t *testing.T, token string, kind iam.IdpKind, domain string, tenants ...string) *iam.IdpConnection {
	t.Helper()
	ctx := context.Background()
	in := IdpConnectionInput{
		Token: token, Name: token, Kind: kind, Issuer: e.op.url(), ClientID: ssoTestClientID,
		ClientSecret: ssoTestSecret,
	}
	if kind == iam.IdpKindInstance {
		in.MfaEnforcedByIdp = true
	}
	d, err := e.m.CreateIdpConnection(ctx, in)
	require.NoError(t, err)
	_, err = e.m.ApproveIdpDiscovery(ctx, token, d.Fingerprint)
	require.NoError(t, err)
	if domain != "" {
		_, err = e.m.AddIdpDomain(ctx, token, domain)
		require.NoError(t, err)
	}
	if kind == iam.IdpKindTenant {
		_, err = e.m.SetIdpConnectionTenants(ctx, token, tenants)
		require.NoError(t, err)
	}
	enabled := true
	_, err = e.m.UpdateIdpConnection(ctx, token, &IdpConnectionUpdateRequest{
		Enabled: dcgraphql.OptionalBool{Set: true, Value: &enabled},
	})
	require.NoError(t, err)
	conn, err := e.store.IdpConnectionByToken(ctx, token)
	require.NoError(t, err)
	return conn
}

func (e *ssoEnv) reloadConn(t *testing.T, token string) *iam.IdpConnection {
	t.Helper()
	c, err := e.store.IdpConnectionByToken(context.Background(), token)
	require.NoError(t, err)
	return c
}

// addTenant creates another active tenant on the harness's tier.
func (e *ssoEnv) addTenant(t *testing.T, token string) {
	t.Helper()
	ctx := context.Background()
	ts, err := e.store.ListTenants(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, ts)
	require.NoError(t, e.store.CreateTenant(ctx, &iam.Tenant{
		Token: token, Enabled: true, TierID: ts[0].TierID, PurgeState: iam.PurgeActive,
	}))
}

// ssoIdentity creates an SSO-only identity (no password) with a membership in each tenant,
// and links (conn, subject) to it. conn "" means do not link.
func (e *ssoEnv) ssoIdentity(t *testing.T, email, connToken, subject string, tenants ...string) *iam.Identity {
	t.Helper()
	ctx := context.Background()
	id := &iam.Identity{Email: email, Enabled: true}
	require.NoError(t, e.store.CreateIdentity(ctx, id))
	for _, tn := range tenants {
		_, err := e.admin.AddMembership(ctx, email, tn, nil)
		require.NoError(t, err)
	}
	if connToken != "" {
		require.NoError(t, e.m.LinkIdentity(ctx, email, connToken, subject))
	}
	got, err := e.store.IdentityByEmail(ctx, email)
	require.NoError(t, err)
	return got
}

// verifierPair returns a fresh browser secret and its challenge.
func verifierPair() (verifier, challenge string) {
	verifier = b64(randBytes(32))
	sum := sha256.Sum256([]byte(verifier))
	return verifier, b64(sum[:])
}

// source returns a fresh source address, so a test's many begins do not share a rate bucket.
func (e *ssoEnv) source() string { return fmt.Sprintf("198.51.100.%d", e.sources.Add(1)) }

// outcome is what the browser ends up with after a callback.
type ssoLanding struct {
	Status   int
	Location *url.URL // nil when the handler answered without a redirect
	Code     string
	Error    string
	Linked   bool
}

func (e *ssoEnv) callback(t *testing.T, connToken string, q url.Values) ssoLanding {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, SsoCallbackPrefix+connToken+"?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	l := ssoLanding{Status: rec.Code}
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
	if loc := rec.Header().Get("Location"); loc != "" {
		u, err := url.Parse(loc)
		require.NoError(t, err)
		l.Location = u
		l.Code = u.Query().Get("code")
		l.Error = u.Query().Get("error")
		l.Linked = u.Query().Get("linked") == "1"
		require.Equal(t, ssoTestReturnTo, u.Scheme+"://"+u.Host+u.Path, "the browser goes back only to the return page")
	}
	return l
}

// signIn runs the whole flow for (sub, email) through connection connToken. It returns the
// landing, the browser's verifier, and the state and provider code used (for replays).
func (e *ssoEnv) signIn(t *testing.T, connToken, sub, email string) (l ssoLanding, verifier, state, code string) {
	t.Helper()
	verifier, challenge := verifierPair()
	begin, err := e.m.BeginSso(context.Background(), BeginSsoInput{
		Connection: connToken, ReturnTo: ssoTestReturnTo, CodeChallenge: challenge,
	}, e.source())
	require.NoError(t, err)
	require.NotNil(t, begin)
	state, code = e.op.authorize(begin.AuthorizationURL, sub, email)
	l = e.callback(t, connToken, url.Values{"state": {state}, "code": {code}})
	return l, verifier, state, code
}

// signInOK is signIn that must end in a login code, redeemed here.
func (e *ssoEnv) signInOK(t *testing.T, connToken, sub, email string) (*IdentityAuth, string) {
	t.Helper()
	l, verifier, _, _ := e.signIn(t, connToken, sub, email)
	require.Equal(t, http.StatusFound, l.Status)
	require.Empty(t, l.Error, "sign-in was refused")
	require.NotEmpty(t, l.Code)
	got, err := e.m.CompleteSso(context.Background(), l.Code, verifier)
	require.NoError(t, err)
	return got, verifier
}
