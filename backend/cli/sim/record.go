// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package sim implements `dcctl sim` — the admin/lifecycle driver for the
// DeviceChain sim subsystem (ADR-035). dcctl is the SOLE caller of the instance
// admin surface (/admin/graphql): it mints a scoped per-sim identity (a
// tenant-admin membership in exactly one tenant) and writes a handshake the
// dc-simulator process reads to come up. The sim runner itself never touches the
// admin surface — the one-directional rule that keeps the admin API from becoming
// an escalation path.
package sim

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Endpoints mirrors dc-simulator/sim.Endpoints exactly (the handshake wire
// contract). Keep the json tags in lockstep with that struct.
type Endpoints struct {
	UserGraphQL            string `json:"userGraphQL"`
	DeviceMgmtGraphQL      string `json:"deviceMgmtGraphQL"`
	DashboardMgmtGraphQL   string `json:"dashboardMgmtGraphQL"`
	Ingress                string `json:"ingress"`
	EventMgmtWS            string `json:"eventMgmtWS"`
	EventProcessingWS      string `json:"eventProcessingWS"`
	EventProcessingGraphQL string `json:"eventProcessingGraphQL"`
	CommandMgmtGraphQL     string `json:"commandMgmtGraphQL"`
	MqttBroker             string `json:"mqttBroker"`
}

// Record is dcctl's local record for one sim. It is written verbatim as the
// handshake file dc-simulator reads: dc-simulator's Handshake struct consumes the
// tenant/simEmail/simPassword/endpoints/manifestId/seed/instanceId fields and
// ignores the dcctl-only name/controlAddr. Keep the shared fields' json tags in
// lockstep with dc-simulator/sim/handshake.go.
type Record struct {
	Name        string    `json:"name"`
	Tenant      string    `json:"tenant"`
	SimEmail    string    `json:"simEmail"`
	SimPassword string    `json:"simPassword"`
	Endpoints   Endpoints `json:"endpoints"`
	ManifestId  string    `json:"manifestId"`
	Seed        int64     `json:"seed"`
	InstanceId  string    `json:"instanceId"`
	ControlAddr string    `json:"controlAddr"`
	// MqttTLSInsecure skips verification of the MQTT gateway's server certificate.
	// A local bring-up terminates TLS with a self-signed cert, so a sim's command far
	// end cannot connect there without it; a deployment reachable at its cert's SAN
	// leaves it false. Consumed by dc-simulator's Handshake.
	MqttTLSInsecure bool `json:"mqttTLSInsecure"`
	// AdminURL is the /admin/graphql endpoint create resolved, persisted so destroy
	// tears down against the same host it was created on (not a re-derived default).
	AdminURL string `json:"adminURL"`
}

// KnownManifestIds are the manifest ids dc-simulator's registry (sim.go) knows
// how to run. Mirrored here as a literal list — dcctl never imports the
// dc-simulator module (it is a separate, untrusted-client binary) — so
// `sim create --manifest` can fail fast on a typo rather than writing a
// handshake dc-simulator will reject (or silently run the wrong scenario for)
// at process start.
//
// 🔴 This is a VALIDATION GATE, not help text: an id missing here is not merely
// undocumented, it is REFUSED, and the scenario becomes unreachable through the
// only supported provisioning path. The mirror is deliberate but it is still two
// lists that must agree, so dc-simulator's TestDcctlKnowsEveryRegisteredScenario
// reads this file and fails when they diverge — a comment asking the next person
// to remember would not have survived the third scenario, and did not.
var KnownManifestIds = []string{"devicepulse", "buildingpulse", "widgetlab"}

// ValidateManifestId rejects a --manifest value dc-simulator's registry
// doesn't know.
func ValidateManifestId(id string) error {
	for _, known := range KnownManifestIds {
		if id == known {
			return nil
		}
	}
	return fmt.Errorf("unknown manifest %q (known: %s)", id, strings.Join(KnownManifestIds, ", "))
}

var nameGrammar = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidateName rejects a sim name that would not survive the derivations below
// (the tenant token grammar is the strictest consumer). Fail here rather than deep
// in an admin round-trip.
func ValidateName(name string) error {
	if !nameGrammar.MatchString(name) {
		return fmt.Errorf("sim name %q must be lower-case alphanumeric with hyphens (e.g. devicepulse)", name)
	}
	return nil
}

// DeriveTenant is the tenant token for a sim: sim-<name>. Deterministic so create
// is reproducible and destroy can find it from the name alone.
func DeriveTenant(name string) string { return "sim-" + name }

// DeriveEmail is the scoped identity's email for a sim.
func DeriveEmail(name string) string { return name + "@sim.devicechain.local" }

// GeneratePassword returns a fresh high-entropy password for the scoped identity.
func GeneratePassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// stateDir is ~/.devicechain/sims, where sim records live.
func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".devicechain", "sims"), nil
}

// RecordPath is the on-disk path of a sim's record/handshake file.
func RecordPath(name string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".json"), nil
}

// Save writes the record as its handshake file with 0600 perms (it carries the
// scoped identity's password).
func Save(r *Record) error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create sim state dir: %w", err)
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sim record: %w", err)
	}
	path := filepath.Join(dir, r.Name+".json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write sim record %q: %w", path, err)
	}
	return nil
}

// Load reads a sim's record by name.
func Load(name string) (*Record, error) {
	path, err := RecordPath(name)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no sim named %q (create it with `dcctl sim create %s`)", name, name)
		}
		return nil, fmt.Errorf("read sim record %q: %w", path, err)
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parse sim record %q: %w", path, err)
	}
	return &r, nil
}

// Exists reports whether a sim record already exists for name.
func Exists(name string) (bool, error) {
	path, err := RecordPath(name)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Delete removes a sim's record (idempotent — a missing record is not an error).
func Delete(name string) error {
	path, err := RecordPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove sim record %q: %w", path, err)
	}
	return nil
}

// scheme returns the http/ws (or tls) scheme pair.
func scheme(tls bool) (http, ws string) {
	if tls {
		return "https", "wss"
	}
	return "http", "ws"
}

// AdminURL is the instance admin GraphQL endpoint (/admin/graphql via the
// /api/user-management ingress prefix, which the chart strips to /admin/graphql).
func AdminURL(server string, tls bool) string {
	h, _ := scheme(tls)
	return fmt.Sprintf("%s://%s/api/user-management/admin/graphql", h, server)
}

// DefaultMqttBroker is the NATS MQTT gateway address for an instance host: the
// gateway's own listener, not an /api ingress route, so it is host:1883 the way the
// device-plane ingress is host:8081.
//
// The scheme is ssl:// unconditionally, because the broker's TLS is not the
// ingress's: nats_enable_tls defaults to TRUE in the OpenTofu module, so a
// standard bring-up terminates TLS on 1883 whether or not the HTTP ingress does.
// Deriving this from the --tls flag would hand a plaintext-ingress local cluster a
// tcp:// broker its gateway will not speak.
//
// A port already on `server` is DROPPED rather than appended. `--server host:8080`
// is legitimate for the HTTP endpoints (a port-forwarded ingress), and naively
// formatting it here yields "ssl://host:8080:1883" — which url.Parse and paho both
// accept, so `sim create` succeeds and writes the record, and the only symptom is a
// confusing dial failure at bootstrap much later. The gateway's port is 1883
// whatever port the HTTP ingress happens to be on, so the host is what carries over.
func DefaultMqttBroker(server string) string {
	host := server
	if h, _, err := net.SplitHostPort(server); err == nil {
		host = h
	}
	return fmt.Sprintf("ssl://%s:1883", host)
}

// BrokerIsTLS reports whether a broker URL's scheme makes the client establish TLS,
// which is what decides whether a certificate-verification choice applies to it at
// all. Mirrors dc-simulator's brokerTLSSchemes, which mirrors paho's openConnection.
// dcctl uses it only to decide whether to SAY anything about verification: telling
// an operator their certificate will not be verified on a tcp:// broker, which
// presents none, is a security-relevant sentence that is simply untrue.
func BrokerIsTLS(broker string) bool {
	for _, s := range []string{"ssl://", "tls://", "mqtts://", "mqtt+ssl://", "tcps://", "wss://"} {
		if strings.HasPrefix(broker, s) {
			return true
		}
	}
	return false
}

// ResolveEndpoints derives the platform endpoints the sim needs from the instance
// host, using the chart's /api/<area> ingress convention (prefix stripped to the
// service's /graphql). ingress overrides the device-plane HTTP ingress base, which
// is NOT on the /api ingress (event-sources :8081); it defaults to
// http(s)://<server>:8081, matching a port-forward of that service. mqttBroker
// likewise overrides the MQTT gateway address a scenario's command far end dials.
func ResolveEndpoints(server, ingress, mqttBroker string, tls bool) Endpoints {
	h, ws := scheme(tls)
	if strings.TrimSpace(ingress) == "" {
		ingress = fmt.Sprintf("%s://%s:8081", h, server)
	}
	if strings.TrimSpace(mqttBroker) == "" {
		mqttBroker = DefaultMqttBroker(server)
	}
	return Endpoints{
		UserGraphQL:            fmt.Sprintf("%s://%s/api/user-management/graphql", h, server),
		DeviceMgmtGraphQL:      fmt.Sprintf("%s://%s/api/device-management/graphql", h, server),
		DashboardMgmtGraphQL:   fmt.Sprintf("%s://%s/api/dashboard-management/graphql", h, server),
		Ingress:                ingress,
		EventMgmtWS:            fmt.Sprintf("%s://%s/api/event-management/graphql", ws, server),
		EventProcessingWS:      fmt.Sprintf("%s://%s/api/event-processing/graphql", ws, server),
		EventProcessingGraphQL: fmt.Sprintf("%s://%s/api/event-processing/graphql", h, server),
		CommandMgmtGraphQL:     fmt.Sprintf("%s://%s/api/command-delivery/graphql", h, server),
		MqttBroker:             mqttBroker,
	}
}
