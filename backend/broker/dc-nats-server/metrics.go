// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"

	"github.com/nats-io/nats-server/v2/server"
)

// metricsAddrEnv names the listen address of this binary's own metrics endpoint. An environment
// variable rather than a flag so that the command line stays exactly upstream's: every flag
// nats-server accepts means the same thing here, and nothing else is accepted.
const metricsAddrEnv = "DC_BROKER_METRICS_ADDR"

// defaultMetricsAddr is on by default, deliberately: the gauge below is the watcher for a failure
// that is otherwise silent, and a watcher that has to be switched on is one that is not there on
// the instance where it was needed. The nats chart's exporter sidecar holds 7777.
const defaultMetricsAddr = ":7778"

// brokerMetrics is the handful of series this binary exports about itself. Hand-written in the
// text exposition format rather than through a client library: four series do not justify a
// dependency in the broker's own binary, which is on every message's path.
type brokerMetrics struct {
	// mqttTLS is 1 when the MQTT listener terminates TLS. Without TLS no certificate can be
	// requested, and that is a deployment choice rather than the failure below, so the alert
	// conditions on this.
	mqttTLS atomic.Int32
	// clientAuthRequest is 1 when the last probe saw the MQTT listener send a CertificateRequest.
	// 0 on a TLS listener is the reload trap having fired — or a probe that could not tell, which
	// is reported the same way on purpose: the device certificate path cannot be confirmed.
	clientAuthRequest atomic.Int32
	// certNotAfter is the expiry (Unix seconds) of the MQTT certificate the listener hands out now;
	// 0 without MQTT TLS. The server's own /varz tls_cert_not_after is stale after a hot swap.
	certNotAfter atomic.Int64
	probeErrors  atomic.Uint64
	probes       atomic.Uint64
	build        string
}

func newBrokerMetrics() *brokerMetrics {
	return &brokerMetrics{build: buildStamp()}
}

// buildStamp identifies the binary by the Go toolchain that built it, and by nothing that moves
// when the binary does not. A source revision is deliberately absent: the image is built with
// -buildvcs=false so that two releases whose broker did not change produce the same image digest
// and do not roll the broker, and a revision here (or nats-server's git_commit, which the library
// reads from the same build info) would make every commit a new image.
func buildStamp() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		return bi.GoVersion
	}
	return "unknown"
}

// labelValue escapes a Prometheus label value (backslash, double quote, newline).
func labelValue(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

func (m *brokerMetrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP dc_broker_info The DeviceChain broker build: the nats-server library it is built on and the Go toolchain that built it.\n")
	fmt.Fprintf(w, "# TYPE dc_broker_info gauge\n")
	fmt.Fprintf(w, "dc_broker_info{library_version=\"%s\",build=\"%s\"} 1\n", labelValue(server.VERSION), labelValue(m.build))
	fmt.Fprintf(w, "# HELP dc_broker_mqtt_tls 1 when the MQTT listener terminates TLS.\n")
	fmt.Fprintf(w, "# TYPE dc_broker_mqtt_tls gauge\n")
	fmt.Fprintf(w, "dc_broker_mqtt_tls %d\n", m.mqttTLS.Load())
	fmt.Fprintf(w, "# HELP dc_broker_mqtt_client_auth_request 1 when the MQTT listener was last seen requesting a client certificate in its TLS handshake.\n")
	fmt.Fprintf(w, "# TYPE dc_broker_mqtt_client_auth_request gauge\n")
	fmt.Fprintf(w, "dc_broker_mqtt_client_auth_request %d\n", m.clientAuthRequest.Load())
	fmt.Fprintf(w, "# HELP dc_broker_mqtt_tls_cert_not_after_seconds Expiry, in Unix seconds, of the certificate the MQTT listener presents to new connections (0 without TLS). /varz tls_cert_not_after goes stale after a certificate rotation.\n")
	fmt.Fprintf(w, "# TYPE dc_broker_mqtt_tls_cert_not_after_seconds gauge\n")
	fmt.Fprintf(w, "dc_broker_mqtt_tls_cert_not_after_seconds %d\n", m.certNotAfter.Load())
	fmt.Fprintf(w, "# HELP dc_broker_mqtt_client_auth_probes_total Handshakes the broker made with its own MQTT listener to check it requests client certificates.\n")
	fmt.Fprintf(w, "# TYPE dc_broker_mqtt_client_auth_probes_total counter\n")
	fmt.Fprintf(w, "dc_broker_mqtt_client_auth_probes_total %d\n", m.probes.Load())
	fmt.Fprintf(w, "# HELP dc_broker_mqtt_client_auth_probe_errors_total Probes that could not complete a handshake, so could not tell.\n")
	fmt.Fprintf(w, "# TYPE dc_broker_mqtt_client_auth_probe_errors_total counter\n")
	fmt.Fprintf(w, "dc_broker_mqtt_client_auth_probe_errors_total %d\n", m.probeErrors.Load())
}

// notAfter is the expiry of the first certificate of tc, in Unix seconds; 0 when there is none.
func notAfter(tc *tls.Config) int64 {
	if tc == nil || len(tc.Certificates) == 0 || len(tc.Certificates[0].Certificate) == 0 {
		return 0
	}
	leaf, err := x509.ParseCertificate(tc.Certificates[0].Certificate[0])
	if err != nil {
		return 0
	}
	return leaf.NotAfter.Unix()
}
