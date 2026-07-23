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
)

// Lwm2mConfiguration is the top-level configuration for the adapter.
type Lwm2mConfiguration struct {
	// Listen is the UDP address the CoAP/DTLS server binds.
	Listen ListenConfiguration `json:"listen"`
	// Security is the DTLS posture and the PSK credential map.
	Security SecurityConfiguration `json:"security"`
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

// PskIdentity is one provisioned DTLS pre-shared-key credential.
type PskIdentity struct {
	// Identity is the DTLS PSK identity the client presents in its ClientHello. It is
	// sent in the clear on the wire (DTLS-PSK does not encrypt the identity), so an
	// OPAQUE, non-semantic handle is recommended over a "tenant:device" string, which
	// would leak fleet inventory to a passive observer (ADR-075 minor). At L0 it is the
	// sole identity axis; L1 maps it to (tenant, externalId). It must be globally
	// unique across the adapter — at PSK-callback time there is no tenant context, so a
	// duplicate identity across two tenants would be ambiguous (ADR-075 M8), which is
	// why Validate rejects a duplicate here.
	Identity string `json:"identity"`
	// PskEnv NAMES the environment variable holding the base64-encoded pre-shared key —
	// never the cleartext (a key written into the mounted config document would be a
	// plaintext-at-rest credential). The Helm chart projects a Kubernetes Secret into
	// this variable; the adapter reads and base64-decodes it once at startup. Required.
	PskEnv string `json:"pskEnv"`
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
	}
	return nil
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
		creds[id.Identity] = key
	}
	return creds, nil
}
