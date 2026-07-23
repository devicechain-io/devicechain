// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// edgeMetrics is the agent's observability surface (E3): a Prometheus registry plus a
// loopback HTTP server exposing /metrics and /healthz.
//
// The registry is PER-AGENT (not the platform's default global registry). The platform
// registers via promauto against the default registry because each service is one
// process with one Microservice; the edge agent's tests, by contrast, construct many
// agents in one process AND restart an agent within a single test, which would panic on
// the default registry ("duplicate metrics collector registration"). A custom registry
// per agent is also the honest shape for a standalone binary that owns its own registry
// and is scraped alone, not on a shared mux.
//
// Counters are CounterFuncs reading the agent's existing atomics (single source of
// truth — no dual-increment drift); gauges derived from stream state are Set on the
// 30s sample tick (sampleMetrics); uplink connectivity is a GaugeFunc read live on
// scrape. Metric names mirror core/messaging/stream_metrics.go so edge dashboards read
// like platform ones.
type edgeMetrics struct {
	reg *prometheus.Registry

	// Set on the sample tick from StreamInfo().State.
	spoolUsedBytes        prometheus.Gauge
	spoolLimitBytes       prometheus.Gauge
	spoolUsedMessages     prometheus.Gauge
	spoolOldestAgeSeconds prometheus.Gauge

	// addr is the resolved bound address (populated once the listener is up); empty
	// when metrics are disabled. Read by tests to scrape an ephemeral port.
	srv  *http.Server
	ln   net.Listener
	addr string
}

const (
	metricsNamespace = "devicechain"
	metricsSubsystem = "edge"
)

// newEdgeMetrics builds the agent's registry and registers every collector. Counter and
// gauge-func collectors close over the agent's live state, so this must be called after
// the atomics exist (they are zero-valued fields on Agent, so any time after New).
func newEdgeMetrics(a *Agent) *edgeMetrics {
	reg := prometheus.NewRegistry()
	// Go runtime + process collectors so an edge-box dashboard sees goroutines / RSS /
	// fds alongside the agent's own metrics (a custom registry has none by default).
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	auto := promauto.With(reg)
	gauge := func(name, help string) prometheus.Gauge {
		return auto.NewGauge(prometheus.GaugeOpts{
			Namespace: metricsNamespace, Subsystem: metricsSubsystem, Name: name, Help: help,
		})
	}
	counterFunc := func(name, help string, fn func() float64) {
		auto.NewCounterFunc(prometheus.CounterOpts{
			Namespace: metricsNamespace, Subsystem: metricsSubsystem, Name: name, Help: help,
		}, fn)
	}

	m := &edgeMetrics{
		reg:                   reg,
		spoolUsedBytes:        gauge("spool_used_bytes", "Bytes currently held in the durable capture spool."),
		spoolLimitBytes:       gauge("spool_limit_bytes", "Configured maximum size of the durable capture spool (spoolMaxBytes)."),
		spoolUsedMessages:     gauge("spool_used_messages", "Events currently buffered in the capture spool (captured but not yet forwarded)."),
		spoolOldestAgeSeconds: gauge("spool_oldest_age_seconds", "Age in seconds of the oldest buffered event; the primary backlog signal."),
	}

	// Counters over the existing atomics.
	counterFunc("received_total", "Events read from the capture spool for forwarding.", func() float64 { return float64(a.received.Load()) })
	counterFunc("forwarded_total", "Events acknowledged by the cloud uplink.", func() float64 { return float64(a.forwarded.Load()) })
	counterFunc("forward_errors_total", "Forward attempts that failed (event left buffered for redelivery).", func() float64 { return float64(a.forwardErrors.Load()) })
	counterFunc("malformed_total", "Events dropped for a malformed subject (unforwardable garbage).", func() float64 { return float64(a.malformed.Load()) })
	counterFunc("instance_mismatched_total", "Device publishes seen on a different instanceId than configured (not forwarded).", func() float64 { return float64(a.mismatched.Load()) })
	// dropped_total: overflow evictions (oldest buffered events dropped to stay within
	// spoolMaxBytes). Set on the sample tick from (FirstSeq-1)-ackedCount; monotonic and
	// restart-safe because both operands are durable (stream FirstSeq + our acked-count
	// token), so unlike the other counters it does NOT reset to 0 across a restart.
	counterFunc("dropped_total", "Oldest buffered events dropped by the ring buffer to stay within spoolMaxBytes.", func() float64 {
		d := a.droppedTotal.Load()
		if d < 0 {
			d = 0
		}
		return float64(d)
	})
	// uplink_connected as a GaugeFunc so a scrape sees live connectivity, not a 30s-stale
	// sample.
	auto.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: metricsNamespace, Subsystem: metricsSubsystem, Name: "uplink_connected",
		Help: "1 when the cloud uplink is connected, 0 otherwise.",
	}, func() float64 {
		if a.uplink.Connected() {
			return 1
		}
		return 0
	})

	return m
}

// start binds the loopback endpoint and serves /metrics + /healthz until ctx ends. A
// bind failure is fail-closed (returned as an error): a monitoring endpoint that
// silently failed to start is invisible blindness, and the config posture is fail-loud.
// listenAddr is 127.0.0.1:<port>; a test seam may pass 127.0.0.1:0 for an ephemeral
// port, read back via addr after start returns.
func (m *edgeMetrics) start(listenAddr string, healthz http.HandlerFunc) error {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("bind metrics endpoint %q: %w", listenAddr, err)
	}
	m.ln = ln
	m.addr = ln.Addr().String()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", healthz)
	m.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		// Serve returns ErrServerClosed on a clean Shutdown — not a fault.
		if err := m.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// The server goroutine can't return an error to Run; a mid-life serve
			// failure on a loopback port is exotic (the bind already succeeded), so
			// there is nothing to do but let the next scrape fail visibly.
			_ = err
		}
	}()
	return nil
}

// stop gracefully shuts the endpoint down, releasing the port before Run returns (a
// restart on the same fixed port would otherwise hit EADDRINUSE).
func (m *edgeMetrics) stop() {
	if m.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = m.srv.Shutdown(ctx)
}
