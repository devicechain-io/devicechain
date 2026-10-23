// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// The client_credentials grant, driven through the REAL token handler over the real
// manager, credential checker and store, with the account rows written as the admin service
// writes them (a keyed digest under the service-account key).

type ccEnv struct {
	*mintTestEnv
	key      *credential.DeviceSecretKey
	compares atomic.Int32
	seen     [][]byte
	now      time.Time
	h        http.Handler
}

func newCCEnv(t *testing.T) *ccEnv {
	t.Helper()
	base := newMintTestEnv(t)
	k, err := credential.DeriveServiceAccountSecretKey([]byte("client-credentials-test-root-key"))
	require.NoError(t, err)
	e := &ccEnv{mintTestEnv: base, key: k, now: time.Now().UTC()}
	checker, err := credential.NewChecker(credentialtest.NewStore(), CredentialPolicies,
		credential.WithServiceAccountSecretKey(k),
		credential.WithCompareObserver(func(stored []byte) {
			e.compares.Add(1)
			e.seen = append(e.seen, append([]byte(nil), stored...))
		}))
	require.NoError(t, err)
	base.m.credentials = checker
	base.m.now = func() time.Time { return e.now }
	base.m.saAttempts.limits = &serviceAccountAttemptLimits
	base.m.saGlobal.limits = &serviceAccountGlobalLimits
	base.m.saMints.limits = &serviceAccountMintLimits
	e.h = TokenHandler(base.m.AuthenticateClient, base.m.RedeemAuthorizationCode, base.m.RefreshOAuth, base.m.ClientCredentials)
	base.seedTenant(t, "acme")
	ctx := context.Background()
	require.NoError(t, base.store.CreateTenant(ctx, &iam.Tenant{Token: "globex", Enabled: true, TierID: 1, PurgeState: iam.PurgeActive}))
	return e
}

// account writes an account in tenant holding one role with the given authorities, and a
// live secret; it returns the client id and the secret.
func (e *ccEnv) account(t *testing.T, tenant, token string, scope []string, authorities ...string) (string, string) {
	t.Helper()
	ctx := context.Background()
	e.roleSeq++
	tid := tenant
	role := &iam.Role{Scope: iam.ScopeTenant, TenantID: &tid, Token: token + "-role", Authorities: authorities}
	require.NoError(t, e.store.CreateRole(ctx, role))
	secret := "dcsas_" + strings.Repeat(token[:1], 20) + token + "-secret-value-0123456789"
	digest, err := e.key.Digest(tenant, secret)
	require.NoError(t, err)
	clientID := "dcsa_" + token + "_" + tenant
	require.NoError(t, e.store.CreateServiceAccount(ctx, &iam.ServiceAccount{
		TenantId: tenant, Token: token, ClientId: clientID, Enabled: true, Scope: scope,
		Roles:   []iam.Role{*role},
		Secrets: []iam.ServiceAccountSecret{{Digest: digest, Hint: secret[len(secret)-4:], ExpiresAt: e.now.Add(time.Hour)}},
	}))
	return clientID, secret
}

func (e *ccEnv) post(t *testing.T, form url.Values, basicID, basicSecret string) *httptest.ResponseRecorder {
	t.Helper()
	return e.postFrom(t, "192.0.2.10:5555", form, basicID, basicSecret)
}

func (e *ccEnv) postFrom(t *testing.T, remote string, form url.Values, basicID, basicSecret string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = remote
	if basicID != "" {
		req.SetBasicAuth(basicID, basicSecret)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *ccEnv) grant(t *testing.T, clientID, secret string, extra ...string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}}
	for i := 0; i+1 < len(extra); i += 2 {
		form.Add(extra[i], extra[i+1])
	}
	return e.post(t, form, clientID, secret)
}

type tokenBody struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) tokenBody {
	t.Helper()
	var b tokenBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &b), rec.Body.String())
	return b
}

func TestClientCredentialsMintsTheAccountsToken(t *testing.T) {
	e := newCCEnv(t)
	id, secret := e.account(t, "acme", "ci", nil, "device:read", "command:write")

	rec := e.grant(t, id, secret, "resource", "https://mcp.example.com", "tenant", "globex")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	b := decode(t, rec)
	require.Empty(t, b.RefreshToken, "a service account must never receive a refresh token")
	require.Equal(t, "Bearer", b.TokenType)
	require.LessOrEqual(t, b.ExpiresIn, int(auth.DefaultScopedAccessTTL.Seconds()))

	c, err := e.validator.Validate(b.AccessToken)
	require.NoError(t, err)
	require.Equal(t, "acme", c.Tenant, "the token must name the account's tenant whatever the request says")
	require.Equal(t, auth.ServiceAccountSubject("ci"), c.Username)
	require.Empty(t, c.Email)
	require.False(t, c.ActingAsSuperuser)
	require.Equal(t, id, c.ClientId)
	require.ElementsMatch(t, []string{"device:read", "command:write"}, c.Authorities,
		"the token must carry exactly the account's roles — no viewer baseline")
	require.Equal(t, []string{"https://mcp.example.com"}, []string(c.Audience))
	require.Nil(t, c.ScopeAnchors)

	// The mint is journaled with the account as the actor.
	var ev rdb.AuditEvent
	require.NoError(t, e.m.db.Database.WithContext(core.WithSystemContext(context.Background())).
		Where("operation = ?", rdb.AuditOpServiceAccountToken).First(&ev).Error)
	require.Equal(t, auth.ServiceAccountSubject("ci"), ev.Actor)
	require.Equal(t, "acme", ev.TenantId)

	// The secret's last use is recorded.
	acct, err := e.store.ServiceAccountByClientId(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, acct.Secrets[0].LastUsedAt)
}

// Every authentication failure is the same invalid_client after one compare — against the
// real digest for a wrong secret, against the dummy for everything else.
func TestClientCredentialsRefusalsAreOneAnswer(t *testing.T) {
	e := newCCEnv(t)
	id, secret := e.account(t, "acme", "ci", nil, "device:read")
	disabledID, disabledSecret := e.account(t, "acme", "off", nil, "device:read")
	acct, err := e.store.ServiceAccountByClientId(context.Background(), disabledID)
	require.NoError(t, err)
	require.NoError(t, e.store.SetServiceAccountEnabled(context.Background(), acct, false))
	expiredID, expiredSecret := e.account(t, "acme", "old", nil, "device:read")
	acct, err = e.store.ServiceAccountByClientId(context.Background(), expiredID)
	require.NoError(t, err)
	require.NoError(t, e.store.ExpireServiceAccountSecret(context.Background(), acct.Secrets[0].ID, e.now.Add(-time.Second)))

	// The control: the real secret works, through exactly one compare against its digest.
	e.compares.Store(0)
	require.Equal(t, http.StatusOK, e.grant(t, id, secret).Code)
	require.EqualValues(t, 1, e.compares.Load())

	cases := map[string][2]string{
		"wrong secret (one character)": {id, secret[:len(secret)-1] + "X"},
		"wrong secret, same hint":      {id, "dcsas_guess" + secret[len(secret)-4:]},
		"unknown client":               {"dcsa_nobody", secret},
		"disabled account":             {disabledID, disabledSecret},
		"expired secret":               {expiredID, expiredSecret},
		"another account's secret":     {disabledID, secret},
	}
	// What an unknown client is compared against IS the dummy: there is no account to
	// supply anything else. Every other no-account-to-compare case must pay the same.
	e.seen = nil
	e.grant(t, "dcsa_nobody", "x")
	require.Len(t, e.seen, 1)
	dummy := e.seen[0]
	for _, tok := range []string{"ci", "off", "old"} {
		a, err := e.store.TenantServiceAccount(context.Background(), "acme", tok)
		require.NoError(t, err)
		require.NotEqual(t, a.Secrets[0].Digest, string(dummy), "the dummy is a real account's digest")
	}
	var bodies []string
	for name, c := range cases {
		e.compares.Store(0)
		e.seen = nil
		rec := e.grant(t, c[0], c[1])
		require.Equalf(t, http.StatusUnauthorized, rec.Code, "%s: %s", name, rec.Body.String())
		require.Equalf(t, "invalid_client", decode(t, rec).Error, name)
		require.EqualValuesf(t, 1, e.compares.Load(), "%s paid %d compares, want exactly one", name, e.compares.Load())
		bodies = append(bodies, rec.Body.String())
		if name != "wrong secret, same hint" && name != "wrong secret (one character)" {
			require.Equalf(t, dummy, e.seen[0], "%s compared against something other than the dummy", name)
		}
	}
	for _, b := range bodies {
		require.Equal(t, bodies[0], b, "two refusals are distinguishable by body")
	}

	// No secret at all is refused before any compare.
	e.compares.Store(0)
	rec := e.post(t, url.Values{"grant_type": {"client_credentials"}, "client_id": {id}}, "", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Zero(t, e.compares.Load())
}

// The grant type decides which principal authenticates: an operator's OAuth client cannot
// obtain a token on this grant, and an account's credentials authenticate nothing on the
// others.
func TestTheGrantTypeDecidesThePrincipal(t *testing.T) {
	e := newCCEnv(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("oauth-client-secret"), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, e.store.CreateOAuthClient(context.Background(), &iam.OAuthClient{
		ClientId: "grafana", Enabled: true, SecretHash: string(hash), Scopes: []string{"read-only"},
	}))
	rec := e.grant(t, "grafana", "oauth-client-secret")
	require.Equal(t, http.StatusUnauthorized, rec.Code, "an OAuth client obtained a client_credentials token: %s", rec.Body.String())

	id, secret := e.account(t, "acme", "ci", nil, "device:read")
	rec = e.post(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}}, id, secret)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "a service account authenticated on the refresh grant")
	require.Equal(t, "invalid_client", decode(t, rec).Error)
}

func TestAScopedAccountsTokenCarriesItsScope(t *testing.T) {
	e := newCCEnv(t)
	id, secret := e.account(t, "acme", "cust", []string{"customer:acme-corp"}, "device:read")
	rec := e.grant(t, id, secret)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	c, err := e.validator.Validate(decode(t, rec).AccessToken)
	require.NoError(t, err)
	scope, scoped := auth.ScopeFromContext(auth.WithClaims(context.Background(), c))
	require.True(t, scoped, "a scoped account's token reads as the whole tenant")
	require.True(t, reflect.DeepEqual(scope.Anchors(), []string{"customer:acme-corp"}))
	// And the data plane refuses such a caller wherever a resolver has not opted in.
	require.ErrorIs(t, auth.Authorize(auth.WithClaims(context.Background(), c), auth.DeviceRead), auth.ErrScopeUnsupported)
}

func TestClientCredentialsScopeAndResource(t *testing.T) {
	e := newCCEnv(t)
	id, secret := e.account(t, "acme", "ci", nil, "device:read", "command:write", "location:read")

	rec := e.grant(t, id, secret, "scope", auth.ScopeReadOnly)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	b := decode(t, rec)
	require.Equal(t, auth.ScopeReadOnly, b.Scope)
	c, err := e.validator.Validate(b.AccessToken)
	require.NoError(t, err)
	require.NotContains(t, c.Authorities, "command:write", "a read-only scope kept a write authority")
	require.NotContains(t, c.Authorities, "location:read", "read-only reached position")

	for name, extra := range map[string][]string{
		"unknown scope":       {"scope", "everything"},
		"two resources":       {"resource", "https://a.example", "resource", "https://b.example"},
		"relative resource":   {"resource", "/api/mcp"},
		"http, not loopback":  {"resource", "http://mcp.example.com"},
		"fragment":            {"resource", "https://mcp.example.com/#x"},
		"not a URI at all":    {"resource", "::"},
		"a scheme we refuse":  {"resource", "ftp://mcp.example.com"},
		"an oversized target": {"resource", "https://x.example/" + strings.Repeat("a", maxResourceLen)},
	} {
		rec := e.grant(t, id, secret, extra...)
		require.Equalf(t, http.StatusBadRequest, rec.Code, "%s: %s", name, rec.Body.String())
	}
	require.Equal(t, http.StatusOK, e.grant(t, id, secret, "resource", "http://127.0.0.1:8080/mcp").Code, "the loopback carve-out")
}

func TestClientCredentialsRefusesADeletedOrDisabledTenant(t *testing.T) {
	e := newCCEnv(t)
	id, secret := e.account(t, "globex", "ci", nil, "device:read")
	require.Equal(t, http.StatusOK, e.grant(t, id, secret).Code)
	sys := e.m.db.Database.WithContext(core.WithSystemContext(context.Background()))
	require.NoError(t, sys.Model(&iam.Tenant{}).Where("token = ?", "globex").Update("enabled", false).Error)
	rec := e.grant(t, id, secret)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_grant", decode(t, rec).Error)

	require.NoError(t, sys.Model(&iam.Tenant{}).Where("token = ?", "globex").
		Updates(map[string]any{"enabled": true, "purge_state": iam.PurgePurging}).Error)
	rec = e.grant(t, id, secret)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_grant", decode(t, rec).Error)
}

// Rotation overlap, at the endpoint: both secrets work during the overlap and only the new
// one after it.
func TestRotationOverlapAtTheEndpoint(t *testing.T) {
	e := newCCEnv(t)
	id, oldSecret := e.account(t, "acme", "ci", nil, "device:read")
	acct, err := e.store.ServiceAccountByClientId(context.Background(), id)
	require.NoError(t, err)
	newSecret := "dcsas_the-rotated-secret-value-abcdefghijklmno"
	digest, err := e.key.Digest("acme", newSecret)
	require.NoError(t, err)
	require.NoError(t, e.store.ExpireServiceAccountSecret(context.Background(), acct.Secrets[0].ID, e.now.Add(10*time.Minute)))
	require.NoError(t, e.store.AddServiceAccountSecret(context.Background(), &iam.ServiceAccountSecret{
		ServiceAccountID: acct.ID, Digest: digest, Hint: newSecret[len(newSecret)-4:], ExpiresAt: e.now.Add(24 * time.Hour),
	}))

	require.Equal(t, http.StatusOK, e.grant(t, id, oldSecret).Code, "the old secret died inside its overlap")
	require.Equal(t, http.StatusOK, e.grant(t, id, newSecret).Code)
	e.now = e.now.Add(11 * time.Minute)
	require.Equal(t, http.StatusUnauthorized, e.grant(t, id, oldSecret).Code, "the old secret outlived its overlap")
	require.Equal(t, http.StatusOK, e.grant(t, id, newSecret).Code)
}

func TestClientCredentialsIsRateLimited(t *testing.T) {
	e := newCCEnv(t)
	// The per-ACCOUNT mint bucket: a runaway integration is slowed after its burst...
	id, secret := e.account(t, "acme", "ci", nil, "device:read")
	for i := 0; i < serviceAccountMintLimits.perKeyBurst; i++ {
		require.Equal(t, http.StatusOK, e.grant(t, id, secret).Code, "mint %d", i)
	}
	rec := e.grant(t, id, secret)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "slow_down", decode(t, rec).Error)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))

	// ...but strangers' failures never drain it: wrong secrets from elsewhere leave another
	// account's mints alone.
	other, otherSecret := e.account(t, "acme", "etl", nil, "device:read")
	e.m.saAttempts = sourceLimiter{limits: &serviceAccountAttemptLimits}
	for i := 0; i < 15; i++ {
		require.Equal(t, http.StatusUnauthorized, e.grant(t, other, "wrong").Code)
	}
	require.Equal(t, http.StatusOK, e.grant(t, other, otherSecret).Code, "failed attempts starved the account")

	// The per-SOURCE attempt bucket bounds a spray before any compare.
	e.m.saAttempts = sourceLimiter{limits: &serviceAccountAttemptLimits}
	e.compares.Store(0)
	for i := 0; i < serviceAccountAttemptLimits.perKeyBurst; i++ {
		e.grant(t, "dcsa_spray", "x")
	}
	n := e.compares.Load()
	rec = e.grant(t, "dcsa_spray", "x")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, n, e.compares.Load(), "a refused attempt still paid a compare")
}

// A stranger who sprays from enough sources to exhaust the GLOBAL bucket cannot hold a
// running integration at the limit: a source that minted recently is exempt from it. A
// source with no recent mint is still refused, which is what the global bucket is for.
func TestAGlobalSprayDoesNotStopARunningIntegration(t *testing.T) {
	e := newCCEnv(t)
	id, secret := e.account(t, "acme", "ci", nil, "device:read")
	form := url.Values{"grant_type": {"client_credentials"}}
	integration := "198.51.100.7:4000"
	require.Equal(t, http.StatusOK, e.postFrom(t, integration, form, id, secret).Code)

	// The global bucket's clock stands still, so it cannot refill while the spray runs.
	frozen := time.Now()
	e.m.saGlobal.now = func() time.Time { return frozen }
	// The spray: one wrong attempt from each of more distinct sources than the global burst.
	for i := 0; i <= serviceAccountGlobalLimits.perKeyBurst; i++ {
		e.postFrom(t, fmt.Sprintf("203.0.%d.%d:1000", i/250, i%250+1), form, "dcsa_spray", "x")
	}
	rec := e.postFrom(t, "192.0.2.200:1000", form, id, secret)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "the global bucket was never exhausted, so this test proves nothing: %s", rec.Body.String())
	require.Equal(t, "1", rec.Header().Get("Retry-After"), "a sub-second refill must round up, not drop the header")

	rec = e.postFrom(t, integration, form, id, secret)
	require.Equal(t, http.StatusOK, rec.Code, "a stranger's spray held a running integration at the global limit: %s", rec.Body.String())

	// The exemption ages out.
	e.now = e.now.Add(serviceAccountTrustedSourceTTL + time.Minute)
	require.False(t, e.m.saTrusted.has(attemptSourceKey("198.51.100.7"), e.now))
}

// An IPv6 sender cannot multiply its per-source allowance by walking the addresses of its
// own /64.
func TestAttemptSourceKeyGroupsAnIPv6Subnet(t *testing.T) {
	require.Equal(t, attemptSourceKey("2001:db8:1:2::1"), attemptSourceKey("2001:db8:1:2:ffff:ffff:ffff:fffe"))
	require.NotEqual(t, attemptSourceKey("2001:db8:1:2::1"), attemptSourceKey("2001:db8:1:3::1"))
	require.Equal(t, "192.0.2.1", attemptSourceKey("192.0.2.1"))
	require.Equal(t, "192.0.2.1", attemptSourceKey("::ffff:192.0.2.1"))
	require.NotEqual(t, attemptSourceKey("192.0.2.1"), attemptSourceKey("192.0.2.2"))
	require.Equal(t, "not-an-address", attemptSourceKey("not-an-address"))

	e := newCCEnv(t)
	form := url.Values{"grant_type": {"client_credentials"}}
	for i := 0; i < serviceAccountAttemptLimits.perKeyBurst; i++ {
		e.postFrom(t, fmt.Sprintf("[2001:db8:1:2::%x]:443", i+1), form, "dcsa_spray", "x")
	}
	rec := e.postFrom(t, "[2001:db8:1:2::abcd]:443", form, "dcsa_spray", "x")
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "a fresh address in the same /64 got a fresh allowance")
}

func TestTheMetadataAdvertisesClientCredentials(t *testing.T) {
	md := BuildAuthorizationServerMetadata("https://as.example.com")
	require.Contains(t, md.GrantTypesSupported, "client_credentials")
}
