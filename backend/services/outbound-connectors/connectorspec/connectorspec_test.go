// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupported(t *testing.T) {
	assert.True(t, Supported("mqtt"))
	assert.False(t, Supported("kafka")) // not yet shipped (C4c)
	assert.False(t, Supported("carrier_pigeon"))
}

func TestValidateMQTT(t *testing.T) {
	good := []string{
		`{"urls":["tcp://b:1883"],"topic":"t"}`,
		`{"urls":["tcp://b:1883"],"topic":"t","qos":2,"clientId":"c","username":"u"}`,
	}
	for _, c := range good {
		require.NoError(t, ValidateConfig("mqtt", []byte(c)), "config %s should be valid", c)
	}

	bad := []string{
		`{"topic":"t"}`,                                 // no urls
		`{"urls":[],"topic":"t"}`,                       // empty urls
		`{"urls":["  "],"topic":"t"}`,                   // blank url
		`{"urls":["tcp://b"],"topic":""}`,               // empty topic
		`{"urls":["tcp://b"],"topic":"t","qos":3}`,      // qos out of range
		`{"urls":["tcp://b"],"topic":"t","bogus":1}`,    // unknown field (fail-closed)
		`{"urls":["tcp://b"],"topic":"a/${! json() }"}`, // Bloblang interpolation in topic (rejected)
	}
	for _, c := range bad {
		require.Error(t, ValidateConfig("mqtt", []byte(c)), "config %s should be rejected", c)
	}
}

func TestValidateConfigUnsupportedType(t *testing.T) {
	require.ErrorIs(t, ValidateConfig("kafka", []byte(`{}`)), ErrUnsupportedType)
}

// TestBuildOutputMQTT asserts the generated Bento output config maps DeviceChain fields
// onto the right Bento field names and injects the secret as the password.
func TestBuildOutputMQTT(t *testing.T) {
	qos := 1
	cfg := mqttConfig{URLs: []string{"tcp://b:1883"}, Topic: "alerts", QoS: &qos, ClientID: "dc", Username: "u"}
	raw, _ := json.Marshal(cfg)

	out, err := BuildOutput("mqtt", raw, "p4ss")
	require.NoError(t, err)

	// Parse back the JSON (which is what Bento receives as YAML) and check the mapping.
	var parsed map[string]map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	mqtt := parsed["mqtt"]
	require.NotNil(t, mqtt)
	assert.Equal(t, []any{"tcp://b:1883"}, mqtt["urls"])
	assert.Equal(t, "alerts", mqtt["topic"])
	assert.Equal(t, float64(1), mqtt["qos"])
	assert.Equal(t, "dc", mqtt["client_id"])
	assert.Equal(t, "u", mqtt["user"])
	assert.Equal(t, "p4ss", mqtt["password"])
	// A set client_id gets a per-connection nanoid suffix (avoids MQTT session takeover under
	// concurrent sends), and connect_timeout is always bounded.
	assert.Equal(t, "nanoid", mqtt["dynamic_client_id_suffix"])
	assert.Equal(t, "20s", mqtt["connect_timeout"])
}

// TestBuildOutputDefaults confirms qos + connect_timeout are always emitted (deterministic
// delivery semantics + a bounded connect), while credential/user/client_id are omitted when
// unset (no dynamic suffix without a client_id).
func TestBuildOutputDefaults(t *testing.T) {
	out, err := BuildOutput("mqtt", []byte(`{"urls":["tcp://b:1883"],"topic":"t"}`), "")
	require.NoError(t, err)
	var parsed map[string]map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	mqtt := parsed["mqtt"]
	assert.Equal(t, float64(1), mqtt["qos"], "qos defaults to 1, emitted explicitly")
	assert.Equal(t, "20s", mqtt["connect_timeout"])
	_, hasPass := mqtt["password"]
	_, hasUser := mqtt["user"]
	_, hasClientID := mqtt["client_id"]
	_, hasDynSuffix := mqtt["dynamic_client_id_suffix"]
	assert.False(t, hasPass)
	assert.False(t, hasUser)
	assert.False(t, hasClientID)
	assert.False(t, hasDynSuffix, "no dynamic suffix without an authored client_id")
}

// TestBuildOutputSecretInjectionSafe verifies a credential containing YAML/JSON-special
// characters cannot break out of its field (json.Marshal escapes it).
func TestBuildOutputSecretInjectionSafe(t *testing.T) {
	nasty := "p\": {injected: true}, \"x\": \"" // would break naive string interpolation
	out, err := BuildOutput("mqtt", []byte(`{"urls":["tcp://b:1883"],"topic":"t"}`), nasty)
	require.NoError(t, err)
	var parsed map[string]map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, nasty, parsed["mqtt"]["password"])
	_, injected := parsed["mqtt"]["injected"]
	assert.False(t, injected, "a crafted secret must not inject sibling fields")
}

func TestBuildOutputUnsupportedType(t *testing.T) {
	_, err := BuildOutput("kafka", []byte(`{}`), "")
	require.ErrorIs(t, err, ErrUnsupportedType)
}
