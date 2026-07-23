// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package config is the typed configuration for the LwM2M ingest service
// (ADR-075): a stateful adapter that terminates OMA LwM2M over CoAP/UDP+DTLS from
// constrained devices and folds their registration, telemetry, and commands onto
// the canonical device-management model.
//
// Tenancy is NOT connection-scoped the way the Sparkplug adapter's is. Sparkplug
// connects OUT to one per-tenant broker, so the connection names the tenant. LwM2M
// devices connect IN to one shared UDP socket, so "which connection" names nothing —
// and the registration endpoint name (`ep`) a device asserts in its own payload is
// UNTRUSTED. Therefore tenant + device identity are bound to the AUTHENTICATED DTLS
// credential (the PSK identity), never parsed from the registration payload (ADR-075
// decision D1). This file holds the PSK credential map that binding is built on; the
// tenant/externalId columns of that binding arrive with the registration interface
// (L1). At L0 the map proves only the transport auth floor: an unknown PSK identity
// is refused, fail-closed.
package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

const (
	// DefaultListenHost binds all interfaces (the service sits behind a Kubernetes
	// Service / NodePort that fronts the CoAPS port).
	DefaultListenHost = "0.0.0.0"
	// DefaultListenPort is the IANA CoAPS port — LwM2M over DTLS (RFC 7252 §12.7).
	DefaultListenPort = 5684
	// DefaultConnectionIdLength is the DTLS Connection ID length in bytes (RFC 9146).
	// CID lets a session survive a client's source-address change (a NAT rebind, or a
	// queue-mode cellular device waking on a new IP) without a fresh handshake, so it
	// is ON by default; a length of 0 disables it. 8 bytes is the pion example default
	// and ample to key the server's session table.
	DefaultConnectionIdLength = 8
	// MaxConnectionIdLength bounds the configured CID length. RFC 9146 allows up to
	// 255; a value beyond a couple of dozen bytes is pure overhead on every record of
	// a constrained radio, so the config refuses it rather than silently bloating.
	MaxConnectionIdLength = 20
	// DefaultHandshakeTimeoutSeconds bounds a single DTLS handshake so a stalled or
	// half-open handshake cannot pin a goroutine (the unauthenticated-datagram DoS
	// surface, ADR-075 §7).
	DefaultHandshakeTimeoutSeconds = 10
	// DefaultMaxSessions bounds the live DTLS session table — a memory-safety and
	// DoS ceiling (ADR-075 M6). A device population beyond this is a sizing decision an
	// operator makes explicitly; the adapter never grows the table without bound.
	DefaultMaxSessions = 100000
	// MinPskBytes is the shortest pre-shared key ResolveCredentials will accept. A PSK
	// is the device's sole authenticator (and, from L1, the tenancy anchor), so a
	// too-short key is refused at startup rather than silently weakening every session.
	// 16 bytes (128-bit) is the LwM2M-common baseline for the AES-128 cipher suite.
	MinPskBytes = 16
	// DefaultIngestMessagesPerSecond and DefaultIngestBurst are the platform per-tenant
	// ingest ceiling (ADR-023, ADR-075 L2c) applied when none is configured — a generous
	// safety ceiling, high enough not to shed a normally busy fleet, low enough that one
	// runaway device cannot saturate the pipeline. They MUST be positive: a zero ceiling
	// yields a token bucket that admits nothing (core.TenantRateLimiter), which would
	// black-hole every device's telemetry. The value matches event-sources so the two
	// device-ingest paths share one platform default; a genuinely high-volume tenant is
	// raised by a per-tenant override, never by making the default unlimited.
	DefaultIngestMessagesPerSecond = 1000
	DefaultIngestBurst             = 2000
)

// Lwm2mConfiguration is the top-level configuration for the adapter.
type Lwm2mConfiguration struct {
	// Listen is the UDP address the CoAP/DTLS server binds.
	Listen ListenConfiguration `json:"listen"`
	// Security is the DTLS posture and the PSK credential map.
	Security SecurityConfiguration `json:"security"`
	// IngestRateLimit is the platform-default, per-tenant ingest ceiling (ADR-023). It
	// gates the device-facing telemetry and registration paths (ADR-075 L2c); a per-tenant
	// override raises it for a legitimately high-volume tenant.
	IngestRateLimit IngestRateLimit `json:"ingestRateLimit"`
}

// IngestRateLimit is the platform-default, per-tenant ingest ceiling every device is metered
// against by an independent token bucket (ADR-023). It is fail-safe: an unset or non-positive
// value falls back to the platform default (see ApplyDefaults), never to unlimited, so a
// misconfiguration cannot silently remove the protection — and never to zero, which would be a
// bucket that admits nothing.
type IngestRateLimit struct {
	// MessagesPerSecond is the sustained per-tenant message rate (the pre-decode STAGE 1
	// ceiling). The per-tenant sample budget (STAGE 2) is derived from it.
	MessagesPerSecond float64 `json:"messagesPerSecond"`
	// Burst is the largest instantaneous batch a tenant may send before the sustained rate
	// applies — it absorbs a bursty fleet without raising the sustained ceiling.
	Burst int `json:"burst"`
}

// ListenConfiguration is the CoAPS bind address.
type ListenConfiguration struct {
	// Host is the interface to bind; empty defaults to DefaultListenHost.
	Host string `json:"host"`
	// Port is the UDP port; 0 defaults to DefaultListenPort (5684, CoAPS).
	Port int `json:"port"`
}

// SecurityConfiguration is the DTLS server posture and the authenticated-credential
// map every device is identified through.
type SecurityConfiguration struct {
	// ConnectionIdLength is the DTLS Connection ID length in bytes (RFC 9146). It is a
	// POINTER so that an explicit 0 (disable CID) is distinguishable from an omitted
	// value (default DefaultConnectionIdLength) — the same pattern the metrics port
	// uses elsewhere. When >0 the server issues a CID so a session survives a source
	// address change; when 0 it does not (a roaming device re-handshakes).
	ConnectionIdLength *int `json:"connectionIdLength"`
	// HandshakeTimeoutSeconds bounds a single DTLS handshake; 0 defaults to
	// DefaultHandshakeTimeoutSeconds.
	HandshakeTimeoutSeconds int `json:"handshakeTimeoutSeconds"`
	// IdleTimeoutSeconds reaps a DTLS session that has carried no traffic for this
	// long, bounding the memory a fleet of idle sessions holds. 0 disables reaping —
	// the correct value for a fleet of always-connected devices, but a queue-mode
	// deployment should set it above the expected wake interval so an idle sleeper's
	// keys are not evicted out from under it (which would re-introduce the
	// re-handshake-on-wake that CID exists to avoid, ADR-075 M6).
	IdleTimeoutSeconds int `json:"idleTimeoutSeconds"`
	// MaxSessions bounds the live DTLS session table; 0 defaults to DefaultMaxSessions.
	// A new handshake past the ceiling is refused (counted), never silently admitted.
	MaxSessions int `json:"maxSessions"`
	// Identities is the fail-closed PSK credential map: the set of DTLS PSK identities
	// the server will authenticate, each with the environment variable holding its
	// pre-shared key. An empty list is a valid, inert posture — the server binds but
	// authenticates no one (every handshake is refused) until credentials are
	// provisioned. A PSK identity that is not in this map is refused.
	Identities []PskIdentity `json:"identities"`
}

// PskIdentity is one provisioned DTLS pre-shared-key credential and the tenancy binding
// every registration on it resolves through (ADR-075 D1). The binding — tenant +
// external id — is fixed by which CREDENTIAL authenticated, never parsed from the
// device's own registration payload (the untrusted `ep`): a device connecting IN over
// the shared socket cannot assert its own tenant.
type PskIdentity struct {
	// Identity is the DTLS PSK identity the client presents in its ClientHello. It is
	// sent in the clear on the wire (DTLS-PSK does not encrypt the identity), so an
	// OPAQUE, non-semantic handle is recommended over a "tenant:device" string, which
	// would leak fleet inventory to a passive observer (ADR-075 minor). It must be
	// globally unique across the adapter — at PSK-callback time there is no tenant
	// context, so a duplicate identity across two tenants would be ambiguous (ADR-075
	// M8), which is why Validate rejects a duplicate here.
	Identity string `json:"identity"`
	// PskEnv NAMES the environment variable holding the base64-encoded pre-shared key —
	// never the cleartext (a key written into the mounted config document would be a
	// plaintext-at-rest credential). The Helm chart projects a Kubernetes Secret into
	// this variable; the adapter reads and base64-decodes it once at startup. Required.
	PskEnv string `json:"pskEnv"`
	// Tenant is the DeviceChain tenant this credential belongs to (ADR-075 D1). Every
	// registration authenticated by this identity is attributed here; the device never
	// names its own tenant. Required.
	Tenant string `json:"tenant"`
	// ExternalId is the device external id device-management keys on (ADR-049). It is
	// EXPLICIT and distinct from the opaque wire Identity: an operator picks a meaningful
	// id ("plant-a/sensor-42") while the Identity stays a rotatable opaque handle —
	// defaulting one to the other would couple the device's stable id to a credential
	// rotation. One credential = one external id: an LwM2M client endpoint holds exactly
	// one registration, and its objects/instances live under it. Required.
	ExternalId string `json:"externalId"`
	// DeviceTypeToken is the device type stamped on the device row when AutoRegister
	// creates it on first registration. Required when AutoRegister is set; ignored
	// otherwise.
	DeviceTypeToken string `json:"deviceTypeToken"`
	// AutoRegister creates the device row in device-management on first registration
	// (ALLOW_NEW): a provisioned credential is meant to become a device. When false the
	// device must already exist or its registration is dropped (counted). An unknown
	// device never reaches here — it has no PSK, so its handshake fails first.
	AutoRegister bool `json:"autoRegister"`
}

// PskBinding is the resolved tenancy binding for one authenticated PSK identity — what a
// registration handler needs after recovering the identity from the DTLS connection.
type PskBinding struct {
	Tenant          string
	ExternalId      string
	DeviceTypeToken string
	AutoRegister    bool
}

// NewLwm2mConfiguration builds a defaulted configuration.
func NewLwm2mConfiguration() *Lwm2mConfiguration {
	cfg := &Lwm2mConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills unset fields (ADR-022 decision 1). An unset ConnectionIdLength
// (nil) becomes CID-on at the default length; an explicit 0 is left alone (CID off).
// An empty Identities list is meaningful (bind, authenticate no one), not unset, so it
// is left alone.
func (c *Lwm2mConfiguration) ApplyDefaults() {
	if strings.TrimSpace(c.Listen.Host) == "" {
		c.Listen.Host = DefaultListenHost
	}
	if c.Listen.Port == 0 {
		c.Listen.Port = DefaultListenPort
	}
	if c.Security.ConnectionIdLength == nil {
		def := DefaultConnectionIdLength
		c.Security.ConnectionIdLength = &def
	}
	if c.Security.HandshakeTimeoutSeconds == 0 {
		c.Security.HandshakeTimeoutSeconds = DefaultHandshakeTimeoutSeconds
	}
	if c.Security.MaxSessions == 0 {
		c.Security.MaxSessions = DefaultMaxSessions
	}
	// Fail safe: a non-positive ceiling (unset, or an out-of-band bad value) floors to the
	// positive platform default. A zero here would hand the limiter a bucket that admits
	// nothing, silently blacking out every device's telemetry — worse than no gate at all.
	if c.IngestRateLimit.MessagesPerSecond <= 0 {
		c.IngestRateLimit.MessagesPerSecond = DefaultIngestMessagesPerSecond
	}
	if c.IngestRateLimit.Burst <= 0 {
		c.IngestRateLimit.Burst = DefaultIngestBurst
	}
}

// Validate fails the load closed on a configuration that would bind an out-of-range
// port, negotiate an unusable CID length, run an unbounded session table, or hold a
// malformed / duplicate PSK credential. Rejecting here keeps a misconfiguration from
// silently becoming a fail-open listener or an ambiguous cross-tenant identity.
func (c *Lwm2mConfiguration) Validate() error {
	if c.Listen.Port < 1 || c.Listen.Port > 65535 {
		return fmt.Errorf("listen.port %d is out of range (1-65535)", c.Listen.Port)
	}
	if c.Security.ConnectionIdLength != nil {
		cid := *c.Security.ConnectionIdLength
		if cid < 0 || cid > MaxConnectionIdLength {
			return fmt.Errorf("security.connectionIdLength %d is out of range (0-%d; 0 disables CID)", cid, MaxConnectionIdLength)
		}
	}
	if c.Security.HandshakeTimeoutSeconds < 1 {
		return fmt.Errorf("security.handshakeTimeoutSeconds %d must be >= 1", c.Security.HandshakeTimeoutSeconds)
	}
	if c.Security.IdleTimeoutSeconds < 0 {
		return fmt.Errorf("security.idleTimeoutSeconds %d must be >= 0 (0 disables idle reaping)", c.Security.IdleTimeoutSeconds)
	}
	if c.Security.MaxSessions < 1 {
		return fmt.Errorf("security.maxSessions %d must be >= 1", c.Security.MaxSessions)
	}
	seen := make(map[string]struct{}, len(c.Security.Identities))
	for i := range c.Security.Identities {
		id := &c.Security.Identities[i]
		if strings.TrimSpace(id.Identity) == "" {
			return fmt.Errorf("security.identities[%d]: identity is required", i)
		}
		if strings.TrimSpace(id.PskEnv) == "" {
			return fmt.Errorf("security.identities[%d]: pskEnv is required (the env var holding the base64 pre-shared key; never the cleartext key)", i)
		}
		if _, dup := seen[id.Identity]; dup {
			return fmt.Errorf("security.identities[%d]: duplicate PSK identity %q — an identity must be globally unique across the adapter (there is no tenant context at DTLS-handshake time to disambiguate)", i, id.Identity)
		}
		seen[id.Identity] = struct{}{}
		if strings.TrimSpace(id.Tenant) == "" {
			return fmt.Errorf("security.identities[%d]: tenant is required (ADR-075 D1 — tenancy is bound to the credential, never the device's untrusted registration payload)", i)
		}
		if strings.TrimSpace(id.ExternalId) == "" {
			return fmt.Errorf("security.identities[%d]: externalId is required (the device external id device-management keys on; explicit, distinct from the opaque wire identity)", i)
		}
		if id.AutoRegister && strings.TrimSpace(id.DeviceTypeToken) == "" {
			return fmt.Errorf("security.identities[%d]: deviceTypeToken is required when autoRegister is set (it is stamped on the auto-created device row)", i)
		}
		// NOTE: a duplicate (tenant, externalId) across two identities is DELIBERATELY
		// allowed — it is the credential-rotation overlap (two live PSKs for one device
		// while a new key is being provisioned). The presence epoch guard (ADR-067)
		// converges the common case: whichever credential registers later mints the higher
		// SessionId and supersedes. (A pathological interleaving — the newer credential
		// deregistering while the older is kept alive by Updates, which emit nothing — can
		// still show DISCONNECTED until the older re-Registers; that needs both credentials
		// concurrently active and resolves on the next register.) Do NOT add a uniqueness
		// check here.
	}
	return nil
}

// Bindings returns the identity -> tenancy binding map a registration handler resolves
// through after recovering the authenticated PSK identity from the DTLS connection
// (ADR-075 D1). It is built from the same identities the credential map authenticates,
// so an authenticated identity always has a binding. Call after Validate.
func (c *Lwm2mConfiguration) Bindings() map[string]PskBinding {
	bindings := make(map[string]PskBinding, len(c.Security.Identities))
	for _, id := range c.Security.Identities {
		bindings[id.Identity] = PskBinding{
			Tenant:          id.Tenant,
			ExternalId:      id.ExternalId,
			DeviceTypeToken: id.DeviceTypeToken,
			AutoRegister:    id.AutoRegister,
		}
	}
	return bindings
}

// ConnectionIdLengthOrDefault returns the configured CID length, or the default when
// unset. A returned 0 means CID is explicitly disabled.
func (c *Lwm2mConfiguration) ConnectionIdLengthOrDefault() int {
	if c.Security.ConnectionIdLength == nil {
		return DefaultConnectionIdLength
	}
	return *c.Security.ConnectionIdLength
}

// ResolveCredentials reads each identity's pre-shared key from its named environment
// variable, base64-decodes it, and returns the identity->key map the DTLS server
// authenticates against. It fails closed: a named variable that is empty, not valid
// base64, or decodes to an empty key is an error — a credential that was meant to be
// projected but was not must crash startup, never degrade into a listener that would
// authenticate that identity with an empty key. An empty Identities list resolves to
// an empty (non-nil) map: a valid inert posture that authenticates no one.
func (c *Lwm2mConfiguration) ResolveCredentials() (map[string][]byte, error) {
	creds := make(map[string][]byte, len(c.Security.Identities))
	for i := range c.Security.Identities {
		id := c.Security.Identities[i]
		raw := os.Getenv(id.PskEnv)
		if raw == "" {
			return nil, fmt.Errorf("security.identities[%d]: pskEnv %q is set but that environment variable is empty (the PSK Secret was not projected); refusing to start", i, id.PskEnv)
		}
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("security.identities[%d]: pskEnv %q is not valid base64: %w", i, id.PskEnv, err)
		}
		// A whitespace-only variable passes the emptiness check above but decodes to a
		// zero-length key; the minimum-length guard catches that AND a too-short key,
		// failing closed at startup rather than provisioning an unauthenticatable or
		// weak credential.
		if len(key) < MinPskBytes {
			return nil, fmt.Errorf("security.identities[%d]: pskEnv %q decoded to a %d-byte key; a pre-shared key must be at least %d bytes", i, id.PskEnv, len(key), MinPskBytes)
		}
		creds[id.Identity] = key
	}
	return creds, nil
}
