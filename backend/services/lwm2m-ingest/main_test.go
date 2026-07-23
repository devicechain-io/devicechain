// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-lwm2m-ingest/config"
	"github.com/devicechain-io/dc-microservice/core"
)

// TestLoadConfigurationParsesRenderedChartDocument pins the chart↔service seam. The
// Helm chart renders this exact JSON into the mounted config document; the service
// parses it with DisallowUnknownFields, so a single key the struct does not name (a
// chart/struct drift) is a startup crash-loop, invisible to a test that hand-builds the
// struct. This feeds the literal rendered bytes through the real loader.
func TestLoadConfigurationParsesRenderedChartDocument(t *testing.T) {
	rendered := `{"listen":{"host":"0.0.0.0","port":5684},"security":{"connectionIdLength":8,"handshakeTimeoutSeconds":10,"idleTimeoutSeconds":0,"maxSessions":50000,"identities":[{"identity":"dev-1","pskEnv":"DC_LWM2M_PSK_DEV1","tenant":"acme","externalId":"plant-a/sensor-1","deviceTypeToken":"sensor","autoRegister":true}]}}`

	var cfg config.Lwm2mConfiguration
	require.NoError(t, core.LoadConfiguration([]byte(rendered), &cfg))
	assert.Equal(t, "0.0.0.0", cfg.Listen.Host)
	assert.Equal(t, 5684, cfg.Listen.Port)
	require.NotNil(t, cfg.Security.ConnectionIdLength)
	assert.Equal(t, 8, *cfg.Security.ConnectionIdLength)
	assert.Equal(t, 50000, cfg.Security.MaxSessions)
	require.Len(t, cfg.Security.Identities, 1)
	assert.Equal(t, "dev-1", cfg.Security.Identities[0].Identity)
	assert.Equal(t, "DC_LWM2M_PSK_DEV1", cfg.Security.Identities[0].PskEnv)
	assert.Equal(t, "acme", cfg.Security.Identities[0].Tenant)
	assert.Equal(t, "plant-a/sensor-1", cfg.Security.Identities[0].ExternalId)
	assert.True(t, cfg.Security.Identities[0].AutoRegister)
}

// The default (empty) configuration must load and validate — a bare render with no
// identities is a valid inert deployment.
func TestEmptyConfigurationIsValid(t *testing.T) {
	var cfg config.Lwm2mConfiguration
	require.NoError(t, core.LoadConfiguration([]byte(`{}`), &cfg))
	assert.Equal(t, config.DefaultListenPort, cfg.Listen.Port)
}

// fakeServe is a serveServer whose Serve() blocks until Stop() (or an explicit die) unblocks it,
// recording how many times Stop was called. A test drives the serve-intent supervision without a
// real DTLS transport.
type fakeServe struct {
	release  chan struct{}
	stops    atomic.Int32
	stopOnce sync.Once
}

func newFakeServe() *fakeServe { return &fakeServe{release: make(chan struct{})} }

func (f *fakeServe) Serve() error { <-f.release; return nil }

func (f *fakeServe) Stop() {
	f.stops.Add(1)
	f.stopOnce.Do(func() { close(f.release) })
}

// die makes a blocked Serve() return WITHOUT a Stop — the unexpected-transport-death path.
func (f *fakeServe) die() { f.stopOnce.Do(func() { close(f.release) }) }

// TestSuperviseServeGracefulStopDoesNotFatal is the L3a serve-intent guard (MUST-FIX #1): an
// intended stop (a leadership eviction) does NOT trigger the fatal callback, even though Serve
// returns. This is the shape that replaces the process-global "stopping" flag with per-serve-term
// intent, so a lease blip stops the socket without killing the pod. stopServing blocks until the
// serve goroutine has unwound, so the zero death count is deterministic (the assert happens-after
// the goroutine's intent check). It pins "an intended stop never fatals"; the store-BEFORE-Stop
// ordering that guarantees this in production (where a real Serve does not return instantly) is a
// correctness argument in the code comment, not something this fast fake can race-pin on its own.
func TestSuperviseServeGracefulStopDoesNotFatal(t *testing.T) {
	f := newFakeServe()
	var deaths atomic.Int32
	stopServing := superviseServe(f, func(error) { deaths.Add(1) })

	stopServing() // records intent, Stops, waits for the serve goroutine to observe it

	assert.Equal(t, int32(0), deaths.Load(), "a graceful (intended) Stop must NOT trigger the fatal path")
	assert.Equal(t, int32(1), f.stops.Load(), "stopServing must Stop the transport exactly once")

	stopServing() // idempotent: a second call does nothing
	assert.Equal(t, int32(1), f.stops.Load(), "stopServing is idempotent")
}

// TestSuperviseServeUnexpectedDeathFatals is the counterweight: when Serve returns while the term
// still intends to serve (a socket death on the single serving replica), the fatal callback DOES
// fire — the total-ingest-outage guard that a Ready pod would otherwise hide — and it is handed the
// Serve error so the production fatal log names the cause.
func TestSuperviseServeUnexpectedDeathFatals(t *testing.T) {
	f := newFakeServe()
	died := make(chan error, 1)
	superviseServe(f, func(err error) { died <- err })

	f.die() // Serve returns with no intent recorded → the unexpected-death path

	select {
	case <-died:
	case <-time.After(2 * time.Second):
		t.Fatal("an unexpected Serve return must trigger the fatal path")
	}
}
