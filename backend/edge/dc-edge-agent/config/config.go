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
	"regexp"
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

	// AgentId uniquely identifies THIS edge box among all agents feeding the same
	// Instance. It is the tail of the uplink MQTT client id: two sites on one
	// Instance is the normal Tier-1 topology, and MQTT client-id takeover would
	// otherwise make each agent's connect boot the other in a permanent mutual-kick
	// loop. Required, and token-safe so it is a clean client-id segment.
	AgentId string `json:"agentId"`

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
}

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

	// Username is the uplink MQTT login (may be empty for anonymous brokers).
	Username string `json:"username"`

	// PasswordEnv names the environment variable holding the uplink password (a
	// projected Secret, never cleartext in this document). Empty means no password.
	PasswordEnv string `json:"passwordEnv"`

	// ConnectTimeoutSeconds bounds a single connect attempt. Defaults to 30.
	ConnectTimeoutSeconds int `json:"connectTimeoutSeconds"`

	// BackoffMinSeconds / BackoffMaxSeconds bound the reconnect backoff so a
	// flapping WAN is not hammered. Default 1 / 60.
	BackoffMinSeconds int `json:"backoffMinSeconds"`
	BackoffMaxSeconds int `json:"backoffMaxSeconds"`
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
}

// Validate enforces the semantic constraints, failing the load closed.
func (c *Configuration) Validate() error {
	if c.InstanceId == "" {
		return fmt.Errorf("instanceId is required")
	}
	if !instanceIdPattern.MatchString(c.InstanceId) {
		return fmt.Errorf("instanceId %q is not token-safe (want %s)", c.InstanceId, instanceIdPattern)
	}
	if c.AgentId == "" {
		return fmt.Errorf("agentId is required (it makes the uplink client id unique so two sites on one Instance do not kick each other)")
	}
	if !instanceIdPattern.MatchString(c.AgentId) {
		return fmt.Errorf("agentId %q is not token-safe (want %s)", c.AgentId, instanceIdPattern)
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
	return nil
}
