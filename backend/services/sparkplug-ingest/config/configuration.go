// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package config is the typed configuration for the Sparkplug ingest service
// (ADR-069): a stateful Sparkplug B Host Application that terminates edge-node
// telemetry from one or more per-tenant customer brokers.
//
// Tenancy is CONNECTION-SCOPED (ADR-069 M7). A Sparkplug topic carries no tenant,
// its Group ID is a customer's own organizational label — not globally unique and
// spoofable by any publisher — so a tenant is NEVER derived from topic content.
// Instead the adapter connects OUT to a per-tenant broker (Fork A) and every
// message received on that connection is attributed to the tenant configured for
// it here. The connection is the trust boundary.
package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
)

// DefaultHostId is the Sparkplug Host Application identity used when a source
// configures none. It appears in the STATE topic (spBv1.0/STATE/{HostId}) that
// edge nodes watch to decide whether their primary host is online, so it must be a
// stable, MQTT-topic-safe token; "devicechain" is the platform default.
const DefaultHostId = "devicechain"

// SparkplugConfiguration is the top-level configuration for the adapter: the set
// of per-tenant Sparkplug sources it connects to (ADR-069). An empty set is a
// valid, inert configuration (the service runs but connects to nothing) — this is
// the default a profile renders before any tenant enables Sparkplug.
type SparkplugConfiguration struct {
	// Sources is the per-tenant broker connections this adapter maintains, one
	// Host Application connection each. Tenancy is fixed per source (M7); see the
	// package doc.
	Sources []SparkplugSource
}

// SparkplugSource is one tenant-bound Sparkplug Host Application connection. The
// adapter opens exactly one MQTT connection per source and attributes every
// message received on it to Tenant.
type SparkplugSource struct {
	// Tenant is the DeviceChain tenant token every message received on this
	// connection is attributed to. Required — connection-scoped tenancy is the
	// whole point of a source, and an empty tenant would produce un-attributable
	// telemetry that no downstream subject or governance key could route.
	Tenant string

	// HostId is this connection's Sparkplug Host Application identifier — the final
	// segment of the STATE topic (spBv1.0/STATE/{HostId}) an edge node on Broker
	// subscribes to in order to learn whether its primary host is online (Sparkplug
	// 3.0). It is per source because each source is a distinct Host Application on a
	// distinct broker; it must be a single MQTT topic level with no wildcard.
	// Defaults to DefaultHostId.
	HostId string

	// Broker is the customer MQTT broker this source connects out to.
	Broker SourceBroker

	// Groups restricts the Sparkplug Group IDs subscribed on this broker
	// (spBv1.0/{group}/#, one subscription per entry). Empty means subscribe to
	// EVERY group on the broker (spBv1.0/#). Each entry must be a single MQTT topic
	// level with no wildcard.
	Groups []string

	// DeviceTypeToken is the device type stamped on a device this source
	// auto-registers. createDevice requires an existing device type and a Sparkplug
	// NBIRTH carries no DeviceChain type, so the operator names one here; it must be
	// a device type they created up front. Required when AutoRegister is true.
	DeviceTypeToken string

	// AutoRegister decides what happens the first time this source sees a Sparkplug
	// identity with no matching DeviceChain device (matched by external id, ADR-049).
	// True: the adapter creates the device (stamped DeviceTypeToken) and ingests it.
	// False: the identity is dropped — counted, never silently created — until an
	// operator pre-registers it (the curated-roster / CHECK_PRE_PROVISIONED posture).
	AutoRegister bool
}

// SourceBroker is the MQTT connection detail for one customer broker.
type SourceBroker struct {
	// URL is the broker address, e.g. tcp://broker.plant-a.example:1883 or
	// ssl://broker.plant-a.example:8883 for TLS. Required. A ssl:// (or tls://)
	// scheme selects TLS with the system root CAs.
	URL string

	// Username is the MQTT login (optional; empty ⇒ anonymous connect).
	Username string

	// PasswordEnv NAMES the environment variable holding the MQTT password — never
	// the cleartext (a broker password written into the mounted config document
	// would be a plaintext-at-rest credential). The Helm chart projects a Kubernetes
	// Secret into this variable; the adapter reads it once at startup. Optional;
	// empty ⇒ no password is sent.
	//
	// A statically-configured source is operator-authored infrastructure, so its
	// broker credential is a 12-factor env/k8s-secret, not an entry in the ADR-059
	// tenant secret store (that store holds API-authored tenant secrets and would
	// have no PUT path for a source that has no authoring API yet). When a
	// SparkplugSource becomes a managed entity post-GA, credential resolution moves
	// to the ADR-059 store, matching outbound-connectors' Connector.
	PasswordEnv string
}

// NewSparkplugConfiguration builds a defaulted (empty) configuration.
func NewSparkplugConfiguration() *SparkplugConfiguration {
	cfg := &SparkplugConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills unset fields (ADR-022 decision 1). Only a source's HostId
// has a universal default; an empty Groups list is a meaningful value (subscribe
// to all groups), not an unset one, so it is left alone. An empty Sources list is
// likewise meaningful (connect to nothing).
func (c *SparkplugConfiguration) ApplyDefaults() {
	for i := range c.Sources {
		if strings.TrimSpace(c.Sources[i].HostId) == "" {
			c.Sources[i].HostId = DefaultHostId
		}
	}
}

// Validate fails the load closed on a configuration that would connect with no
// tenant to attribute to, dial an unusable broker URL, produce an invalid MQTT
// subscription or STATE topic, bind one broker to more than one tenant, or collide
// two Host Applications on one broker (ADR-022 decision 1). Rejecting here keeps a
// misconfiguration from silently mis-routing telemetry, leaking one tenant's data
// into another, or fighting itself for a STATE identity at the broker.
func (c *SparkplugConfiguration) Validate() error {
	// endpointTenant enforces the M7 invariant that a broker connection is the
	// tenant trust boundary: a normalized broker endpoint may serve exactly one
	// tenant. stateIdentity catches two Host Applications on one broker sharing a
	// STATE topic. Both key on the NORMALIZED endpoint so a case- or trailing-slash
	// variant of the same URL cannot dodge either check.
	endpointTenant := map[string]string{}
	stateIdentity := map[string]int{}
	for i := range c.Sources {
		s := &c.Sources[i]
		if strings.TrimSpace(s.Tenant) == "" {
			return fmt.Errorf("sources[%d]: tenant is required", i)
		}
		endpoint, err := normalizeBrokerEndpoint(s.Broker.URL)
		if err != nil {
			return fmt.Errorf("sources[%d]: %w", i, err)
		}
		if err := validateTopicToken(fmt.Sprintf("sources[%d].hostId", i), s.HostId); err != nil {
			return err
		}
		for j, g := range s.Groups {
			if err := validateTopicToken(fmt.Sprintf("sources[%d].groups[%d]", i, j), g); err != nil {
				return err
			}
		}
		// A source that auto-registers MUST name the device type to stamp — a device
		// cannot be created without one, and defaulting a type would silently file a
		// tenant's whole fleet under a placeholder. When a type is named (registering
		// or not) it must satisfy the global token grammar so a malformed value fails
		// the load rather than every createDevice at runtime.
		if s.AutoRegister && strings.TrimSpace(s.DeviceTypeToken) == "" {
			return fmt.Errorf("sources[%d]: deviceTypeToken is required when autoRegister is true (a device cannot be created without a device type)", i)
		}
		if s.DeviceTypeToken != "" {
			if err := core.ValidateToken(s.DeviceTypeToken); err != nil {
				return fmt.Errorf("sources[%d].deviceTypeToken: %w", i, err)
			}
		}
		// One broker, one tenant. Two tenants on one broker would either both receive
		// every message (dual attribution) or be told apart by the Group ID — which is
		// publisher-controlled and spoofable, exactly what connection-scoped tenancy
		// forbids. (Same-tenant multiple sources on one broker are fine.)
		if prev, ok := endpointTenant[endpoint]; ok && prev != s.Tenant {
			return fmt.Errorf("sources[%d]: broker %q is already bound to tenant %q and cannot also serve tenant %q — a broker connection is the tenant trust boundary (M7)", i, s.Broker.URL, prev, s.Tenant)
		}
		endpointTenant[endpoint] = s.Tenant

		key := endpoint + "\x00" + s.HostId
		if prev, dup := stateIdentity[key]; dup {
			return fmt.Errorf("sources[%d]: broker %q + hostId %q collides with sources[%d] (two Host Applications cannot share a STATE identity on one broker)", i, s.Broker.URL, s.HostId, prev)
		}
		stateIdentity[key] = i
	}
	return nil
}

// normalizeBrokerEndpoint parses a broker URL, requires a supported scheme
// (tcp:// plaintext or ssl:///tls:// TLS) and a host, and returns a canonical
// host:port key (lowercased host) used to compare brokers. A scheme-less string
// like "broker:1883" parses with an empty Host and is rejected here rather than
// dialed. Keeping this in Validate() means a config-only consumer (a lint tool, a
// future authoring API) rejects the same URLs the service refuses.
func normalizeBrokerEndpoint(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("broker.url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("broker.url %q is not a valid URL: %w", raw, err)
	}
	switch u.Scheme {
	case "tcp", "ssl", "tls":
	default:
		return "", fmt.Errorf("broker.url %q has unsupported scheme %q (want tcp:// or ssl://)", raw, u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("broker.url %q must include a host", raw)
	}
	return strings.ToLower(u.Host), nil
}

// validateTopicToken requires a non-empty single MQTT topic level: no separator
// ('/'), no wildcards ('+'/'#'), and no NUL. These are exactly the characters
// that would let a value escape its intended topic level and either mis-address
// the retained STATE message or widen a subscription past its group.
func validateTopicToken(field, raw string) error {
	if raw == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.ContainsAny(raw, "/+#\x00") {
		return fmt.Errorf("%s: must be a single MQTT topic level with no '/', '+', '#', or NUL (got %q)", field, raw)
	}
	return nil
}
