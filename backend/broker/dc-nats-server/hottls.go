// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"sync/atomic"

	"github.com/nats-io/nats-server/v2/server"
)

// A certificate rotation must not disconnect the fleet.
//
// 🔴 WHY A RELOAD CANNOT BE USED FOR IT (nats-server v2.15.1). Server.ReloadOptions always runs
// reloadAuthorization (reload.go, diffOptions: `case "accounts"` appends an accountsOption
// unconditionally, because accounts "always differ"), and that closes every client for which
// clientHasMovedToDifferentAccount is true. That function looks the CONNECT username up in the
// configured users and, finding none, answers "user no longer exists" (reload.go,
// clientHasMovedToDifferentAccount, the final `return true`). A client admitted by the auth callout
// is by definition not a configured user, so EVERY callout-admitted session with a username (all
// MQTT devices) is closed by ANY reload, including one whose config is byte-identical. Measured in
// TestTLSRotationReloadKeepsMQTTSessions: 20 of 20 sessions lost, with no TLS option involved.
// Fixed upstream on main (nats-io/nats-server #8759, "Config reload disconnects clients authorized
// by auth callout", October 2026) and in no release yet: v2.15.2 does not carry it.
//
// WHAT THIS DOES INSTEAD. When the only thing that moved is key material (same config digest, same
// CA pools), the broker never calls ReloadOptions. The TLS configs the server is started with are
// wrappers whose GetConfigForClient (and GetClientCertificate, for the routes the server dials)
// hand out the CURRENT config, and a rotation swaps what that is. Established connections are
// untouched because nothing reloads; the next handshake presents the new certificate.
//
// WHEN IT DOES NOT APPLY, the reload is the full one (and, on a library without the fix above,
// still drops callout sessions):
//   - the config text changed, or a CA bundle did;
//   - a listener this file does not wrap has TLS (websocket and https monitoring install their own
//     GetConfigForClient, which would shadow ours; leafnode, gateway and their remotes are simply
//     not covered), or OCSP stapling is configured (unwrappedListener, wrapOptions);
//   - something other than this binary changed the server's TLS since our last reload: a reload
//     from outside (the config load time moved), or a hook the server put on a listener's config
//     (OCSP stapling for a Must-Staple leaf, ocsp_peer). GetConfigForClient returns the config
//     it is given wholesale, so those hooks, set on the wrapper, would be bypassed: ocsp_peer
//     verify would fail OPEN. broker.hotAllowed and liveIsOurs check both before every swap.
//
// 🔴 THE SERVER'S OWN /varz `tls_cert_not_after` reads the wrapper's frozen first certificate and is
// STALE after a hot swap (monitor.go). The served certificate's expiry is
// dc_broker_mqtt_tls_cert_not_after_seconds; use that, not varz, to watch for an expiring leaf.
//
// DROP THIS FILE when the library carries the upstream fix: TestFullReloadDropsCalloutSessions is
// the canary, and fails the day a bump makes the full reload keep sessions.

// swappable is one listener's TLS config, replaceable while the server runs.
type swappable struct {
	cur atomic.Pointer[tls.Config]
}

// wrap returns the config to hand the server in place of c, and makes c the current one.
func (w *swappable) wrap(c *tls.Config) *tls.Config {
	if c == nil {
		return nil
	}
	w.cur.Store(c)
	out := c.Clone()
	out.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) { return w.cur.Load(), nil }
	if len(c.Certificates) > 0 {
		// The client side of a route this server dials: present the current leaf.
		out.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			if cur := w.cur.Load(); len(cur.Certificates) > 0 {
				return &cur.Certificates[0], nil
			}
			return &tls.Certificate{}, nil
		}
	}
	return out
}

// hotTLS holds the three listeners the chart configures TLS on.
type hotTLS struct {
	client, mqtt, cluster swappable
}

// wrapOptions installs the wrappers into opts and returns the holder. It must run after
// applyDeviceTLS, so the current MQTT config is the one that requests device certificates. It
// returns nil, wrapping nothing, when the hot path could not be used with these options.
func wrapOptions(opts *server.Options) *hotTLS {
	if unwrappedListener(opts) {
		return nil
	}
	h := &hotTLS{}
	opts.TLSConfig = h.client.wrap(opts.TLSConfig)
	opts.MQTT.TLSConfig = h.mqtt.wrap(opts.MQTT.TLSConfig)
	opts.Cluster.TLSConfig = h.cluster.wrap(opts.Cluster.TLSConfig)
	return h
}

// unwrappedListener reports whether opts configures TLS where the wrappers do not reach, or OCSP.
func unwrappedListener(opts *server.Options) bool {
	if opts.OCSPConfig != nil || opts.Websocket.TLSConfig != nil || opts.LeafNode.TLSConfig != nil ||
		opts.Gateway.TLSConfig != nil || opts.HTTPSPort != 0 {
		return true
	}
	for _, g := range opts.Gateway.Gateways {
		if g.TLSConfig != nil {
			return true
		}
	}
	for _, r := range opts.LeafNode.Remotes {
		if r.TLSConfig != nil {
			return true
		}
	}
	return false
}

// liveIsOurs reports whether the TLS configs the server holds are still the wrappers this file
// installed, carrying no hook the server added: GetCertificate and VerifyConnection are what OCSP
// stapling and ocsp_peer set, and the MQTT config must still ask for client certificates.
func liveIsOurs(live *server.Options) bool {
	for _, c := range []*tls.Config{live.TLSConfig, live.MQTT.TLSConfig, live.Cluster.TLSConfig} {
		if c == nil {
			continue
		}
		if c.GetConfigForClient == nil || c.GetCertificate != nil || c.VerifyConnection != nil {
			return false
		}
	}
	if m := live.MQTT.TLSConfig; m != nil && m.ClientAuth == tls.NoClientCert {
		return false
	}
	return true
}

// swapIfKeyMaterialOnly makes next the current config of each wrapped listener when nothing but
// the key material differs from what is current. It reports whether it did; false means nothing
// was changed and a full reload is needed. next must not have been wrapped.
func (h *hotTLS) swapIfKeyMaterialOnly(next *server.Options) bool {
	if unwrappedListener(next) {
		return false
	}
	pairs := []struct {
		w *swappable
		n *tls.Config
	}{{&h.client, next.TLSConfig}, {&h.mqtt, next.MQTT.TLSConfig}, {&h.cluster, next.Cluster.TLSConfig}}
	for _, p := range pairs {
		cur := p.w.cur.Load()
		if (cur == nil) != (p.n == nil) {
			return false
		}
		if cur != nil && (!cur.RootCAs.Equal(p.n.RootCAs) || !cur.ClientCAs.Equal(p.n.ClientCAs) ||
			cur.ClientAuth != p.n.ClientAuth) {
			return false
		}
	}
	for _, p := range pairs {
		if p.n != nil {
			p.w.cur.Store(p.n)
		}
	}
	return true
}
