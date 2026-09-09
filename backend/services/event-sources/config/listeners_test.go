// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// httpSource builds one HTTP event source on the given port, as an operator's document
// would express it.
func httpSource(id string, port int) EventSource {
	return EventSource{
		Id:            id,
		Type:          SourceTypeHttp,
		Configuration: map[string]string{"port": strconv.Itoa(port)},
		Decoder:       EventDecoder{Type: "json", Configuration: map[string]string{}},
	}
}

// Two HTTP sources on the same port are refused BEFORE anything binds, and the message
// names both of them and the port.
//
// This service runs two kinds of listener in one process, and nothing compared their
// ports. While every listener bound inside a goroutine that cost one ingest transport,
// silently; now that a bind failure is fatal it stops the whole deployment, on a
// configuration that gives an operator no signal until the upgrade. The bind error it
// would otherwise produce names the symptom — an address in use — and neither of the
// two pieces of configuration that collided.
func TestValidate_RefusesTwoSourcesOnOnePort(t *testing.T) {
	cfg := &EventSourcesConfiguration{
		EventSources: []EventSource{httpSource("http1", 8081), httpSource("http2", 8081)},
	}

	err := cfg.Validate()
	require.Error(t, err, "two listeners configured on one port must not reach the bind")
	msg := err.Error()
	assert.Contains(t, msg, `"http2"`, "the refusal must name the source that collided")
	assert.Contains(t, msg, `"http1"`, "the refusal must name the source it collided WITH")
	assert.Contains(t, msg, "8081", "the refusal must name the port")
}

// An HTTP source on the GraphQL/management port is refused, and the message says which
// two listeners collided — the second of which is a listener the operator never wrote
// down and would otherwise have no way to identify from an address-in-use error.
func TestValidate_RefusesSourceOnManagementPort(t *testing.T) {
	cfg := &EventSourcesConfiguration{
		EventSources: []EventSource{httpSource("http1", gqlcore.GRAPHQL_PORT)},
	}

	err := cfg.Validate()
	require.Error(t, err, "a source on the management port must not reach the bind")
	msg := err.Error()
	assert.Contains(t, msg, `"http1"`, "the refusal must name the source")
	assert.Contains(t, strings.ToLower(msg), "graphql", "the refusal must name the listener it collided with")
	assert.Contains(t, msg, strconv.Itoa(gqlcore.GRAPHQL_PORT), "the refusal must name the port")
}

// Port 0 is refused. It is in the TCP range and passes every range check, and
// net.Listen reads it as "any free port" — so the source would bind an ephemeral port,
// start successfully, log a healthy address, and ingest nothing a device can reach.
func TestValidate_RefusesEphemeralPort(t *testing.T) {
	cfg := &EventSourcesConfiguration{EventSources: []EventSource{httpSource("http1", 0)}}

	err := cfg.Validate()
	require.Error(t, err, "a device-facing listener on port 0 must be refused")
	assert.Contains(t, err.Error(), `"http1"`, "the refusal must name the source")
	assert.Contains(t, err.Error(), "any free port", "the refusal must say what 0 actually does")
}

// An unparseable or out-of-range port is refused by the same pass, so a document that
// cannot produce a listener never reaches the point where one is built.
func TestValidate_RefusesUnusablePort(t *testing.T) {
	for _, raw := range []string{"abc", "70000", "-1"} {
		cfg := &EventSourcesConfiguration{
			EventSources: []EventSource{{
				Id:            "http1",
				Type:          SourceTypeHttp,
				Configuration: map[string]string{"port": raw},
			}},
		}
		assert.Errorf(t, cfg.Validate(), "port %q must be refused", raw)
	}
}

// 🔑 THE COUNTERWEIGHT. A gate that only refuses can pass while the feature is broken —
// a validation that rejected every configuration would satisfy every test above. A
// correct multi-source document must still load.
func TestValidate_AcceptsDistinctListenerPorts(t *testing.T) {
	cfg := &EventSourcesConfiguration{
		EventSources: []EventSource{
			httpSource("http1", 8081),
			httpSource("http2", 8082),
			{
				// An MQTT source's port is the port it DIALS OUT to on a broker, not a
				// port it listens on — so it collides with nothing, even when it is the
				// management port. Counting it would refuse a correct configuration.
				Id:            "mqtt1",
				Type:          "mqtt",
				Configuration: map[string]string{"host": "dc-nats.dc-system", "port": strconv.Itoa(gqlcore.GRAPHQL_PORT)},
			},
		},
	}

	assert.NoError(t, cfg.Validate(), "distinct listener ports plus an outbound source must load")
}

// The configuration the service ships with must pass its own validation. The default
// document carries an HTTP source, and the shipped defaults are the one configuration
// every instance that sets nothing is running.
func TestValidate_AcceptsShippedDefaults(t *testing.T) {
	assert.NoError(t, NewEventSourcesConfiguration().Validate())
}

// A source that names no port at all takes the default ingest port, NOT 0 — the absent
// and empty cases are what an operator arrives at by dropping a key, and if either
// yielded 0 the refusal above would be firing on ordinary documents.
func TestHttpSourcePort_AbsentAndEmptyTakeTheDefault(t *testing.T) {
	port, err := HttpSourcePort(map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, DefaultHttpIngestPort, port)

	port, err = HttpSourcePort(map[string]string{"port": ""})
	require.NoError(t, err)
	assert.Equal(t, DefaultHttpIngestPort, port)
}

// The default ingest port is not the management port. The two constants are what keep
// device telemetry and the management API off one listener, and a change that made them
// equal would make the shipped default refuse to start.
func TestDefaultIngestPortIsNotTheManagementPort(t *testing.T) {
	assert.NotEqual(t, gqlcore.GRAPHQL_PORT, DefaultHttpIngestPort)
}

// The device-facing listener bounds are configurable, default to the documented values,
// and never fall back to "no bound at all".
func TestHttpIngestTimeouts(t *testing.T) {
	var unset HttpIngest
	assert.Equal(t, time.Duration(DefaultHttpHeaderTimeoutSeconds)*time.Second, unset.HeaderTimeout())
	assert.Equal(t, time.Duration(DefaultHttpRequestTimeoutSeconds)*time.Second, unset.RequestTimeout())

	configured := HttpIngest{HeaderTimeoutSeconds: 30, RequestTimeoutSeconds: 120}
	assert.Equal(t, 30*time.Second, configured.HeaderTimeout())
	assert.Equal(t, 120*time.Second, configured.RequestTimeout())

	// Fail-safe, as everywhere else in this package: a non-positive value falls back to
	// the platform default rather than to zero, which for a timeout means unbounded.
	negative := HttpIngest{HeaderTimeoutSeconds: -1, RequestTimeoutSeconds: -1}
	assert.Equal(t, time.Duration(DefaultHttpHeaderTimeoutSeconds)*time.Second, negative.HeaderTimeout())
	assert.Equal(t, time.Duration(DefaultHttpRequestTimeoutSeconds)*time.Second, negative.RequestTimeout())
}

// The bounds arrive through the real load path — a document an operator writes, decoded
// by the strict loader — rather than by assigning the struct field in a test. A typed
// setting that is never reached by a document is a setting nobody can use.
func TestLoadConfiguration_CarriesHttpIngestBounds(t *testing.T) {
	doc := []byte(`{"httpIngest":{"headerTimeoutSeconds":20,"requestTimeoutSeconds":90}}`)

	cfg := &EventSourcesConfiguration{}
	require.NoError(t, core.LoadConfiguration(doc, cfg))
	assert.Equal(t, 20*time.Second, cfg.HttpIngest.HeaderTimeout())
	assert.Equal(t, 90*time.Second, cfg.HttpIngest.RequestTimeout())
}

// And a colliding document fails the LOAD, not merely the Validate call — the whole
// point is that the refusal happens at startup, where the operator sees it, rather than
// at the bind.
func TestLoadConfiguration_FailsClosedOnCollidingListeners(t *testing.T) {
	doc, err := json.Marshal(map[string]any{
		"eventSources": []EventSource{httpSource("http1", 8081), httpSource("http2", 8081)},
	})
	require.NoError(t, err)

	err = core.LoadConfiguration(doc, &EventSourcesConfiguration{})
	require.Error(t, err, "a colliding document must fail the load")
	assert.Contains(t, err.Error(), "8081")
}
