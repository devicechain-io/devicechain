// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

// TestLoadConfigurationParsesRenderedChartDocument pins the chart↔service seam.
// The Helm chart renders this exact JSON into the mounted config document; the
// service parses it with DisallowUnknownFields, so a single key the struct does
// not name (a chart/struct drift) is a startup crash-loop, invisible to any test
// that hand-builds the struct. This feeds the literal rendered bytes through the
// real loader.
func TestLoadConfigurationParsesRenderedChartDocument(t *testing.T) {
	// Verbatim from `helm template` of a one-source sparkplug-ingest config, including
	// the SP3b auto-registration keys (autoRegister/deviceTypeToken) — the seam that
	// crash-loops on a chart↔struct drift.
	rendered := `{"sources":[{"autoRegister":true,"broker":{"passwordEnv":"SPARKPLUG_ACME_PW","url":"ssl://broker.acme.example:8883","username":"devicechain"},"deviceTypeToken":"sparkplug-node","groups":["plant-a","plant-b"],"hostId":"dc-acme","tenant":"acme"}]}`

	var cfg config.SparkplugConfiguration
	require.NoError(t, core.LoadConfiguration([]byte(rendered), &cfg))
	require.Len(t, cfg.Sources, 1)
	s := cfg.Sources[0]
	assert.Equal(t, "acme", s.Tenant)
	assert.Equal(t, "dc-acme", s.HostId)
	assert.Equal(t, "ssl://broker.acme.example:8883", s.Broker.URL)
	assert.Equal(t, "devicechain", s.Broker.Username)
	assert.Equal(t, "SPARKPLUG_ACME_PW", s.Broker.PasswordEnv)
	assert.Equal(t, []string{"plant-a", "plant-b"}, s.Groups)
	assert.True(t, s.AutoRegister)
	assert.Equal(t, "sparkplug-node", s.DeviceTypeToken)

	require.NoError(t, cfg.Validate(), "the rendered document must also pass Validate")
}

// TestBuildIngesterRejectsNilWriter pins the BLOCKER fix: the durable writer is
// created synchronously after NatsManager.Initialize (the oncreate callback that
// would otherwise populate it does not run until Start), so a wiring-order regression
// that hands buildIngester a nil writer must FAIL CLOSED at startup rather than build
// an emitter that nil-panics the receive goroutine on the first message. The nil
// check precedes any Microservice access, so this needs no lifecycle setup.
func TestBuildIngesterRejectsNilWriter(t *testing.T) {
	_, _, err := buildIngester(nil)
	require.Error(t, err, "a nil inbound-events writer must fail closed")
}

// TestIngestEndpointFailsClosed pins that the ingest path refuses to come up half-
// configured: with no service secret or no device-management coordinate it returns
// an error (a startup failure) rather than a URL that would resolve no devices and
// silently drop all telemetry. A fully-configured infra yields the GraphQL URL.
func TestIngestEndpointFailsClosed(t *testing.T) {
	full := mscfg.InfrastructureConfiguration{
		ServiceAuth:      mscfg.ServiceAuthConfiguration{Secret: "s3cret"},
		DeviceManagement: mscfg.DeviceManagementConfiguration{Hostname: "device-management", Port: 8080},
	}
	url, err := ingestEndpoint(full)
	require.NoError(t, err)
	assert.Equal(t, "http://device-management:8080/graphql", url)

	noSecret := full
	noSecret.ServiceAuth.Secret = ""
	_, err = ingestEndpoint(noSecret)
	require.Error(t, err, "no service secret must fail closed")

	noDM := full
	noDM.DeviceManagement = mscfg.DeviceManagementConfiguration{}
	_, err = ingestEndpoint(noDM)
	require.Error(t, err, "no device-management coordinate must fail closed")
}

// TestDeviceStateEndpointFailsClosed pins that failover reconciliation refuses to come
// up unconfigured (ADR-067 SP4b): with no device-state coordinate it errors at startup
// rather than silently disabling the reconciliation that keeps a missed death from
// stranding a device CONNECTED. A configured coordinate yields the GraphQL URL.
func TestDeviceStateEndpointFailsClosed(t *testing.T) {
	full := mscfg.InfrastructureConfiguration{
		DeviceState: mscfg.DeviceStateConfiguration{Hostname: "device-state", Port: 8080},
	}
	url, err := deviceStateEndpoint(full)
	require.NoError(t, err)
	assert.Equal(t, "http://device-state:8080/graphql", url)

	_, err = deviceStateEndpoint(mscfg.InfrastructureConfiguration{})
	require.Error(t, err, "no device-state coordinate must fail closed")
}

// src is a minimal valid source for resolveBroker tests.
func src(url string) config.SparkplugSource {
	return config.SparkplugSource{Tenant: "acme", HostId: "devicechain", Broker: config.SourceBroker{URL: url}}
}

// TestResolveBrokerSelectsTLSByScheme is the load-bearing guard: the URL scheme,
// not a config flag, decides whether the connection is encrypted. A ssl:// URL
// must produce a TLS config (so credentials are not sent in the clear) and a tcp://
// URL must not. If this inverted, a source meant to be TLS would connect plaintext.
func TestResolveBrokerSelectsTLSByScheme(t *testing.T) {
	tlsBroker, err := resolveBroker(src("ssl://broker.example:8883"), "inst", 0)
	require.NoError(t, err)
	require.NotNil(t, tlsBroker.TLS, "ssl:// must select TLS")
	assert.GreaterOrEqual(t, tlsBroker.TLS.MinVersion, uint16(0x0303), "TLS 1.2 floor")

	plain, err := resolveBroker(src("tcp://broker.example:1883"), "inst", 0)
	require.NoError(t, err)
	assert.Nil(t, plain.TLS, "tcp:// must not select TLS")
}

// TestResolveBrokerRejectsBadScheme fails closed on a URL that names no scheme or
// an unsupported one, rather than guessing a transport.
func TestResolveBrokerRejectsBadScheme(t *testing.T) {
	_, err := resolveBroker(src("broker.example:1883"), "inst", 0)
	require.Error(t, err, "a URL with no scheme is rejected")

	_, err = resolveBroker(src("http://broker.example"), "inst", 0)
	require.Error(t, err, "an unsupported scheme is rejected")
}

// TestResolveBrokerReadsPasswordFromEnv proves the password comes from the named
// environment variable (a projected Secret), never from config, and that a
// configured-but-unset variable fails CLOSED — a silently-empty password would
// dial the broker unauthenticated and look like a broker-side auth problem.
func TestResolveBrokerReadsPasswordFromEnv(t *testing.T) {
	s := src("tcp://broker.example:1883")
	s.Broker.Username = "u"
	s.Broker.PasswordEnv = "SPARKPLUG_TEST_PW"

	t.Setenv("SPARKPLUG_TEST_PW", "s3cret")
	b, err := resolveBroker(s, "inst", 0)
	require.NoError(t, err)
	assert.Equal(t, "s3cret", b.Password)
	assert.Equal(t, "u", b.Username)

	// Configured env name, but the variable is empty ⇒ fail closed.
	t.Setenv("SPARKPLUG_TEST_PW", "")
	_, err = resolveBroker(s, "inst", 0)
	require.Error(t, err, "an intended-but-empty password env must fail closed")

	// No PasswordEnv at all ⇒ anonymous, no error.
	anon, err := resolveBroker(src("tcp://broker.example:1883"), "inst", 0)
	require.NoError(t, err)
	assert.Empty(t, anon.Password)
}

// TestResolveBrokerClientIdIsUniquePerSource proves two sources — even on the SAME
// broker — get distinct MQTT client ids. A shared id makes the broker drop one of
// the two connections nondeterministically (a silent half-outage).
func TestResolveBrokerClientIdIsUniquePerSource(t *testing.T) {
	b0, err := resolveBroker(src("tcp://shared:1883"), "inst", 0)
	require.NoError(t, err)
	b1, err := resolveBroker(src("tcp://shared:1883"), "inst", 1)
	require.NoError(t, err)
	assert.NotEqual(t, b0.ClientID, b1.ClientID, "each source needs a unique client id")
}

// TestResolveSourcesBuildsOnePerSource pins that the whole config lowers to one
// client per source, and that an empty config is valid (zero clients, inert).
func TestResolveSourcesBuildsOnePerSource(t *testing.T) {
	cfg := &config.SparkplugConfiguration{Sources: []config.SparkplugSource{
		src("tcp://a:1883"), src("ssl://b:8883"),
	}}
	clients, err := resolveSources(cfg, "inst", nil, nil, host.Metrics{})
	require.NoError(t, err)
	assert.Len(t, clients, 2)

	empty, err := resolveSources(&config.SparkplugConfiguration{}, "inst", nil, nil, host.Metrics{})
	require.NoError(t, err)
	assert.Empty(t, empty)
}
