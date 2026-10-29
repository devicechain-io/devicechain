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

	// hot swaps key material without a reload, and digest is the config the server runs (hottls.go).
	hot    *hotTLS
	digest string
	// live is the options value the server was last handed (at start, or by our last full reload);
	// the hot path reads the TLS configs in it to check nothing but this binary changed them.
	live *server.Options
	// lastLoad is the server's config load time as of our last reload (or start). A reload from
	// outside this binary moves it (see reloadWith), and the hot path then stands down.
	lastLoad time.Time
	// hotSwaps counts reloads served by the hot path; tests read it.
	hotSwaps atomic.Uint64

	// ldm records that lame duck mode was requested. Upstream's SIGTERM handler reads the
	// server's own (unexported) flag; see signals_unix.go for why this one is equivalent.
	ldm atomic.Bool
}

// prepareOptions is what this binary does to the options before a server is created from them:
// request device certificates on the MQTT listener, and switch off the library's own signal
// handler so that this binary's (signals_unix.go) is the only one. main calls it, and so do the
// tests, so they start a server the way main does.
//
// It returns what newBroker needs to rotate the TLS key material without a reload (hottls.go).
func prepareOptions(opts *server.Options) *hotTLS {
	applyDeviceTLS(opts)
	opts.NoSigs = true
	return wrapOptions(opts)
}

// newBroker takes the options the server was created from. hot is what wrapOptions returned for
// them, or nil, in which case every reload is the full one.
func newBroker(s *server.Server, opts *server.Options, m *brokerMetrics, hot *hotTLS) *broker {
	b := &broker{s: s, configFile: opts.ConfigFile, metrics: m, mqttTLS: opts.MQTT.TLSConfig,
		hot: hot, digest: opts.ConfigDigest(), live: opts}
	// Zero when unknown, which keeps the hot path off: it is allowed only when the load time is
	// known and unmoved.
	b.lastLoad, _ = b.configLoadTime()
	return b
}

// configLoadTime is when the server last loaded its configuration (start, or any reload by anyone).
func (b *broker) configLoadTime() (time.Time, bool) {
	vz, err := b.s.Varz(nil)
	if err != nil || vz.ConfigLoadTime.IsZero() {
		return time.Time{}, false
	}
	return vz.ConfigLoadTime, true
}

// hotAllowed reports whether a key-material-only reload may skip the server. It is false when
// anything but this binary has touched the server's TLS since our last reload:
//
//   - a reload from outside: $SYS.REQ.SERVER.<id>.RELOAD calls Server.Reload itself, which installs
//     raw configs (the wrappers and the certificate request are gone) and moves the config load
//     time. Swapping into the orphaned wrappers would be silent: no error, and every later
//     rotation swallowed until the certificate expired. The system login is held away from that
//     request (applyDeviceTLS), but this does not rely on it;
//   - a hook the server installed on a listener's config since (OCSP stapling and ocsp_peer set
//     GetCertificate / VerifyConnection on the config they apply): the config GetConfigForClient
//     hands out would bypass them.
func (b *broker) hotAllowed() bool {
	if b.hot == nil || b.live == nil || b.lastLoad.IsZero() {
		return false
	}
	if lt, ok := b.configLoadTime(); !ok || !lt.Equal(b.lastLoad) {
		return false
	}
	return liveIsOurs(b.live)
}

// reload is the SIGHUP path. On success the server runs the new options, so the probe follows
// their TLS config; on failure the server kept the old ones, and so does the probe.
//
// A reload that changes only key material (a certificate rotation) is not given to the server at
// all: see hottls.go for why, and for when this falls back to the full reload.
func (b *broker) reload() error { return b.reloadWith(true) }

// reloadFull always hands the server the new options, as upstream's SIGHUP does.
func (b *broker) reloadFull() error { return b.reloadWith(false) }

func (b *broker) reloadWith(allowHot bool) error {
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
	digest := newOpts.ConfigDigest()
	// What the probe follows is the config the listener hands out, not its wrapper.
	next := newOpts.MQTT.TLSConfig
	if allowHot && digest != "" && digest == b.digest && b.hotAllowed() && b.hot.swapIfKeyMaterialOnly(newOpts) {
		b.mqttTLS = next
		b.hotSwaps.Add(1)
		b.s.Noticef("Reloaded server configuration (TLS key material only; no connection was closed)")
		return nil
	}
	// A new options value carries new configs: wrap them, so the next rotation can be hot. Nil when
	// they cannot be (hottls.go says when), and then every reload is the full one.
	hot := wrapOptions(newOpts)
	next = newOpts.MQTT.TLSConfig
	if err := b.s.ReloadOptions(newOpts); err != nil {
		return err
	}
	b.hot, b.digest, b.mqttTLS, b.live = hot, digest, next, newOpts
	b.lastLoad, _ = b.configLoadTime()
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

	b.metrics.certNotAfter.Store(notAfter(tc))
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
