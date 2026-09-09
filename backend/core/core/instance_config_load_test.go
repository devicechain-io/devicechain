// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The instance configuration document is the OPERATOR-FACING one: it is what an operator
// edits through the chart, and its keys are what the documentation tells them to set. It
// was also the one configuration path that did not decode strictly, so a misspelled key
// was discarded and the service started healthy on the built-in default — a setting
// somebody deliberately chose, wrote down and deployed, believed to be in force, and
// available to be trusted in an incident.
//
// These tests go through LoadInstanceConfigurationFrom, the function a service actually
// calls at startup, rather than through LoadConfiguration. That is deliberate: what has
// to be gated is the WIRING. A test written against the loader passes just as happily
// with a plain json.Unmarshal still sitting in the service's load path.

// instanceDoc writes doc to a file and returns its path.
func instanceDoc(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "instance")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
	return path
}

// A minimal document that passes validation, so a test can add one key to it and measure
// only that key. The two blocks here are the ones config.Validate requires.
const minimalInstanceDoc = `{
  "infrastructure": {
    "nats": {"hostname": "dc-nats.dc-system", "port": 4222},
    "userManagement": {"hostname": "user-management", "port": 8080}
  }
}`

// 🔴 THE GATE. A key that is not a field is refused, and the message NAMES it — an error
// that only said "unknown field" would leave the operator to diff a hundred-line document
// against a struct they cannot see.
//
// The cases are nested at three different depths, because that is what this document is
// and because the top-level-only shape of the retirement path made depth the thing most
// likely to be got wrong.
func TestLoadInstanceConfigurationRefusesAMisspelledKey(t *testing.T) {
	for _, tc := range []struct {
		name, doc, wantKey string
	}{
		{
			name: "at the root",
			doc: `{"infrastructure":{"nats":{"hostname":"h","port":4222},
			       "userManagement":{"hostname":"u","port":8080}},"persistance":{}}`,
			wantKey: "persistance",
		},
		{
			name: "one level down",
			doc: `{"infrastructure":{"nats":{"hostname":"h","port":4222},
			       "userManagement":{"hostname":"u","port":8080},"grapqhl":{}}}`,
			wantKey: "grapqhl",
		},
		{
			name: "two levels down",
			doc: `{"infrastructure":{"nats":{"hostname":"h","port":4222},
			       "userManagement":{"hostname":"u","port":8080},
			       "graphql":{"maxSubscriptionMessageBytse":8388608}}}`,
			wantKey: "maxSubscriptionMessageBytse",
		},
		{
			name: "three levels down",
			doc: `{"infrastructure":{"nats":{"hostname":"h","port":4222,"tls":{"enabeld":true}},
			       "userManagement":{"hostname":"u","port":8080}}}`,
			wantKey: "enabeld",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := &Microservice{}
			err := ms.LoadInstanceConfigurationFrom(instanceDoc(t, tc.doc))

			require.Error(t, err, "a key that is not a field must refuse the load, not fall through to the default")
			assert.Contains(t, err.Error(), tc.wantKey,
				"the error must name the key the operator got wrong")
		})
	}
}

// 🔴 THE COUNTERWEIGHT, and the half that makes the gate above safe to have: a
// CORRECTLY spelled key must still arrive, with the value the operator wrote.
//
// It is also the measurement of what the missing strictness cost. Loaded permissively,
// the misspelled document in the case above produced no error and no log line, and the
// subscription frame ceiling this asserts came out at the platform default —
// DefaultGraphQLMaxSubscriptionMessageBytes, 4 MiB — rather than the 8 MiB written. The
// operator's evidence for the larger ceiling being in force was that the pod was healthy.
func TestLoadInstanceConfigurationAppliesACorrectlySpelledKey(t *testing.T) {
	doc := `{"infrastructure":{"nats":{"hostname":"h","port":4222},
	         "userManagement":{"hostname":"u","port":8080},
	         "graphql":{"maxSubscriptionMessageBytes":8388608}}}`

	ms := &Microservice{}
	require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, doc)))

	assert.Equal(t, int64(8388608), ms.InstanceConfiguration.Infrastructure.GraphQL.MaxSubscriptionMessageBytes)
	assert.NotEqual(t, config.DefaultGraphQLMaxSubscriptionMessageBytes,
		ms.InstanceConfiguration.Infrastructure.GraphQL.MaxSubscriptionMessageBytes,
		"the asserted value must differ from the default, or this test cannot tell an "+
			"applied key from a discarded one")
}

// A document with no overrides at all still loads on its defaults, and still validates:
// strictness must not turn an omitted key into a refusal.
func TestLoadInstanceConfigurationDefaultsAnOmittedKey(t *testing.T) {
	ms := &Microservice{}
	require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, minimalInstanceDoc)))

	assert.Equal(t, config.DefaultGraphQLMaxSubscriptionMessageBytes,
		ms.InstanceConfiguration.Infrastructure.GraphQL.MaxSubscriptionMessageBytes)
	assert.Equal(t, config.DefaultStreamMaxBytes,
		ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytes)
}

// The retirement path, on the one key this document has retired. infrastructure.
// metrics.httpPort was shipped as a chart DEFAULT, so every operator who copied
// values.yaml has it, and one running a pre-created instance Secret has it in a document
// the chart cannot edit. Refusing it would stop every pod in the instance on upgrade over
// a value nothing has read for several releases.
//
// It is NESTED, which is the whole reason the retirement path grew a path syntax: matched
// at the top level only, this key would not have matched and the load would have failed
// closed — the outcome that path exists to prevent.
func TestLoadInstanceConfigurationAcceptsTheRetiredMetricsPort(t *testing.T) {
	doc := `{"infrastructure":{"nats":{"hostname":"h","port":4222},
	         "userManagement":{"hostname":"u","port":8080},
	         "metrics":{"enabled":true,"httpPort":9090}}}`

	ms := &Microservice{}
	require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, doc)),
		"a key this project shipped and then removed must warn and load, not stop the pod")
	assert.True(t, ms.InstanceConfiguration.Infrastructure.Metrics.Enabled,
		"the live key beside the retired one must still apply")
}

// Retiring a key must not relax the posture for its neighbours: the same object still
// refuses a typo.
func TestLoadInstanceConfigurationRetirementDoesNotAdmitUnknownKeys(t *testing.T) {
	doc := `{"infrastructure":{"nats":{"hostname":"h","port":4222},
	         "userManagement":{"hostname":"u","port":8080},
	         "metrics":{"httpPort":9090,"enabeld":true}}}`

	ms := &Microservice{}
	err := ms.LoadInstanceConfigurationFrom(instanceDoc(t, doc))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "enabeld")
}

// A bad VALUE is still refused. Strictness is added to the name check, not in place of the
// value checks — each of which was defeated by a mistyped key name falling through to a
// default that refuses nothing.
func TestLoadInstanceConfigurationStillRefusesABadValue(t *testing.T) {
	doc := `{"infrastructure":{"nats":{"hostname":"h","port":4222,"streamReplicas":2},
	         "userManagement":{"hostname":"u","port":8080}}}`

	ms := &Microservice{}
	err := ms.LoadInstanceConfigurationFrom(instanceDoc(t, doc))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "streamReplicas")
}

// 🔴 THE OTHER COUNTERWEIGHT, and the one that matters most: a document that sets EVERY
// key this platform documents must load. A strict decode that refuses a fully-configured
// instance would be found at the worst possible moment, on the deployment that had
// configured the most.
//
// This is the Go-side half. The chart-side half — that the document the chart actually
// RENDERS loads — lives in dcctl's bootstrap tests, which render the real chart through
// the real Helm engine and run this same loader over the result.
func TestLoadInstanceConfigurationAcceptsAFullyPopulatedDocument(t *testing.T) {
	doc := `{
  "infrastructure": {
    "nats": {
      "hostname": "dc-nats.dc-system", "port": 4222, "streamReplicas": 3,
      "streamMaxBytes": 2147483648, "streamMaxBytesCold": 268435456,
      "streamMaxMsgs": 9000000, "streamMaxMsgSize": 2097152,
      "mqttStoreMaxBytes": 536870912, "mqttQoS2StoreMaxBytes": 134217728,
      "kvCacheMaxBytes": 134217728, "kvStateMaxBytes": 268435456,
      "tls": {"enabled": false, "ca": ""},
      "auth": {"user": "dc_service", "password": "p", "sysUser": "dc_sys",
               "sysPassword": "p", "calloutIssuerSeed": "s"}
    },
    "metrics": {"enabled": true},
    "graphql": {"maxSubscriptionMessageBytes": 8388608},
    "shutdown": {"drainSeconds": 10, "terminationGracePeriodSeconds": 60},
    "userManagement": {"hostname": "user-management", "port": 8080},
    "deviceManagement": {"hostname": "device-management", "port": 8080},
    "eventProcessing": {"hostname": "event-processing", "port": 8080},
    "deviceState": {"hostname": "device-state", "port": 8080},
    "commandDelivery": {"hostname": "command-delivery", "port": 8080},
    "aiInference": {"hostname": "ai-inference", "port": 8080},
    "serviceAuth": {"secret": "svc"},
    "secrets": {"backend": "postgres", "kekProvider": "instance",
                "rootKey": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
    "egress": {"allowedDestinations": ["10.42.0.17/32"]},
    "blob": {"backend": "s3", "directory": "", "bucket": "b", "region": "r",
             "endpoint": "https://minio.example.com", "usePathStyle": true}
  },
  "persistence": {
    "rdb": {"type": "postgres95", "configuration": {"hostname": "h", "port": 5432,
            "maxConnections": 20, "username": "u", "password": "p"}},
    "tsdb": {"type": "timescaledb", "configuration": {"hostname": "h", "port": 5432,
             "maxConnections": 20, "username": "u", "password": "p"}}
  }
}`

	ms := &Microservice{}
	require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, doc)),
		"every key this platform documents must survive a strict decode")

	// Spot-check one value from each of the two top-level sections, so a document that
	// loaded but bound nothing would not pass.
	infra := ms.InstanceConfiguration.Infrastructure
	assert.Equal(t, "ai-inference", infra.AiInference.Hostname)
	assert.Equal(t, []string{"10.42.0.17/32"}, infra.Egress.AllowedDestinations)
	assert.Equal(t, "timescaledb", ms.InstanceConfiguration.Persistence.Tsdb.Type)
}
