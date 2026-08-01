// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"strings"
	"testing"
)

// The MQTT gateway is a PORT on the instance host, not a route on the /api ingress,
// so a port already carried by --server belongs to the HTTP endpoints and not here.
//
// 🔴 `--server host:8080` is legitimate — it is what a port-forwarded ingress looks
// like — and formatting it naively yields "ssl://host:8080:1883". url.Parse and paho
// both ACCEPT that, so `sim create` succeeds and writes the record, and the only
// symptom is a dial failure at bootstrap, far from the input that caused it.
func TestDefaultMqttBrokerDropsAPortAlreadyOnTheServer(t *testing.T) {
	cases := map[string]string{
		"devicechain.local":      "ssl://devicechain.local:1883",
		"devicechain.local:8080": "ssl://devicechain.local:1883",
		"127.0.0.1":              "ssl://127.0.0.1:1883",
		"127.0.0.1:8080":         "ssl://127.0.0.1:1883",
	}
	for server, want := range cases {
		if got := DefaultMqttBroker(server); got != want {
			t.Errorf("DefaultMqttBroker(%q) = %q, want %q", server, got, want)
		}
	}
}

// The scheme decides whether a certificate-verification choice applies at all, and
// this list is paho's — the client that actually dials. dcctl uses it to decide
// whether to TELL the operator their certificate will not be verified, and saying
// that about a plaintext broker (which presents none) is a security-relevant
// sentence that is simply untrue.
func TestBrokerIsTLSMatchesWhatTheClientDials(t *testing.T) {
	for _, broker := range []string{
		"ssl://h:1883", "tls://h:1883", "mqtts://h:1883",
		"mqtt+ssl://h:1883", "tcps://h:1883", "wss://h:443",
	} {
		if !BrokerIsTLS(broker) {
			t.Errorf("BrokerIsTLS(%q) = false; the client dials that scheme over TLS", broker)
		}
	}
	for _, broker := range []string{"tcp://h:1883", "mqtt://h:1883", "ws://h:80", "unix://sock", ""} {
		if BrokerIsTLS(broker) {
			t.Errorf("BrokerIsTLS(%q) = true; no TLS is established there", broker)
		}
	}
}

// Every endpoint the sim reads has to come out of one host, and the two overridable
// ones have to actually override. A default that silently won an explicit flag would
// point a run at a different cluster than the operator named.
func TestResolveEndpointsDerivesAndOverrides(t *testing.T) {
	e := ResolveEndpoints("dc.local", "", "", false)
	for name, got := range map[string]string{
		"userGraphQL":          e.UserGraphQL,
		"deviceMgmtGraphQL":    e.DeviceMgmtGraphQL,
		"dashboardMgmtGraphQL": e.DashboardMgmtGraphQL,
		"ingress":              e.Ingress,
		"eventMgmtWS":          e.EventMgmtWS,
		"eventProcessingWS":    e.EventProcessingWS,
		"commandMgmtGraphQL":   e.CommandMgmtGraphQL,
		"mqttBroker":           e.MqttBroker,
	} {
		if got == "" {
			t.Errorf("endpoint %s resolved empty", name)
		}
		if !strings.Contains(got, "dc.local") {
			t.Errorf("endpoint %s = %q, which does not name the instance host", name, got)
		}
	}
	if e.MqttBroker != "ssl://dc.local:1883" {
		t.Errorf("default mqttBroker = %q", e.MqttBroker)
	}
	// The HTTP scheme follows --tls; the broker's does NOT, because the gateway
	// terminates its own TLS (nats_enable_tls defaults true) whether or not the HTTP
	// ingress does. Deriving it from --tls would hand a plaintext-ingress local
	// cluster a tcp:// broker its gateway will not speak.
	secure := ResolveEndpoints("dc.local", "", "", true)
	if !strings.HasPrefix(secure.UserGraphQL, "https://") || !strings.HasPrefix(secure.EventMgmtWS, "wss://") {
		t.Errorf("--tls did not reach the HTTP/WS endpoints: %q, %q", secure.UserGraphQL, secure.EventMgmtWS)
	}
	if secure.MqttBroker != e.MqttBroker {
		t.Errorf("--tls changed the broker URL (%q vs %q); the broker's TLS is not the ingress's",
			secure.MqttBroker, e.MqttBroker)
	}

	overridden := ResolveEndpoints("dc.local", "http://ingress.example:9000", "tcp://broker.example:1884", false)
	if overridden.Ingress != "http://ingress.example:9000" {
		t.Errorf("--ingress was not honoured: %q", overridden.Ingress)
	}
	if overridden.MqttBroker != "tcp://broker.example:1884" {
		t.Errorf("--mqtt-broker was not honoured: %q", overridden.MqttBroker)
	}
}

// A manifest id dc-simulator's registry does not know is REFUSED here, not merely
// undocumented — so the counterweight matters as much as the rejection: an id that
// IS known must pass, or the only supported provisioning path is closed.
func TestValidateManifestId(t *testing.T) {
	if len(KnownManifestIds) == 0 {
		t.Fatal("KnownManifestIds is empty; every create would be refused")
	}
	for _, id := range KnownManifestIds {
		if err := ValidateManifestId(id); err != nil {
			t.Errorf("known manifest %q was refused: %v", id, err)
		}
	}
	err := ValidateManifestId("widgetlabb")
	if err == nil {
		t.Fatal("a typo'd manifest id was accepted; the sim would run the wrong scenario or fail at start")
	}
	// The message has to carry the alternatives, since the whole point is catching
	// a typo the operator cannot see.
	for _, id := range KnownManifestIds {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("rejection %q does not list the known id %q", err, id)
		}
	}
}
