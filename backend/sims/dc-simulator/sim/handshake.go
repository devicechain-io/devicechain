// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Endpoints is the set of base URLs a sim needs to reach the platform. All four
// are resolved by dcctl (Lane B) against the local deployment (kind ingress or
// otherwise) and handed to the sim verbatim — the sim never hardcodes a shape.
type Endpoints struct {
	// UserGraphQL is user-management's data-plane GraphQL endpoint (login,
	// selectTenant, refresh) — the base URL userclient.NewTenantSession uses.
	UserGraphQL string `json:"userGraphQL"`
	// DeviceMgmtGraphQL is device-management's tenant-scoped GraphQL endpoint,
	// used for provisioning (TenantSession.Query's baseURL).
	DeviceMgmtGraphQL string `json:"deviceMgmtGraphQL"`
	// DashboardMgmtGraphQL is dashboard-management's tenant-scoped GraphQL
	// endpoint, used for provisioning a scenario's dashboards (createDashboard
	// + publishDashboard). Still tenant-scoped, not admin — the sim's one-
	// directional admin rule is unchanged (sim-slice2-buildingpulse-spec.md).
	//
	// Required only when the chosen scenario actually provisions dashboards —
	// Provision enforces that at bootstrap (guarded by manifest.Dashboards), NOT
	// Validate below. A devicepulse scenario declares none, so a pre-slice-2
	// handshake file that never carried this field still loads and runs.
	DashboardMgmtGraphQL string `json:"dashboardMgmtGraphQL"`
	// Ingress is the device-plane HTTP ingress base (event-sources); the sim
	// POSTs to "{Ingress}/{instanceId}/{tenant}/events".
	Ingress string `json:"ingress"`
	// EventMgmtWS is event-management's graphql-ws endpoint (ws:// or wss://),
	// carrying the measurementStream subscription the presentation page uses.
	EventMgmtWS string `json:"eventMgmtWS"`
	// EventProcessingWS is event-processing's graphql-ws endpoint (ws:// or
	// wss://), carrying the detectionStream subscription the ADR-064 load-test
	// detection probes watch. Optional in the handshake, like DashboardMgmtGraphQL
	// — only the load-test detection harness needs it, so a pre-L2d handshake that
	// never carried the field still loads and runs; the harness enforces it lazily
	// at dial time rather than Validate rejecting every sim record without it.
	EventProcessingWS string `json:"eventProcessingWS"`
	// EventProcessingGraphQL is event-processing's tenant-scoped HTTP GraphQL
	// endpoint, carrying the ruleHealth query a scenario post-asserts its detection
	// rules against — the only place "this rule is RUNNING" can be read, as opposed
	// to "the publish did not error", which is not the same claim.
	//
	// Optional in the handshake like the WS endpoint beside it, so a pre-existing sim
	// record still loads. It is NOT optional at bootstrap for a scenario that
	// declares enabled detection rules: that case fails loudly rather than
	// provisioning alarm widgets nothing can confirm will ever fill.
	EventProcessingGraphQL string `json:"eventProcessingGraphQL"`
	// CommandMgmtGraphQL is command-delivery's tenant-scoped GraphQL endpoint,
	// carrying the durable command-status query (commands/commandsByToken) the
	// ADR-064 load-test command round-trip probes reconcile against. Optional in
	// the handshake, like EventProcessingWS — only the load-test command harness
	// needs it, so a pre-L2d-3 handshake that never carried the field still loads
	// and runs; the harness enforces it lazily rather than Validate rejecting every
	// sim record without it.
	CommandMgmtGraphQL string `json:"commandMgmtGraphQL"`
	// MqttBroker is the NATS MQTT gateway a scenario's COMMAND FAR END dials to
	// receive its devices' commands and answer them (ADR-043 two-way delivery) —
	// e.g. "ssl://127.0.0.1:1883" for a local kind bring-up, "tcp://..." for a
	// broker with no TLS. Unlike every other endpoint here this is not an HTTP(S)
	// base URL, because the device command plane is not HTTP.
	//
	// Optional in the handshake like EventProcessingWS and CommandMgmtGraphQL, and
	// for the same reason: a pre-widgetlab handshake never carried it. It is NOT
	// optional at bootstrap for a scenario that declares CommandFarEnd — that case
	// fails loudly rather than running a scenario whose command widget can only
	// ever demonstrate a command expiring.
	MqttBroker string `json:"mqttBroker"`
}

// Handshake is the local sim record dcctl (Lane B) writes and dc-simulator (Lane
// C) reads to come up: the scoped identity's credentials, the tenant it is
// scoped to, resolved endpoints, and which manifest/seed to run. This is the
// exact wire shape agreed between the two lanes — do not add required fields
// without updating both sides.
type Handshake struct {
	Tenant      string    `json:"tenant"`
	SimEmail    string    `json:"simEmail"`
	SimPassword string    `json:"simPassword"`
	Endpoints   Endpoints `json:"endpoints"`
	ManifestId  string    `json:"manifestId"`
	Seed        int64     `json:"seed"`
	InstanceId  string    `json:"instanceId"`
	// MqttTLSInsecure skips verification of the MQTT gateway's server certificate
	// when Endpoints.MqttBroker carries an ssl:// scheme. A local bring-up
	// terminates TLS with a self-signed cert that no system root chains to, so
	// without this the far end cannot connect at all there; a real deployment
	// reachable at its cert's SAN leaves it false.
	//
	// It is a top-level field rather than one of Endpoints because it is not an
	// address — it is a trust decision about one, and burying a verification
	// opt-out among base URLs is how it stops being read as one.
	MqttTLSInsecure bool `json:"mqttTLSInsecure"`
}

// LoadHandshake reads and validates a handshake file from path.
func LoadHandshake(path string) (*Handshake, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read handshake %q: %w", path, err)
	}
	var hs Handshake
	if err := json.Unmarshal(raw, &hs); err != nil {
		return nil, fmt.Errorf("parse handshake %q: %w", path, err)
	}
	if err := hs.Validate(); err != nil {
		return nil, fmt.Errorf("handshake %q: %w", path, err)
	}
	return &hs, nil
}

// Validate reports the first missing required field. Every field here is load-
// bearing — Bootstrap/Tick cannot proceed without it.
func (h *Handshake) Validate() error {
	missing := make([]string, 0, 8)
	if strings.TrimSpace(h.Tenant) == "" {
		missing = append(missing, "tenant")
	}
	if strings.TrimSpace(h.SimEmail) == "" {
		missing = append(missing, "simEmail")
	}
	if strings.TrimSpace(h.SimPassword) == "" {
		missing = append(missing, "simPassword")
	}
	if strings.TrimSpace(h.Endpoints.UserGraphQL) == "" {
		missing = append(missing, "endpoints.userGraphQL")
	}
	if strings.TrimSpace(h.Endpoints.DeviceMgmtGraphQL) == "" {
		missing = append(missing, "endpoints.deviceMgmtGraphQL")
	}
	// endpoints.dashboardMgmtGraphQL is intentionally NOT required here — it is
	// needed only by scenarios that provision dashboards, which Provision
	// enforces at bootstrap. Requiring it unconditionally would break a
	// pre-slice-2 devicepulse handshake that never carried the field.
	if strings.TrimSpace(h.Endpoints.Ingress) == "" {
		missing = append(missing, "endpoints.ingress")
	}
	if strings.TrimSpace(h.Endpoints.EventMgmtWS) == "" {
		missing = append(missing, "endpoints.eventMgmtWS")
	}
	if strings.TrimSpace(h.InstanceId) == "" {
		missing = append(missing, "instanceId")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required field(s): %s", strings.Join(missing, ", "))
	}
	return nil
}
