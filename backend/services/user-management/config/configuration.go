// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"golang.org/x/crypto/bcrypt"
)

// AuthConfiguration controls JWT issuance, signing-key rotation, and the
// one-time bootstrap admin.
type AuthConfiguration struct {
	// IssuerUrl is the external https origin of this Authorization Server (ADR-047).
	// When set it becomes the "iss" claim of *every* minted token (a decisive
	// platform-wide cutover from the legacy internal identifier — the validator
	// selects keys by "kid", never pins "iss", so cross-service verification is
	// unaffected) AND turns on the OAuth 2.1 AS surface: the token's issuer must
	// equal the base URL where the RFC 8414 metadata is served. Empty (the default)
	// keeps the legacy derived issuer and leaves the entire OAuth surface OFF,
	// fail-closed — mirroring how an empty service-auth secret disables service-token
	// minting. Must be an absolute http/https URL with no query or fragment (http is
	// tolerated only for a localhost issuer, for local development).
	IssuerUrl string

	// Token lifetimes in seconds (0 falls back to the auth package defaults).
	AccessTokenTtlSeconds  int
	RefreshTokenTtlSeconds int

	// SigningKeyMaxAgeDays triggers an age-based signing-key rotation at startup
	// when the active key is older than this. 0 disables age-based rotation
	// (rotation can still be invoked explicitly). SigningKeyRetentionDays is how
	// long a rotated-out key is kept (its public half stays in the JWKS) so
	// tokens it signed keep verifying; it must exceed the refresh-token lifetime.
	SigningKeyMaxAgeDays    int
	SigningKeyRetentionDays int

	// Superuser seeded on first startup when no identity exists (ADR-033): a global
	// email identity holding the `superuser` system role (authority `*`). The
	// default password MUST be changed after first login; startup logs a warning.
	// dcctl bootstrap supplies a generated password.
	SuperuserEmail    string
	SuperuserPassword string

	// SeedClients registers OAuth 2.1 clients at startup (idempotent upsert), so a
	// deployment can provision a confidential client — e.g. Grafana SSO (ADR-047) —
	// without a manual admin call. The secret is delivered PRE-HASHED (bcrypt); the
	// cleartext lives only in the consuming client's own config (the mint-once
	// pattern the NATS service password uses). Config is the source of truth: a
	// redeploy re-syncs each client's redirect URIs / scopes / secret hash. Empty by
	// default (no seeded clients).
	SeedClients []SeedOAuthClientConfig
}

// SeedOAuthClientConfig is a single OAuth client provisioned at startup (ADR-047).
// ClientId is its stable identifier; RedirectURIs the exact-match allowlist; Scopes
// the scopes it may request; SecretHash the bcrypt hash of its secret for a
// confidential client (empty ⇒ a public PKCE client). The cleartext secret is never
// carried here — only its hash — so a config dump never yields a usable credential.
type SeedOAuthClientConfig struct {
	ClientId     string
	RedirectURIs []string
	Scopes       []string
	SecretHash   string
}

// TenantPurgeConfiguration tunes the ADR-077 purge coordinator: the loop that erases a
// deleted tenant's data across every store and then releases its token.
//
// All three fields take 0 to mean the default. 🔴 ONLY IntervalSeconds TAKES A NEGATIVE
// TO MEAN "DISABLED" — the other two reject one at load, and the asymmetry is the design
// rather than an oversight. Disabling the loop is a real operational lever (a maintenance
// window where nothing should be deleting rows) and it is safe by construction: the
// tenant row is the work list, so a disabled coordinator leaves every purge pending
// rather than losing it, and no token is released while it is off. Disabling either
// WINDOW would instead complete purges that were never observed clean, or release a
// token while a pre-deletion session can still write under it. See Validate.
type TenantPurgeConfiguration struct {
	// IntervalSeconds is how often a pass runs. A pass is cheap when nothing is
	// purging — one indexed query returning no rows.
	IntervalSeconds int

	// SettleSeconds is how long EVERY store must keep reporting clean before the purge
	// completes and the token is released. It is not padding; the derivation of the
	// default is on defaultTenantPurgeSettleSeconds below.
	SettleSeconds int

	// TokenHoldSeconds is the minimum age of the purge itself — measured from the cut,
	// not from when the stores went quiet — before the token may be released.
	//
	// It answers a different question from SettleSeconds and neither substitutes for the
	// other. Settling asks "has everything already written stopped arriving?". This asks
	// "can anything still be admitted at all?", and the answer outlives the sweep by a
	// long way: see defaultTenantPurgeTokenHoldSeconds.
	TokenHoldSeconds int
}

type UserManagementConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration

	// TsdbConfiguration sizes the purge's guest connection to the TELEMETRY cluster
	// (ADR-077). This service stores nothing there and owns no schema in it; the
	// connection exists only so a deleted tenant's events can be erased from the one
	// database no service running the coordinator owns. It is separate from
	// RdbConfiguration because the two pools serve different work and should not have
	// to be sized together.
	TsdbConfiguration config.MicroserviceDatastoreConfiguration

	Auth        AuthConfiguration
	TenantPurge TenantPurgeConfiguration
}

// TenantPurgeInterval is the coordinator's tick period, or 0 when the coordinator is
// disabled.
func (c *UserManagementConfiguration) TenantPurgeInterval() time.Duration {
	if c.TenantPurge.IntervalSeconds < 0 {
		return 0
	}
	return time.Duration(c.TenantPurge.IntervalSeconds) * time.Second
}

// TenantPurgeSettle is how long every store must stay clean before completion.
func (c *UserManagementConfiguration) TenantPurgeSettle() time.Duration {
	return time.Duration(c.TenantPurge.SettleSeconds) * time.Second
}

// TenantPurgeTokenHold is the minimum age of a purge before its token may be released.
func (c *UserManagementConfiguration) TenantPurgeTokenHold() time.Duration {
	return time.Duration(c.TenantPurge.TokenHoldSeconds) * time.Second
}

// Creates the default user management configuration
func NewUserManagementConfiguration() *UserManagementConfiguration {
	cfg := &UserManagementConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills unset fields with their defaults so configuration loaded
// from a document that omits them is still well-formed (ADR-022 decision 1). It
// runs on both the constructor and the load path so there is one source of
// defaults. SigningKeyMaxAgeDays is intentionally left at 0 (age-based rotation
// off); SqlDebug is intentionally left at its zero value (SQL query logging off).
func (c *UserManagementConfiguration) ApplyDefaults() {
	if c.Auth.AccessTokenTtlSeconds == 0 {
		c.Auth.AccessTokenTtlSeconds = 900 // 15 minutes
	}
	if c.Auth.RefreshTokenTtlSeconds == 0 {
		c.Auth.RefreshTokenTtlSeconds = 604800 // 7 days
	}
	if c.Auth.SigningKeyRetentionDays == 0 {
		c.Auth.SigningKeyRetentionDays = 8 // > refresh-token lifetime (7 days)
	}
	if c.Auth.SuperuserEmail == "" {
		c.Auth.SuperuserEmail = "superuser@devicechain.local"
	}
	if c.Auth.SuperuserPassword == "" {
		c.Auth.SuperuserPassword = "devicechain"
	}
	if c.TenantPurge.IntervalSeconds == 0 {
		c.TenantPurge.IntervalSeconds = defaultTenantPurgeIntervalSeconds
	}
	if c.TenantPurge.SettleSeconds == 0 {
		c.TenantPurge.SettleSeconds = defaultTenantPurgeSettleSeconds
	}
	if c.TenantPurge.TokenHoldSeconds == 0 {
		c.TenantPurge.TokenHoldSeconds = defaultTenantPurgeTokenHoldSeconds
	}
}

const (
	// defaultTenantPurgeIntervalSeconds ticks the coordinator once a minute. A pass over
	// nothing is one indexed query returning no rows, and a purge that has work to do
	// wants to be prompt: an operator who just deleted a tenant is watching.
	defaultTenantPurgeIntervalSeconds = 60

	// defaultTenantPurgeSettleSeconds is five minutes: long enough for a write that was
	// ALREADY ADMITTED when the fence closed to finish reaching storage, which is what a
	// residual scan run straight after a sweep cannot see. The ingest fence's
	// tenant-lifecycle cache TTL (governance.defaultCacheTTL, 60 seconds) bounds how long
	// after the cut a straggler can still be let in; this leaves five times that for it
	// to land. The asymmetry is the point: waiting too long reserves a token for a few
	// extra minutes, not waiting long enough writes an erasure record that is false.
	//
	// 🔴 IT ALSO HAS A FLOOR IT MUST NOT DROP BELOW: messaging.RetainedCacheWindow. A
	// stream purge does not stop retained delivery immediately — nats-server answers a new
	// subscriber from an in-memory cache before it reads JetStream, for up to two minutes,
	// with no configuration knob. A purge that completed inside that window would write a
	// record saying the tenant's data is gone while the broker was still handing its
	// retained payloads to new subscribers. The default clears it by a wide margin, and
	// TestTheSettleDefaultOutlastsTheBrokersRetainedCache is what keeps that true rather
	// than coincidental.
	//
	// 🔴 IT IS NOT THE BOUND ON WHEN THE TOKEN MAY BE RELEASED, and reading it that way
	// is the misreading governance.TenantLifecycleResolver's own doc warns about: the 60s
	// TTL bounds how long the gate keeps SAYING "active", not how long the device plane
	// keeps moving. TokenHoldSeconds is that bound.
	defaultTenantPurgeSettleSeconds = 300
)

// defaultTenantPurgeTokenHoldSeconds is how long a purged token stays reserved, and it is
// tied to the broker credential's own lifetime rather than chosen.
//
// 🔴 THE FENCE DIES WITH THE TENANT ROW. Every ADR-077 device-plane gate resolves the
// tenant's lifecycle through user-management, and an UNKNOWN tenant reads as not deleted —
// the fail-open posture is deliberate and documented, because failing closed would make
// user-management a hard dependency of device connectivity. So the instant completion
// removes the row, all of those gates start admitting the released token again. Nothing
// downstream is epoch-aware; the data plane knows only tokens.
//
// What can still present itself at that point is a session established BEFORE the cut. Its
// credential rows were swept, so it cannot re-authenticate — but the broker arms its
// force-close on the JWT's own expiry, so an already-connected device keeps its live
// connection for up to one credential TTL. Release the token before that elapses and a
// straggler's writes land under it, to be inherited by the successor: the exact defect
// ADR-077 exists to close, re-entering through its own completion step.
//
// Hence the value, not a round number: natsauth.DefaultUserJWTTTL, read from the constant
// so the two cannot drift apart silently. Lowering it trades erasure certainty for a token
// that can be reused sooner, and that is a real operational choice — a deleted tenant's
// name is unavailable for this long — but it is a choice about correctness, not tidiness.
var defaultTenantPurgeTokenHoldSeconds = int(natsauth.DefaultUserJWTTTL / time.Second)

// Validate enforces semantic constraints after decoding and defaulting, failing
// the load closed on an invalid configuration (ADR-022 decision 1). It is
// defense in depth: the bootstrap admin must be fully specified, and token TTLs
// must be positive so a key-value store is never created with a zero TTL.
func (c *UserManagementConfiguration) Validate() error {
	if c.Auth.SuperuserEmail == "" {
		return fmt.Errorf("auth.superuserEmail must not be empty")
	}
	if c.Auth.SuperuserPassword == "" {
		return fmt.Errorf("auth.superuserPassword must not be empty")
	}
	if c.Auth.AccessTokenTtlSeconds <= 0 {
		return fmt.Errorf("auth.accessTokenTtlSeconds must be positive (got %d)", c.Auth.AccessTokenTtlSeconds)
	}
	if c.Auth.RefreshTokenTtlSeconds <= 0 {
		return fmt.Errorf("auth.refreshTokenTtlSeconds must be positive (got %d)", c.Auth.RefreshTokenTtlSeconds)
	}
	// A negative interval disables the coordinator, which is a supported lever. A
	// negative SETTLE is not the same kind of knob: it would mean "complete the purge
	// without ever having observed the stores clean", i.e. release the token and record
	// an erasure on the strength of a delete returning no error. There is deliberately
	// no configuration that buys that.
	if c.TenantPurge.TokenHoldSeconds < 0 {
		return fmt.Errorf("tenantPurge.tokenHoldSeconds must not be negative (got %d): it is what "+
			"keeps a token reserved until no pre-deletion device session can still write under it",
			c.TenantPurge.TokenHoldSeconds)
	}
	if c.TenantPurge.SettleSeconds < 0 {
		return fmt.Errorf("tenantPurge.settleSeconds must not be negative (got %d): the window is what "+
			"turns one clean residual scan into a sustained one, and a purge cannot complete without it",
			c.TenantPurge.SettleSeconds)
	}
	// 🔴 THE FLOOR IS A CORRECTNESS BOUND, NOT A PREFERENCE, and it is checked here rather
	// than left to the default because a default only protects an operator who never
	// touches the knob. A stream purge does not stop retained delivery immediately:
	// nats-server answers a new subscriber from an in-memory cache before it reads
	// JetStream, for messaging.RetainedCacheWindow, with no configuration knob of its own.
	// Complete a purge inside that window and the deletion record says the tenant's data
	// is gone while the broker is still handing its retained payloads to whoever
	// subscribes next.
	//
	// 🔴 THE FLOOR IS THE CACHE WINDOW PLUS PurgeTimeout, AND THE SECOND TERM IS NOT PADDING.
	// The settle window is measured from a store's clean-since, and the coordinator stamps
	// that BEFORE calling the store (Coordinator.eraseOne) — so the purge that empties the
	// retained stream, and therefore the last moment its payloads can be stamped into the
	// broker's cache, can land up to a whole broker PurgeTimeout after the timestamp being
	// measured from. A floor of the cache window alone leaves that gap uncovered: a
	// configured 121s would pass validation and still allow completion while a retained
	// payload is servable.
	//
	// Strictly greater, not merely equal, for the same reason at the other end: the last
	// stamp can be the instant of the purge, so equality puts completion exactly on the
	// boundary.
	floor := messaging.RetainedCacheWindow + messaging.PurgeTimeout
	if settle := c.TenantPurgeSettle(); settle > 0 && settle <= floor {
		return fmt.Errorf("tenantPurge.settleSeconds is %s, which does not clear the broker's "+
			"retained-message cache (%s) plus the time one broker purge may take (%s): a purge could "+
			"complete while the broker is still delivering this tenant's retained payloads to new "+
			"subscribers, so the deletion record would be false",
			settle, messaging.RetainedCacheWindow, messaging.PurgeTimeout)
	}
	if c.Auth.IssuerUrl != "" {
		if err := validateIssuerUrl(c.Auth.IssuerUrl); err != nil {
			return fmt.Errorf("auth.issuerUrl: %w", err)
		}
	}
	seen := make(map[string]struct{}, len(c.Auth.SeedClients))
	for i, sc := range c.Auth.SeedClients {
		if err := validateSeedClient(sc); err != nil {
			return fmt.Errorf("auth.seedClients[%d]: %w", i, err)
		}
		if _, dup := seen[sc.ClientId]; dup {
			return fmt.Errorf("auth.seedClients: duplicate clientId %q", sc.ClientId)
		}
		seen[sc.ClientId] = struct{}{}
	}
	return nil
}

// validateSeedClient enforces the same shape the admin API requires of a runtime
// client (a valid clientId, at least one OAuth-2.1-legal redirect URI, and at least
// one scope this AS grants), failing startup closed on a malformed seed entry.
func validateSeedClient(sc SeedOAuthClientConfig) error {
	if err := validateClientId(sc.ClientId); err != nil {
		return err
	}
	if len(sc.RedirectURIs) == 0 {
		return fmt.Errorf("at least one redirectUri is required")
	}
	for _, u := range sc.RedirectURIs {
		if err := auth.ValidateRedirectURI(u); err != nil {
			return fmt.Errorf("redirectUri: %w", err)
		}
	}
	if len(sc.Scopes) == 0 {
		return fmt.Errorf("at least one scope is required")
	}
	for _, s := range sc.Scopes {
		if !auth.IsSupportedScope(s) {
			return fmt.Errorf("unknown scope %q (supported: %s)", s, strings.Join(auth.SupportedScopes, ", "))
		}
	}
	// A confidential client's secret is delivered PRE-HASHED. Require it to actually
	// be a bcrypt hash so a boot-time config error catches an operator who pasted the
	// cleartext (which would otherwise be stored as the "hash", fail every token
	// exchange with a confusing remote invalid_client, and leave cleartext in config)
	// or an over-length string (which would fail later at the varchar(100) column).
	// Empty is allowed — a public PKCE client.
	if sc.SecretHash != "" {
		if _, err := bcrypt.Cost([]byte(sc.SecretHash)); err != nil {
			return fmt.Errorf("secretHash must be a bcrypt hash (the pre-hashed client secret), not cleartext: %w", err)
		}
	}
	return nil
}

// validateClientId enforces a bounded, URL/config-safe clientId (letters, digits,
// '-', '_', '.'; ≤128 chars) — the same rule the admin API applies, kept in sync
// here so a seeded client cannot carry an id the admin API would reject.
func validateClientId(id string) error {
	if id == "" {
		return fmt.Errorf("clientId is required")
	}
	if len(id) > 128 {
		return fmt.Errorf("clientId must be at most 128 characters")
	}
	for _, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.'
		if !ok {
			return fmt.Errorf("clientId may contain only letters, digits, '-', '_', '.' (got %q)", id)
		}
	}
	return nil
}

// OAuthEnabled reports whether the OAuth 2.1 Authorization-Server surface is
// turned on — it is exactly when an issuer URL is configured (fail-closed: no
// issuer, no OAuth). The metadata/authorize/token handlers register only when
// this is true, and the same URL is stamped as every token's "iss".
func (c *UserManagementConfiguration) OAuthEnabled() bool {
	return c.Auth.IssuerUrl != ""
}

// validateIssuerUrl enforces the RFC 8414 issuer-identifier shape: an absolute
// URL, https (http tolerated only for a localhost issuer during local dev), with
// no query or fragment. A trailing slash is rejected so the stored issuer and the
// "iss" claim compare byte-for-byte against what clients derive from discovery.
func validateIssuerUrl(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a valid URL: %w", err)
	}
	if !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("must be an absolute URL with a host (got %q)", raw)
	}
	// The raw string is what gets stamped as "iss" and concatenated into endpoint
	// URLs, so reject anything the parsed-field checks below would miss on a
	// technicality: a bare "?"/"#" (url.Parse records these as ForceQuery / empty
	// fragment, slipping past the RawQuery/Fragment checks) and any query/fragment
	// delimiter at all. An issuer with either can never compare byte-for-byte
	// against what a client derives from discovery.
	if u.ForceQuery || strings.ContainsAny(raw, "?#") {
		return fmt.Errorf("must have no query or fragment (got %q)", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("must have no query or fragment (got %q)", raw)
	}
	if u.User != nil {
		return fmt.Errorf("must not contain userinfo/credentials (got %q)", raw)
	}
	if strings.HasSuffix(u.Path, "/") {
		return fmt.Errorf("must not end with a trailing slash (got %q)", raw)
	}
	// The scheme and host must already be lowercase in the RAW string (it is what
	// is stored/emitted as "iss"), so an uppercase "HTTPS://Host" fails a
	// normalizing client's issuer match. url.Parse lowercases u.Scheme (so check the
	// raw prefix) but preserves u.Host's case.
	if i := strings.Index(raw, "://"); i >= 0 {
		if s := raw[:i]; s != strings.ToLower(s) {
			return fmt.Errorf("scheme must be lowercase (got %q)", raw)
		}
	}
	if host := u.Host; host != strings.ToLower(host) {
		return fmt.Errorf("host must be lowercase (got %q)", raw)
	}
	host := u.Hostname()
	isLocalhost := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && isLocalhost) {
		return fmt.Errorf("must use https (http allowed only for a localhost issuer; got scheme %q host %q)", u.Scheme, host)
	}
	return nil
}
