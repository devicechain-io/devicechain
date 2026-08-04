// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/devicechain-io/dc-microservice/messaging"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// aBcryptHash is a real bcrypt hash used wherever a seed client's SecretHash must be
// well-formed (config now rejects a non-bcrypt hash — e.g. cleartext).
func aBcryptHash(t *testing.T) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte("a-client-secret"), bcrypt.MinCost)
	assert.NoError(t, err)
	return string(h)
}

// Loading an empty document defaults the superuser (ADR-033) so the documented
// first login works (ADR-022 decision 1 defaulting via core.LoadConfiguration).
// This is platform-breaking if it regresses.
func TestLoadDefaultsSuperuser(t *testing.T) {
	cfg := &UserManagementConfiguration{}
	err := core.LoadConfiguration([]byte(``), cfg)

	assert.NoError(t, err)
	assert.Equal(t, "superuser@devicechain.local", cfg.Auth.SuperuserEmail)
	assert.Equal(t, "devicechain", cfg.Auth.SuperuserPassword)
	assert.Equal(t, 900, cfg.Auth.AccessTokenTtlSeconds)
	assert.Equal(t, 604800, cfg.Auth.RefreshTokenTtlSeconds)
	assert.Equal(t, 8, cfg.Auth.SigningKeyRetentionDays)
	assert.Equal(t, 0, cfg.Auth.SigningKeyMaxAgeDays)
	assert.NoError(t, cfg.Validate())
}

// The constructor and the load path share one source of defaults.
func TestDefaultConfigurationValid(t *testing.T) {
	cfg := NewUserManagementConfiguration()
	assert.Equal(t, "superuser@devicechain.local", cfg.Auth.SuperuserEmail)
	assert.NoError(t, cfg.Validate())
}

// A non-positive refresh-token TTL fails validation closed so a key-value store
// is never created with a zero/negative TTL.
func TestValidateRejectsNonPositiveRefreshTtl(t *testing.T) {
	cfg := &UserManagementConfiguration{
		Auth: AuthConfiguration{
			AccessTokenTtlSeconds:  900,
			RefreshTokenTtlSeconds: -1,
			SuperuserEmail:         "superuser@devicechain.local",
			SuperuserPassword:      "devicechain",
		},
	}
	assert.Error(t, cfg.Validate())
}

// OAuth is off by default (no issuer configured) — the AS surface stays
// fail-closed until an operator sets an issuer URL (ADR-047).
func TestOAuthDisabledByDefault(t *testing.T) {
	cfg := NewUserManagementConfiguration()
	assert.False(t, cfg.OAuthEnabled())
	assert.NoError(t, cfg.Validate())
}

// A configured issuer URL turns OAuth on and passes validation.
func TestOAuthEnabledWithIssuer(t *testing.T) {
	cfg := NewUserManagementConfiguration()
	cfg.Auth.IssuerUrl = "https://devicechain.example.com/user-management"
	assert.True(t, cfg.OAuthEnabled())
	assert.NoError(t, cfg.Validate())
}

// The issuer URL must be a well-formed absolute https origin (http tolerated only
// for localhost), with no query/fragment/trailing slash, so it compares
// byte-for-byte with what clients derive from RFC 8414 discovery.
func TestValidateIssuerUrl(t *testing.T) {
	cases := []struct {
		name string
		url  string
		ok   bool
	}{
		{"https ok", "https://devicechain.example.com", true},
		{"https with path ok", "https://devicechain.example.com/user-management", true},
		{"http localhost ok", "http://localhost:8080", true},
		{"http 127.0.0.1 ok", "http://127.0.0.1:8080", true},
		{"http non-localhost rejected", "http://devicechain.example.com", false},
		{"trailing slash rejected", "https://devicechain.example.com/", false},
		{"query rejected", "https://devicechain.example.com?x=1", false},
		{"fragment rejected", "https://devicechain.example.com#f", false},
		{"bare question mark rejected", "https://devicechain.example.com?", false},
		{"bare hash rejected", "https://devicechain.example.com#", false},
		{"userinfo rejected", "https://user:pass@devicechain.example.com", false},
		{"uppercase scheme rejected", "HTTPS://devicechain.example.com", false},
		{"uppercase host rejected", "https://Devicechain.Example.COM", false},
		{"relative rejected", "/user-management", false},
		{"no host rejected", "https://", false},
		{"garbage rejected", "://nope", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NewUserManagementConfiguration()
			cfg.Auth.IssuerUrl = tc.url
			err := cfg.Validate()
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// A seedClients document decodes (camelCase JSON → struct fields) and validates —
// the injection point dcctl uses to provision a confidential Grafana client.
func TestSeedClientsDecodeAndValidate(t *testing.T) {
	doc := `{
	  "auth": {
	    "seedClients": [
	      {
	        "clientId": "grafana",
	        "redirectUris": ["https://dc.example.com/grafana/login/generic_oauth"],
	        "scopes": ["read-only"],
	        "secretHash": "` + aBcryptHash(t) + `"
	      }
	    ]
	  }
	}`
	cfg := &UserManagementConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(doc), cfg))
	assert.NoError(t, cfg.Validate())

	if assert.Len(t, cfg.Auth.SeedClients, 1) {
		sc := cfg.Auth.SeedClients[0]
		assert.Equal(t, "grafana", sc.ClientId)
		assert.Equal(t, []string{"https://dc.example.com/grafana/login/generic_oauth"}, sc.RedirectURIs)
		assert.Equal(t, []string{"read-only"}, sc.Scopes)
		assert.NotEmpty(t, sc.SecretHash)
	}
}

// Each seed-client entry is validated fail-closed at startup, mirroring the admin
// API's rules, so a malformed provisioned client cannot boot the service.
func TestValidateSeedClients(t *testing.T) {
	base := func(sc SeedOAuthClientConfig) *UserManagementConfiguration {
		cfg := NewUserManagementConfiguration()
		cfg.Auth.SeedClients = []SeedOAuthClientConfig{sc}
		return cfg
	}
	ok := SeedOAuthClientConfig{
		ClientId: "grafana", RedirectURIs: []string{"https://dc.example.com/grafana/login/generic_oauth"},
		Scopes: []string{"read-only"}, SecretHash: aBcryptHash(t),
	}
	assert.NoError(t, base(ok).Validate(), "a well-formed confidential seed client is valid")

	pub := ok
	pub.SecretHash = ""
	assert.NoError(t, base(pub).Validate(), "an empty secret hash (public client) is allowed")

	bad := map[string]SeedOAuthClientConfig{
		"empty clientId":       {ClientId: "", RedirectURIs: ok.RedirectURIs, Scopes: ok.Scopes},
		"bad clientId charset": {ClientId: "bad id", RedirectURIs: ok.RedirectURIs, Scopes: ok.Scopes},
		"no redirect":          {ClientId: "grafana", RedirectURIs: nil, Scopes: ok.Scopes},
		"http non-loopback":    {ClientId: "grafana", RedirectURIs: []string{"http://dc.example.com/cb"}, Scopes: ok.Scopes},
		"no scope":             {ClientId: "grafana", RedirectURIs: ok.RedirectURIs, Scopes: nil},
		"unknown scope":        {ClientId: "grafana", RedirectURIs: ok.RedirectURIs, Scopes: []string{"write"}},
		// The secret must be a real bcrypt hash — cleartext or a malformed string is
		// rejected at boot rather than failing confusingly later.
		"cleartext secret":  {ClientId: "grafana", RedirectURIs: ok.RedirectURIs, Scopes: ok.Scopes, SecretHash: "my-plaintext-secret"},
		"malformed hash":    {ClientId: "grafana", RedirectURIs: ok.RedirectURIs, Scopes: ok.Scopes, SecretHash: "$2a$10$tooshort"},
		"non-bcrypt scheme": {ClientId: "grafana", RedirectURIs: ok.RedirectURIs, Scopes: ok.Scopes, SecretHash: "$argon2id$v=19$m=65536"},
	}
	for name, sc := range bad {
		t.Run(name, func(t *testing.T) { assert.Error(t, base(sc).Validate()) })
	}

	// Duplicate clientIds across entries are rejected.
	dup := NewUserManagementConfiguration()
	dup.Auth.SeedClients = []SeedOAuthClientConfig{ok, ok}
	assert.Error(t, dup.Validate(), "duplicate clientId rejected")
}

// The purge coordinator's defaults come from the load path, not just the constructor —
// an instance whose config document says nothing about tenant purging still gets a
// coordinator that runs (ADR-077). A deleted tenant that is never swept is the whole
// defect this exists to fix, so "unset" must not mean "off".
func TestTenantPurgeDefaultsFromAnEmptyDocument(t *testing.T) {
	cfg := &UserManagementConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.Equal(t, 60, cfg.TenantPurge.IntervalSeconds)
	assert.Equal(t, 300, cfg.TenantPurge.SettleSeconds)
	assert.Equal(t, 60*time.Second, cfg.TenantPurgeInterval())
	assert.Equal(t, 300*time.Second, cfg.TenantPurgeSettle())
	assert.NoError(t, cfg.Validate())
}

// A negative interval disables the coordinator, following the platform's sweep
// convention. main.go reads a zero duration as "do not construct it", so this is the
// only value that turns it off — and it is safe, because the tenant row is the work
// list: nothing is lost, the purges simply wait.
func TestNegativeTenantPurgeIntervalDisablesTheCoordinator(t *testing.T) {
	cfg := &UserManagementConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(`{"tenantPurge":{"intervalSeconds":-1}}`), cfg))
	assert.Equal(t, time.Duration(0), cfg.TenantPurgeInterval())
	assert.NoError(t, cfg.Validate())
}

// A configured interval survives defaulting rather than being overwritten by it.
//
// The settle value here is 180s rather than something shorter because Validate now floors
// it at the broker's retained-message cache — see
// TestASettleWindowShorterThanTheBrokersRetainedCacheIsRefused. That floor is the reason
// this fixture changed, and the fixture is not the thing under test here.
func TestTenantPurgeIntervalIsConfigurable(t *testing.T) {
	cfg := &UserManagementConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(`{"tenantPurge":{"intervalSeconds":15,"settleSeconds":180}}`), cfg))
	assert.Equal(t, 15*time.Second, cfg.TenantPurgeInterval())
	assert.Equal(t, 180*time.Second, cfg.TenantPurgeSettle())
	assert.NoError(t, cfg.Validate())
}

// TestASettleWindowShorterThanTheBrokersRetainedCacheIsRefused pins the floor at the one
// place that protects an operator who DOES touch the knob.
//
// 🔴 The default clearing the floor protects nobody who changed it, and the value that
// looks most reasonable to change it to — "shorten the window so a test purge finishes
// sooner" — is exactly the one that breaks it. A purge completing inside the broker's
// retained-message cache writes a deletion record saying the tenant's data is gone while
// the broker is still handing its retained payloads to whoever subscribes next. The cache
// TTL is compiled into nats-server with no knob of its own, so this side is the only place
// the constraint can be enforced.
func TestASettleWindowShorterThanTheBrokersRetainedCacheIsRefused(t *testing.T) {
	cfg := NewUserManagementConfiguration()
	cfg.TenantPurge.SettleSeconds = int(messaging.RetainedCacheWindow.Seconds()) - 1

	err := cfg.Validate()

	require.Error(t, err, "a settle window inside the retained-message cache lets a purge "+
		"complete while the broker is still serving the deleted tenant's payloads")
	assert.Contains(t, err.Error(), "retained-message cache")

	// The boundary is inclusive-safe on the other side: exactly the cache window is not
	// enough, one second more is. Asserting both is what stops the check drifting into
	// either a no-op or a blanket refusal.
	cfg.TenantPurge.SettleSeconds = int(messaging.RetainedCacheWindow.Seconds())
	assert.Error(t, cfg.Validate(), "equal is not longer")
	cfg.TenantPurge.SettleSeconds = int(messaging.RetainedCacheWindow.Seconds()) + 1
	assert.NoError(t, cfg.Validate())
}

// A negative settle window is refused, and it is NOT the same knob as a negative
// interval. Disabling the coordinator leaves every purge pending; a negative settle
// would let one COMPLETE without ever having observed the stores clean — releasing the
// token and recording an erasure on the strength of a delete that returned no error.
func TestNegativeSettleWindowIsRefused(t *testing.T) {
	cfg := NewUserManagementConfiguration()
	cfg.TenantPurge.SettleSeconds = -1
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "settleSeconds")
}

// The token-hold default is TIED to the broker credential's lifetime, not chosen to look
// round. Completion removes the tenant row, and every device-plane gate reads an unknown
// tenant as not deleted — so releasing the token re-opens the door to any session
// established before the delete, which survives until its credential expires. Reading the
// constant means the two cannot drift apart silently.
func TestTokenHoldDefaultTracksTheBrokerCredentialLifetime(t *testing.T) {
	cfg := &UserManagementConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.Equal(t, natsauth.DefaultUserJWTTTL, cfg.TenantPurgeTokenHold())
	assert.Greater(t, cfg.TenantPurgeTokenHold(), cfg.TenantPurgeSettle(),
		"the hold answers a longer-lived question than settling and must not be the shorter of the two")
}

// A negative token hold is refused for the same reason a negative settle window is: it
// would release the token while a pre-deletion session can still write under it.
func TestNegativeTokenHoldIsRefused(t *testing.T) {
	cfg := NewUserManagementConfiguration()
	cfg.TenantPurge.TokenHoldSeconds = -1
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tokenHoldSeconds")
}

// TestTheSettleDefaultOutlastsTheBrokersRetainedCache pins a relationship between two
// constants in different packages that nothing else would notice breaking.
//
// 🔴 A STREAM PURGE IS NOT AN ERASURE FOR UP TO TWO MINUTES. nats-server answers a new
// subscriber for a retained message from an in-memory cache before it reads JetStream, so
// a tenant's retained payloads keep being delivered after the purge reports zero messages.
// The TTL is a compiled-in server constant with no knob. If the settle window were ever
// shortened below it, a purge would complete — writing a deletion record and releasing the
// token — while the broker was still serving the deleted tenant's data to whoever
// subscribed next.
//
// The margin today is large and this test is cheap; the point is that shortening the
// window becomes a failing test with an explanation rather than a silent regression.
func TestTheSettleDefaultOutlastsTheBrokersRetainedCache(t *testing.T) {
	settle := NewUserManagementConfiguration().TenantPurgeSettle()

	if settle <= messaging.RetainedCacheWindow {
		t.Fatalf("the settle window (%s) does not outlast the broker's retained-message cache "+
			"(%s), so a purge can complete while the broker is still delivering this tenant's "+
			"retained payloads to new subscribers", settle, messaging.RetainedCacheWindow)
	}
}
