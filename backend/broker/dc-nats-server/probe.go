// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

// probeTimeout bounds one probe handshake. The listener is local, so anything near this is a
// broker in trouble, which the probe reports as an error rather than as a verdict.
const probeTimeout = 5 * time.Second

// errNoMQTTTLS is a probe outcome, not a failure: a broker whose MQTT listener does not terminate
// TLS cannot request a certificate, and says so through dc_broker_mqtt_tls = 0.
var errNoMQTTTLS = errors.New("the MQTT listener does not terminate TLS")

// probeClientAuth dials the broker's own MQTT listener and reports whether the TLS handshake
// carried a CertificateRequest — the one fact the device certificate path depends on, read off
// the wire rather than off the options this binary believes it set.
//
// 🔴 WHY THE WIRE AND NOT THE OPTIONS. applyDeviceTLS writes a field the server later reads; a
// gauge computed from that field would report what this binary did, not what the listener does,
// and the failure it exists to catch (a reload path that rebuilds the TLS config and skips the
// hook, here or in a future library) is precisely one in which the two differ. The client's
// GetClientCertificate callback is invoked if and only if the server sent a CertificateRequest,
// in TLS 1.2 and 1.3 alike, so it is an observation of the handshake itself.
//
// The probe trusts exactly the certificate the listener is configured to serve (tc is the MQTT
// TLS config the server was last handed): it is a self-check, so that certificate is the only
// identity it should accept, and it needs no CA. It presents no certificate and sends no MQTT
// packet; the broker sees a connection that closes before CONNECT and never reaches
// authentication or the callout.
func probeClientAuth(s *server.Server, tc *tls.Config) (bool, error) {
	if tc == nil {
		return false, errNoMQTTTLS
	}
	addr, err := mqttProbeAddr(s)
	if err != nil {
		return false, err
	}
	roots, name, err := selfTrust(tc)
	if err != nil {
		return false, err
	}
	requested := false
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: probeTimeout}, "tcp", addr, &tls.Config{
		RootCAs:    roots,
		ServerName: name,
		MinVersion: tls.VersionTLS12,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			requested = true
			// An empty certificate is what a token or password device sends: the answer
			// that must keep working.
			return &tls.Certificate{}, nil
		},
	})
	if err != nil {
		return false, fmt.Errorf("probe handshake with %s: %w", addr, err)
	}
	_ = conn.Close()
	return requested, nil
}

// mqttProbeAddr is where the probe dials: the MQTT port the server actually bound (Varz reports
// the bound port even when the config asked for a random one), on loopback when the listener is
// bound to every interface.
func mqttProbeAddr(s *server.Server) (string, error) {
	vz, err := s.Varz(nil)
	if err != nil {
		return "", fmt.Errorf("read varz: %w", err)
	}
	if vz.MQTT.Port <= 0 {
		return "", fmt.Errorf("the broker reports no bound MQTT port (%d)", vz.MQTT.Port)
	}
	host := vz.MQTT.Host
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(vz.MQTT.Port)), nil
}

// selfTrust returns a pool holding the listener's own leaf and a name that leaf is valid for.
// crypto/x509 accepts a chain of length one when the leaf itself is in the root pool.
func selfTrust(tc *tls.Config) (*x509.CertPool, string, error) {
	if len(tc.Certificates) == 0 || len(tc.Certificates[0].Certificate) == 0 {
		return nil, "", errors.New("the MQTT TLS config carries no static certificate to probe against")
	}
	leaf, err := x509.ParseCertificate(tc.Certificates[0].Certificate[0])
	if err != nil {
		return nil, "", fmt.Errorf("parse the MQTT listener's certificate: %w", err)
	}
	var name string
	switch {
	case len(leaf.DNSNames) > 0:
		name = leaf.DNSNames[0]
	case len(leaf.IPAddresses) > 0:
		name = leaf.IPAddresses[0].String()
	default:
		return nil, "", errors.New("the MQTT listener's certificate names no DNS or IP subject alternative name to verify against")
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return pool, name, nil
}
