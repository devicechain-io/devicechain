// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

// probeInterval is how often the listener is re-checked between reloads. A reload re-checks at
// once; the interval is what catches a change that did not come through a reload this binary
// performed.
const probeInterval = time.Minute

var errNoConfigFile = errors.New("can only reload config when a file is provided using -c or --config")

// broker is the running server plus what this binary adds to it: the reload that keeps the device
// TLS setting, and the probe and metrics that watch it.
type broker struct {
	s          *server.Server
	configFile string
	metrics    *brokerMetrics

	// mu serializes reloads and probes, so a probe never reads the TLS config of a reload that
	// is half applied.
	mu sync.Mutex
	// mqttTLS is the MQTT TLS config the server was last handed: at start, and after each
	// reload that succeeded. The probe trusts the certificate it serves.
	mqttTLS *tls.Config

	// ldm records that lame duck mode was requested. Upstream's SIGTERM handler reads the
	// server's own (unexported) flag; see signals_unix.go for why this one is equivalent.
	ldm atomic.Bool
}

// prepareOptions is what this binary does to the options before a server is created from them:
// request device certificates on the MQTT listener, and switch off the library's own signal
// handler so that this binary's (signals_unix.go) is the only one. main calls it, and so do the
// tests, so they start a server the way main does.
func prepareOptions(opts *server.Options) {
	applyDeviceTLS(opts)
	opts.NoSigs = true
}

func newBroker(s *server.Server, opts *server.Options, m *brokerMetrics) *broker {
	return &broker{s: s, configFile: opts.ConfigFile, metrics: m, mqttTLS: opts.MQTT.TLSConfig}
}

// reload is the SIGHUP path. On success the server runs the new options, so the probe follows
// their TLS config; on failure the server kept the old ones, and so does the probe.
func (b *broker) reload() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.configFile == "" {
		return errNoConfigFile
	}
	newOpts, err := server.ProcessConfigFile(b.configFile)
	if err != nil {
		return err
	}
	applyDeviceTLS(newOpts)
	if err := b.s.ReloadOptions(newOpts); err != nil {
		return err
	}
	b.mqttTLS = newOpts.MQTT.TLSConfig
	return nil
}

// check probes the listener and publishes what it saw.
//
// Nothing is checked once a lame duck shutdown has begun: the listeners are closing by design, and
// a failed probe then would be noise.
func (b *broker) check() {
	if b.ldm.Load() {
		return
	}
	b.mu.Lock()
	tc := b.mqttTLS
	b.mu.Unlock()

	b.metrics.probes.Add(1)
	requested, err := probeClientAuth(b.s, tc)
	switch {
	case errors.Is(err, errNoMQTTTLS):
		b.metrics.mqttTLS.Store(0)
		b.metrics.clientAuthRequest.Store(0)
		return
	case err != nil:
		b.metrics.probeErrors.Add(1)
		b.s.Warnf("Device certificate probe could not complete: %v", err)
		requested = false
	case !requested:
		b.s.Errorf("The MQTT listener is not requesting client certificates: devices that authenticate by certificate cannot connect")
	}
	b.metrics.mqttTLS.Store(1)
	if requested {
		b.metrics.clientAuthRequest.Store(1)
	} else {
		b.metrics.clientAuthRequest.Store(0)
	}
}

// serveMetrics starts the metrics endpoint on an already-bound listener.
func (b *broker) serveMetrics(ln net.Listener) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", b.metrics)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			b.s.Errorf("Broker metrics endpoint stopped: %v", err)
		}
	}()
}

// watch checks the listener once the server is ready for connections (which on a broker
// recovering JetStream state can take minutes), then every probeInterval until it shuts down.
func (b *broker) watch() {
	for !b.s.ReadyForConnections(probeInterval) {
		if !b.s.Running() {
			return
		}
	}
	b.check()
	t := time.NewTicker(probeInterval)
	defer t.Stop()
	for range t.C {
		if !b.s.Running() {
			return
		}
		b.check()
	}
}
