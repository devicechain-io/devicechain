// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	// Verbatim from `helm template` of a one-source sparkplug-ingest config.
	rendered := `{"sources":[{"broker":{"passwordEnv":"SPARKPLUG_ACME_PW","url":"ssl://broker.acme.example:8883","username":"devicechain"},"groups":["plant-a","plant-b"],"hostId":"dc-acme","tenant":"acme"}]}`

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
	clients, err := resolveSources(cfg, "inst", host.Metrics{})
	require.NoError(t, err)
	assert.Len(t, clients, 2)

	empty, err := resolveSources(&config.SparkplugConfiguration{}, "inst", host.Metrics{})
	require.NoError(t, err)
	assert.Empty(t, empty)
}
