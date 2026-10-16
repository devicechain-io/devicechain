// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolution.workers loads as written, and a document that omits it gets 10.
func TestResolutionWorkersLoadAndDefault(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"resolution":{"workers":7}}`), cfg))
	assert.Equal(t, 7, cfg.Resolution.Workers)

	cfg = &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.Equal(t, 10, cfg.Resolution.Workers)
	assert.Equal(t, 10, NewDeviceManagementConfiguration().Resolution.Workers)
	assert.Equal(t, 10, DefaultResolutionWorkers)
}

// resolution.workers is bounded by the relational pool the resolvers draw from — 20 unless
// rdbConfiguration sets one — and an out-of-range value stops the service, naming the
// setting. The edges are exact: the pool size is refused, one below it is accepted.
func TestResolutionWorkersAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name, doc, wantErr string
	}{
		{"negative", `{"resolution":{"workers":-1}}`, "resolution.workers must be at least 1, got -1"},
		{"at the default pool", `{"resolution":{"workers":20}}`,
			"resolution.workers is 20, but the connection pool holds 20; keep it below 20 so reads are not starved"},
		{"below the default pool", `{"resolution":{"workers":19}}`, ""},
		{"at a configured pool", `{"rdbConfiguration":{"maxOpenConnections":8},"resolution":{"workers":8}}`,
			"resolution.workers is 8, but the connection pool holds 8"},
		{"below a configured pool", `{"rdbConfiguration":{"maxOpenConnections":8},"resolution":{"workers":7}}`, ""},
		// The default is refused on a pool of 10 or fewer, which is what the upgrade note tells
		// an operator who set one.
		{"the default on a pool of 10", `{"rdbConfiguration":{"maxOpenConnections":10}}`,
			"resolution.workers is 10, but the connection pool holds 10"},
		{"the default on a pool of 11", `{"rdbConfiguration":{"maxOpenConnections":11}}`, ""},
		{"a misspelled key", `{"resolution":{"resolvers":8}}`, `unknown field "resolvers"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := core.LoadConfiguration([]byte(tc.doc), &DeviceManagementConfiguration{})
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}

// The default is quiet on the default pool: 10 of 20 is not "more than half", so an
// operator who changed nothing sees no warning. A default above 10 would log here. The
// counterweight shows the same capture does see the warning, so an empty capture cannot pass
// for a quiet one.
func TestTheDefaultResolutionWorkersDoNotWarnOnTheDefaultPool(t *testing.T) {
	const warning = "More than half the connection pool"

	for _, tc := range []struct {
		doc    string
		logged bool
	}{{``, false}, {`{"resolution":{"workers":11}}`, true}} {
		t.Run(tc.doc, func(t *testing.T) {
			logs := logSink.Capture(t)
			require.NoError(t, core.LoadConfiguration([]byte(tc.doc), &DeviceManagementConfiguration{}))
			if got := strings.Contains(logs.String(), warning); got != tc.logged {
				t.Errorf("document %q: logged the warning = %v, want %v (log: %q)", tc.doc, got, tc.logged, logs.String())
			}
		})
	}
}

// The default publish window covers a replicated PubAck at the rate the platform is sized for.
//
// A window caps throughput at window / PubAck latency (Little's law). Measured on a three-node
// cluster at saturation, the mean resolved-events PubAck took 11.4 ms; the rate to leave
// headroom for is 25,000 events a second. 25,000 x 0.0114 = 285 publishes in flight, so a
// default below that makes the window, not the broker, the limit on a replicated install.
// The 128 this replaced allowed 11,200 a second, and that is the ceiling the cluster measured.
func TestDefaultPublishWindowCoversReplicatedPubAck(t *testing.T) {
	const targetPerSecond = 25_000
	const replicatedPubAck = 11400 * time.Microsecond
	need := int(math.Ceil(targetPerSecond * replicatedPubAck.Seconds()))
	require.Equal(t, 285, need)
	assert.GreaterOrEqual(t, DefaultPublishWindow, need,
		"the default publish window caps one pod at %d events a second against a replicated PubAck of %s; %d are needed for %d",
		int(float64(DefaultPublishWindow)/replicatedPubAck.Seconds()), replicatedPubAck, need, targetPerSecond)
	assert.Equal(t, 512, DefaultPublishWindow)
	assert.LessOrEqual(t, DefaultPublishWindow, messaging.MaxOrderedWindow)
}

// resolution.publishWindow loads as written, and a document that omits it gets the default.
func TestResolutionPublishWindowLoadAndDefault(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"resolution":{"publishWindow":300}}`), cfg))
	assert.Equal(t, 300, cfg.Resolution.PublishWindowSize())

	cfg = &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.Equal(t, DefaultPublishWindow, cfg.Resolution.PublishWindowSize())
	require.NotNil(t, cfg.Resolution.PublishWindow)

	cfg = &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"resolution":{"workers":4}}`), cfg))
	assert.Equal(t, DefaultPublishWindow, cfg.Resolution.PublishWindowSize())
	assert.Equal(t, DefaultPublishWindow, *NewDeviceManagementConfiguration().Resolution.PublishWindow)
}

// resolution.publishWindow outside 1..1024 stops the service, naming the setting and the
// value. It is refused, never clamped, and an explicit 0 is refused rather than read as unset.
// The edges are exact.
func TestResolutionPublishWindowIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name, doc string
		ok        bool
	}{
		{"zero", `{"resolution":{"publishWindow":0}}`, false},
		{"negative", `{"resolution":{"publishWindow":-1}}`, false},
		{"one above the maximum", `{"resolution":{"publishWindow":1025}}`, false},
		{"the minimum", `{"resolution":{"publishWindow":1}}`, true},
		{"the maximum", `{"resolution":{"publishWindow":1024}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &DeviceManagementConfiguration{}
			err := core.LoadConfiguration([]byte(tc.doc), cfg)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "resolution.publishWindow must be between 1 and 1024")
		})
	}
}

// resolution.inboundReaders loads as written, and a document that omits it gets 1.
func TestResolutionInboundReadersLoadAndDefault(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"resolution":{"inboundReaders":3}}`), cfg))
	assert.Equal(t, 3, cfg.Resolution.InboundReaderCount())

	cfg = &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.Equal(t, 1, cfg.Resolution.InboundReaderCount())
	require.NotNil(t, cfg.Resolution.InboundReaders)

	cfg = &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"resolution":{"workers":4}}`), cfg))
	assert.Equal(t, 1, cfg.Resolution.InboundReaderCount())
	assert.Equal(t, 1, *NewDeviceManagementConfiguration().Resolution.InboundReaders)
	assert.Equal(t, 1, DefaultInboundReaders)
	assert.Equal(t, 4, MaxInboundReaders)
}

// resolution.inboundReaders outside 1..4 stops the service, naming the setting and the value.
// It is refused, never clamped, and an explicit 0 is refused rather than read as unset. The
// edges are exact.
func TestResolutionInboundReadersAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name, doc string
		ok        bool
	}{
		{"zero", `{"resolution":{"inboundReaders":0}}`, false},
		{"negative", `{"resolution":{"inboundReaders":-1}}`, false},
		{"one above the maximum", `{"resolution":{"inboundReaders":5}}`, false},
		{"the minimum", `{"resolution":{"inboundReaders":1}}`, true},
		{"the maximum", `{"resolution":{"inboundReaders":4}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &DeviceManagementConfiguration{}
			err := core.LoadConfiguration([]byte(tc.doc), cfg)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "resolution.inboundReaders must be between 1 and 4")
		})
	}
}
