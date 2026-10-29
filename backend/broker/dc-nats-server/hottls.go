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
// still drops callout sessions): the config text changed, a CA bundle changed, or a listener whose
// TLS this file does not wrap is configured (websocket, leafnode, https monitoring: the library
// installs its own GetConfigForClient on those, which would shadow ours).
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
// applyDeviceTLS, so the current MQTT config is the one that requests device certificates.
func wrapOptions(opts *server.Options) *hotTLS {
	h := &hotTLS{}
	opts.TLSConfig = h.client.wrap(opts.TLSConfig)
	opts.MQTT.TLSConfig = h.mqtt.wrap(opts.MQTT.TLSConfig)
	opts.Cluster.TLSConfig = h.cluster.wrap(opts.Cluster.TLSConfig)
	return h
}

// unwrappedListener reports whether opts configures TLS on a listener the wrappers do not cover.
func unwrappedListener(opts *server.Options) bool {
	return opts.Websocket.TLSConfig != nil || opts.LeafNode.TLSConfig != nil || opts.HTTPSPort != 0
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
