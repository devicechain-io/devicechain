// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package config holds the dc-edge-agent's typed, fail-closed configuration
// (ADR-022 posture, reused via core.LoadConfiguration): unknown/invalid keys are
// rejected at startup, defaults are authoritative, and Validate moves errors from
// pod runtime to load time. The agent is NOT an Instance — this config carries only
// what a store-and-forward edge box needs (a local device listener + a cloud
// uplink), never a tenant registry, database, or NATS-cluster coordinates.
package config

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// instanceIdPattern mirrors the platform's instance-id grammar (values.schema.json):
// the id is the first segment of every device-plane MQTT topic and messaging
// subject, so it is constrained to the token-safe alphabet and cannot inject a
// subject/topic metacharacter. The edge agent must forward on the SAME instance
// namespace as the cloud it feeds, so it enforces the same grammar.
var instanceIdPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Configuration is the whole dc-edge-agent config document.
type Configuration struct {
	// InstanceId is the cloud Instance this agent forwards to. It is the first
	// segment of the golden-MQTT topic on BOTH planes (device→agent and
	// agent→cloud), so it must match the target Instance exactly or every forward
	// silently addresses a namespace nothing consumes.
	InstanceId string `json:"instanceId"`

	// Local configures the device-facing MQTT listener the agent terminates.
	Local LocalConfiguration `json:"local"`

	// Uplink configures the cloud-facing MQTT connection the agent forwards to.
	Uplink UplinkConfiguration `json:"uplink"`
}

// LocalConfiguration is the device-facing side: an embedded nats-server MQTT
// gateway that terminates the golden path byte-identically (ADR-006), backed by a
// JetStream file store under StoreDir.
type LocalConfiguration struct {
	// ListenHost is the bind address for the local device MQTT listener. Defaults
	// to 0.0.0.0 (devices on the site LAN connect to it). NOTE (E1): the listener
	// carries no local device auth yet — it is trusted-LAN-only; see the module
	// README. Cloud event attribution rides on the per-event payload credential
	// (ADR-014), not this connection.
	ListenHost string `json:"listenHost"`

	// ListenPort is the local device MQTT port. Defaults to 1883.
	ListenPort int `json:"listenPort"`

	// Username / PasswordEnv are the OPTIONAL shared-secret credential the local MQTT
	// listener requires (E4). Empty (the default) leaves the listener OPEN — a
	// trusted-LAN posture the agent announces with a loud startup WARN so "open" is
	// always a visible operational choice, never a silent default. When set, the
	// embedded MQTT gateway rejects any CONNECT that does not present this pair (it
	// gates ONLY the device MQTT surface; the agent's own in-process drain client is
	// unaffected). This is a single shared secret, NOT per-device identity — cloud
	// event ATTRIBUTION still rides the per-event payload credential (ADR-014), so the
	// gate is a network-access control, not an identity system. PasswordEnv names an
	// environment variable holding the password (a projected Secret, never cleartext in
	// this document), mirroring the uplink pattern. NOTE: over plaintext MQTT the secret
	// crosses the LAN in the clear — the network is still the real boundary; local MQTT
	// TLS is future work.
	Username    string `json:"username"`
	PasswordEnv string `json:"passwordEnv"`

	// StoreDir is the on-disk directory for the embedded JetStream file store (the
	// durable local spool). Required: an in-memory spool would lose everything on
	// an agent restart during an outage (spec decision 3).
	StoreDir string `json:"storeDir"`

	// SpoolMaxBytes bounds the durable capture spool's on-disk size (E3). A
	// multi-day WAN outage must not grow the store until the disk fills — beyond
	// this budget the spool behaves as a ring buffer (drop OLDEST un-forwarded
	// event to admit the newest; see the agent's DiscardOld rationale), and every
	// drop is counted and surfaced. Defaults to 1 GiB; must be >= SpoolMinBytes so
	// a JetStream file stream can actually function rather than thrash.
	SpoolMaxBytes int64 `json:"spoolMaxBytes"`

	// MetricsPort is the loopback (127.0.0.1) port for the Prometheus /metrics and
	// /healthz endpoints (E3). Bound to loopback ONLY — the MQTT gateway stays the
	// sole LAN-exposed surface (F5). A pointer so an omitted key (nil) takes the
	// default while an explicit 0 disables the endpoint entirely; any other value is
	// the port. Defaults to 9090.
	MetricsPort *int `json:"metricsPort"`

	// Priority classes the events the spool sheds by when it nears its budget. See
	// PriorityConfiguration. Omitted, every event is "standard" and the spool is the plain
	// ring buffer it always was.
	Priority PriorityConfiguration `json:"priority"`
}

// Priority classes. A spool that must lose something loses the lowest class first, and the
// safety class is never shed by the agent.
const (
	ClassSafety   = "safety"
	ClassStandard = "standard"
	ClassBulk     = "bulk"
)

// PriorityRule assigns the events of the local devices whose token matches Match (a path.Match
// glob, e.g. "estop-*") to Class. The first matching rule wins; a device matching none is
// ClassStandard.
type PriorityRule struct {
	Match string `json:"match"`
	Class string `json:"class"`
}

// PriorityConfiguration is the shedding policy. With rules set, the agent keeps the spool at or
// below ShedAtFraction of spoolMaxBytes by deleting buffered events lowest class first (oldest
// first within a class) down to ShedToFraction. The headroom above ShedAtFraction is what keeps
// the spool's ring buffer (which cannot tell classes apart and evicts the oldest event of any
// class) from ever being reached while shedding can keep up. Every shed event is counted by class.
type PriorityConfiguration struct {
	Rules          []PriorityRule `json:"rules"`
	ShedAtFraction float64        `json:"shedAtFraction"`
	ShedToFraction float64        `json:"shedToFraction"`
}

// Retire modes for Uplink.RetireOn.
const (
	// RetireOnPuback retires a spool entry when the broker PUBACKs it (the behaviour before
	// the accept signal existed). It is the default, so an agent upgraded ahead of its platform
	// keeps working.
	RetireOnPuback = "puback"
	// RetireOnAccept retires a spool entry only when the platform says it durably accepted the
	// event. A PUBACK says the broker took the message; the platform may still shed it at the
	// tenant's ingest ceiling or fail to store it. Requires a platform that sends acceptances.
	RetireOnAccept = "accept"
)

// SpoolMinBytes is the floor for Local.SpoolMaxBytes: below a working minimum a
// JetStream file stream cannot hold a useful backlog and would thrash its blocks.
const SpoolMinBytes int64 = 16 * 1024 * 1024 // 16 MiB

// DefaultSpoolMaxBytes is the authoritative default spool budget (1 GiB) for an
// edge box sized to ride a multi-hour-to-day outage of modest telemetry volume.
const DefaultSpoolMaxBytes int64 = 1024 * 1024 * 1024 // 1 GiB

// MaxSpoolBytes is the ceiling for Local.SpoolMaxBytes (1 TiB): far above any real edge
// box, but bounded so the agent's JetStreamMaxStore pin (spoolMaxBytes + headroom) cannot
// overflow int64 into a negative value that would silently reopen the disk-derived
// admission crash-loop the pin exists to close.
const MaxSpoolBytes int64 = 1024 * 1024 * 1024 * 1024 // 1 TiB

// DefaultMetricsPort is the default loopback Prometheus/health port.
const DefaultMetricsPort = 9090

// UplinkConfiguration is the cloud-facing side: a paho MQTT client publishing the
// golden path to the cloud broker, authenticated per ADR-025. The credential is
// referenced (never inline): the edge box has no ADR-059 secret store, so the
// working precedent is the sparkplug env/projected-Secret pattern.
type UplinkConfiguration struct {
	// BrokerURL is the cloud MQTT broker, scheme-selected for transport: tcp://
	// (plaintext) or ssl:// / tls:// (TLS with system roots). Required.
	BrokerURL string `json:"brokerUrl"`

	// Tenant is the tenant the gateway device belongs to. It is a segment of every
	// uplink topic, the prefix of Username, and the only tenant the agent captures
	// from its local devices: a local publish naming another tenant is counted
	// (tenant_mismatched_total) and never spooled. Required.
	Tenant string `json:"tenant"`

	// GatewayToken is the token of the gateway DEVICE this agent authenticates as.
	// The agent connects as that device (client id = the device's plain client id)
	// and publishes every local device's events in the gateway's own namespace,
	// {instance}/{tenant}/gateways/{gatewayToken}/devices/{child}/events; the
	// platform accepts an event only for a device assigned to this gateway.
	// Required.
	GatewayToken string `json:"gatewayToken"`

	// Username is the gateway device's MQTT login: "{tenant}:{credentialId}", the
	// device credential the broker's auth callout verifies. Required, and refused at
	// load when it does not carry the configured tenant prefix: an unprefixed name
	// would otherwise fail only at CONNECT, as an opaque not-authorized.
	Username string `json:"username"`

	// PasswordEnv names the environment variable holding the gateway credential's
	// secret (a projected Secret, never cleartext in this document). Required.
	PasswordEnv string `json:"passwordEnv"`

	// ConnectTimeoutSeconds bounds a single connect attempt. Defaults to 30.
	ConnectTimeoutSeconds int `json:"connectTimeoutSeconds"`

	// BackoffMinSeconds / BackoffMaxSeconds bound the reconnect backoff so a
	// flapping WAN is not hammered. Default 1 / 60.
	BackoffMinSeconds int `json:"backoffMinSeconds"`
	BackoffMaxSeconds int `json:"backoffMaxSeconds"`

	// RetireOn selects what retires a spool entry: "puback" (default) or "accept" (the
	// platform's durable-accept signal). Use "accept" only against a platform that sends
	// acceptances: against one that does not, nothing is ever retired and every event is
	// resent each acceptTimeoutSeconds (the platform suppresses the duplicates, but the spool
	// never drains).
	RetireOn string `json:"retireOn"`

	// AcceptTimeoutSeconds is how long an accept-mode agent waits for the platform's
	// acceptance before offering the event again. It must exceed the round trip plus the
	// platform's ingest latency; a satellite link wants minutes. Defaults to 120.
	AcceptTimeoutSeconds int `json:"acceptTimeoutSeconds"`
}

// ApplyDefaults fills zero-valued fields with authoritative defaults (runs before
// Validate, regardless of which keys the document supplied).
func (c *Configuration) ApplyDefaults() {
	if c.Local.ListenHost == "" {
		c.Local.ListenHost = "0.0.0.0"
	}
	if c.Local.ListenPort == 0 {
		c.Local.ListenPort = 1883
	}
	if c.Local.SpoolMaxBytes == 0 {
		c.Local.SpoolMaxBytes = DefaultSpoolMaxBytes
	}
	if c.Local.MetricsPort == nil {
		// Omitted (nil) takes the default port; an explicit 0 is preserved and means
		// "disabled" (handled by the agent, which skips binding the endpoint).
		def := DefaultMetricsPort
		c.Local.MetricsPort = &def
	}
	if c.Uplink.ConnectTimeoutSeconds == 0 {
		c.Uplink.ConnectTimeoutSeconds = 30
	}
	if c.Uplink.BackoffMinSeconds == 0 {
		c.Uplink.BackoffMinSeconds = 1
	}
	if c.Uplink.BackoffMaxSeconds == 0 {
		c.Uplink.BackoffMaxSeconds = 60
	}
	if c.Uplink.RetireOn == "" {
		c.Uplink.RetireOn = RetireOnPuback
	}
	if c.Uplink.AcceptTimeoutSeconds == 0 {
		c.Uplink.AcceptTimeoutSeconds = 120
	}
	if len(c.Local.Priority.Rules) > 0 {
		if c.Local.Priority.ShedAtFraction == 0 {
			c.Local.Priority.ShedAtFraction = 0.9
		}
		if c.Local.Priority.ShedToFraction == 0 {
			c.Local.Priority.ShedToFraction = 0.8
		}
	}
}

// Validate enforces the semantic constraints, failing the load closed.
// RenamedConfigKeys names the keys an earlier release accepted whose role another key now
// plays, so a config left over from that release is refused with the replacement in the error
// rather than as a bare unknown field. agentId made the old direct uplink's client id unique
// per box; the gateway identity replaced that mode, and each box is now its own gateway
// device, named by uplink.gatewayToken (with uplink.tenant and a "{tenant}:{credentialId}"
// username beside it). The old key is refused, never stripped: the agent cannot run on the
// old identity, so starting without it would only move the failure to CONNECT.
func (c *Configuration) RenamedConfigKeys() map[string]string {
	return map[string]string{"agentId": "uplink.gatewayToken"}
}

func (c *Configuration) Validate() error {
	if c.InstanceId == "" {
		return fmt.Errorf("instanceId is required")
	}
	if !instanceIdPattern.MatchString(c.InstanceId) {
		return fmt.Errorf("instanceId %q is not token-safe (want %s)", c.InstanceId, instanceIdPattern)
	}
	if c.Local.StoreDir == "" {
		return fmt.Errorf("local.storeDir is required (the durable spool must survive a restart)")
	}
	if c.Local.ListenPort < 1 || c.Local.ListenPort > 65535 {
		return fmt.Errorf("local.listenPort %d out of range 1..65535", c.Local.ListenPort)
	}
	if c.Local.SpoolMaxBytes < SpoolMinBytes {
		return fmt.Errorf("local.spoolMaxBytes %d is below the %d-byte floor a JetStream file spool needs to function",
			c.Local.SpoolMaxBytes, SpoolMinBytes)
	}
	if c.Local.SpoolMaxBytes > MaxSpoolBytes {
		return fmt.Errorf("local.spoolMaxBytes %d exceeds the %d-byte ceiling", c.Local.SpoolMaxBytes, MaxSpoolBytes)
	}
	// MetricsPort is a pointer defaulted non-nil by ApplyDefaults; guard nil so a caller
	// that validates without defaulting gets an error, never a panic (fail-closed, not crash).
	if p := c.Local.MetricsPort; p != nil && *p != 0 && (*p < 1 || *p > 65535) {
		return fmt.Errorf("local.metricsPort %d out of range (0 to disable, else 1..65535)", *p)
	}
	// Local MQTT auth is both-or-neither: a username with no password source cannot be
	// enforced, and a passwordEnv with no username names a secret nothing consumes. Either
	// half alone is a misconfiguration whose runtime posture (open? closed?) is ambiguous —
	// reject it at load rather than resolve it silently one way.
	if (c.Local.Username == "") != (c.Local.PasswordEnv == "") {
		return fmt.Errorf("local.username and local.passwordEnv must be set together (or both omitted to leave the local MQTT listener open)")
	}
	if err := c.Uplink.validateIdentity(); err != nil {
		return err
	}
	if c.Uplink.BrokerURL == "" {
		return fmt.Errorf("uplink.brokerUrl is required")
	}
	u, err := url.Parse(c.Uplink.BrokerURL)
	if err != nil {
		return fmt.Errorf("uplink.brokerUrl %q is not a valid URL: %w", c.Uplink.BrokerURL, err)
	}
	switch u.Scheme {
	case "tcp", "ssl", "tls":
		// supported: tcp = plaintext, ssl/tls = TLS with system roots.
	default:
		return fmt.Errorf("uplink.brokerUrl scheme %q unsupported (want tcp://, ssl://, or tls://)", u.Scheme)
	}
	if u.User != nil {
		// Credentials must come from username + passwordEnv, never the URL: the
		// TLS path rebuilds the URL and would silently drop URL userinfo, so a
		// credential-in-URL "works" on tcp:// and vanishes on ssl:// — reject both.
		return fmt.Errorf("uplink.brokerUrl must not embed credentials; use username + passwordEnv")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("uplink.brokerUrl %q has no host", c.Uplink.BrokerURL)
	}
	if u.Port() == "" {
		return fmt.Errorf("uplink.brokerUrl %q has no port", c.Uplink.BrokerURL)
	}
	if c.Uplink.BackoffMinSeconds < 1 {
		return fmt.Errorf("uplink.backoffMinSeconds must be >= 1")
	}
	if c.Uplink.BackoffMaxSeconds < c.Uplink.BackoffMinSeconds {
		return fmt.Errorf("uplink.backoffMaxSeconds (%d) must be >= backoffMinSeconds (%d)",
			c.Uplink.BackoffMaxSeconds, c.Uplink.BackoffMinSeconds)
	}
	if c.Uplink.ConnectTimeoutSeconds < 1 {
		return fmt.Errorf("uplink.connectTimeoutSeconds must be >= 1")
	}
	switch c.Uplink.RetireOn {
	case RetireOnPuback, RetireOnAccept:
	default:
		return fmt.Errorf("uplink.retireOn %q unsupported (want %q or %q)", c.Uplink.RetireOn, RetireOnPuback, RetireOnAccept)
	}
	if c.Uplink.AcceptTimeoutSeconds < 1 {
		return fmt.Errorf("uplink.acceptTimeoutSeconds must be >= 1")
	}
	return c.Local.Priority.validate()
}

func (p *PriorityConfiguration) validate() error {
	for i, r := range p.Rules {
		if _, err := path.Match(r.Match, ""); err != nil || r.Match == "" {
			return fmt.Errorf("local.priority.rules[%d].match %q is not a valid glob", i, r.Match)
		}
		switch r.Class {
		case ClassSafety, ClassStandard, ClassBulk:
		default:
			return fmt.Errorf("local.priority.rules[%d].class %q unsupported (want %q, %q or %q)", i, r.Class, ClassSafety, ClassStandard, ClassBulk)
		}
	}
	if len(p.Rules) == 0 {
		if p.ShedAtFraction != 0 || p.ShedToFraction != 0 {
			return fmt.Errorf("local.priority.shedAtFraction/shedToFraction are set without any rules")
		}
		return nil
	}
	if !(p.ShedToFraction > 0 && p.ShedToFraction < p.ShedAtFraction && p.ShedAtFraction < 1) {
		return fmt.Errorf("local.priority needs 0 < shedToFraction (%v) < shedAtFraction (%v) < 1", p.ShedToFraction, p.ShedAtFraction)
	}
	return nil
}

// validateIdentity checks the gateway identity half of the uplink: the tenant and gateway
// tokens are subject tokens (they become topic segments), and the username must be the
// "{tenant}:{credentialId}" form the broker's callout parses, with this tenant.
func (u *UplinkConfiguration) validateIdentity() error {
	if u.Tenant == "" {
		return fmt.Errorf("uplink.tenant is required (the tenant the gateway device belongs to)")
	}
	if !instanceIdPattern.MatchString(u.Tenant) {
		return fmt.Errorf("uplink.tenant %q is not token-safe (want %s)", u.Tenant, instanceIdPattern)
	}
	if u.GatewayToken == "" {
		return fmt.Errorf("uplink.gatewayToken is required (the token of the gateway device this agent authenticates as)")
	}
	if !instanceIdPattern.MatchString(u.GatewayToken) {
		return fmt.Errorf("uplink.gatewayToken %q is not token-safe (want %s)", u.GatewayToken, instanceIdPattern)
	}
	if u.Username == "" || u.PasswordEnv == "" {
		return fmt.Errorf("uplink.username and uplink.passwordEnv are required: the gateway authenticates as its own device with a credential")
	}
	prefix := u.Tenant + ":"
	if !strings.HasPrefix(u.Username, prefix) || len(u.Username) == len(prefix) {
		return fmt.Errorf("uplink.username %q must be %q followed by the gateway credential's id: the broker reads the tenant from the part before the colon, so a bare name is refused at CONNECT", u.Username, prefix+"<credentialId>")
	}
	return nil
}
