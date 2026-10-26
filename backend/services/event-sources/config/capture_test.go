// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"math"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The default capture publish window covers a replicated PubAck at the rate the platform is
// sized for.
//
// A window caps throughput at window / PubAck latency (Little's law). Measured on a three-node
// cluster at saturation, the mean PubAck took 11.4 ms; the rate to leave headroom for is
// 25,000 events a second. 25,000 x 0.0114 = 285 publishes in flight, so a default below that
// makes the window, not the broker, the limit on a replicated install. The 128 this replaced
// allowed 11,200 a second, which is the ceiling the cluster measured.
//
// Under load the PubAck that binds is far slower than that idle figure: a 12,000/s MQTT run
// on the same kind of cluster settled at a mean of about 72 ms, where 512 caps a pod at about
// 7,100 a second. The default is 1024 for that; see DefaultCapturePublishWindow.
func TestDefaultCapturePublishWindowCoversReplicatedPubAck(t *testing.T) {
	const targetPerSecond = 25_000
	const replicatedPubAck = 11400 * time.Microsecond
	need := int(math.Ceil(targetPerSecond * replicatedPubAck.Seconds()))
	require.Equal(t, 285, need)
	assert.GreaterOrEqual(t, DefaultCapturePublishWindow, need,
		"the default capture publish window caps one pod at %d events a second against a replicated PubAck of %s; %d are needed for %d",
		int(float64(DefaultCapturePublishWindow)/replicatedPubAck.Seconds()), replicatedPubAck, need, targetPerSecond)
	assert.Equal(t, 1024, DefaultCapturePublishWindow)
	assert.LessOrEqual(t, DefaultCapturePublishWindow, messaging.MaxOrderedWindow)
}

// capture.publishWindow loads as written, and a document that omits it, or writes null, gets
// the default.
func TestCapturePublishWindowLoadAndDefault(t *testing.T) {
	cfg := &EventSourcesConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"capture":{"publishWindow":300}}`), cfg))
	assert.Equal(t, 300, cfg.Capture.PublishWindowSize())

	for name, doc := range map[string]string{
		"empty":       ``,
		"absent":      `{"contention":{"manualFloor":1}}`,
		"null":        `{"capture":{"publishWindow":null}}`,
		"empty block": `{"capture":{}}`,
		"nullblock":   `{"capture":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := &EventSourcesConfiguration{}
			require.NoError(t, core.LoadConfiguration([]byte(doc), cfg))
			assert.Equal(t, DefaultCapturePublishWindow, cfg.Capture.PublishWindowSize())
			require.NotNil(t, cfg.Capture.PublishWindow, "after ApplyDefaults it is never nil")
		})
	}
	assert.Equal(t, DefaultCapturePublishWindow, *NewEventSourcesConfiguration().Capture.PublishWindow)
}

// capture.publishWindow outside 1..1024 stops the service, naming the setting and the value.
// It is refused, never clamped, and an explicit 0 is refused rather than read as unset. The
// edges are exact.
func TestCapturePublishWindowIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name, doc string
		ok        bool
	}{
		{"zero", `{"capture":{"publishWindow":0}}`, false},
		{"negative", `{"capture":{"publishWindow":-1}}`, false},
		{"one above the maximum", `{"capture":{"publishWindow":1025}}`, false},
		{"the minimum", `{"capture":{"publishWindow":1}}`, true},
		{"the maximum", `{"capture":{"publishWindow":1024}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &EventSourcesConfiguration{}
			err := core.LoadConfiguration([]byte(tc.doc), cfg)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "capture.publishWindow must be between 1 and 1024")
		})
	}
}

// capture.bodyCredential was REMOVED in v0.26.0 (a body credential on the platform broker is
// always refused). It is not a retired key that is stripped with a warning: stripping it would
// start an operator who still set "strip" on a refusal they did not choose, quietly, so the load
// fails closed on it instead, whatever the value. If this ever loads, the strip has come back.
func TestCaptureBodyCredentialIsRejectedAtLoad(t *testing.T) {
	for _, v := range []string{"strip", "refuse", ""} {
		cfg := &EventSourcesConfiguration{}
		err := core.LoadConfiguration([]byte(`{"capture":{"bodyCredential":"`+v+`"}}`), cfg)
		require.Error(t, err, v)
		assert.Contains(t, err.Error(), "bodyCredential")
	}
}
