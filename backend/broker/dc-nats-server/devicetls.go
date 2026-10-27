// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"

	"github.com/nats-io/nats-server/v2/server"
)

// applyDeviceTLS makes the MQTT listener ASK each device for a client certificate, without
// requiring one.
//
// nats-server's configuration grammar has two client-certificate modes for a listener: none, or
// `verify: true`, which requires and verifies one against a static ca_file (GenTLSConfig sets
// ClientAuth only on verify). Neither serves a mixed fleet, where devices that authenticate by
// token or password share the listener with devices that authenticate by certificate. The
// library has a third mode, tls.RequestClientCert, that its config file cannot express: the
// server sends a CertificateRequest carrying no CA list, a device without a certificate answers
// with an empty Certificate message and carries on exactly as before, and a device with one
// sends its chain and signs the handshake with its key. The handshake proves possession; the
// auth callout is handed the chain (ClientTLS.Certs) and decides what it is worth.
//
// 🔴 IT ONLY EVER RAISES "NONE" TO "REQUEST". A listener configured with `verify: true`
// (RequireAndVerifyClientCert) is left alone: lowering it would silently turn an operator's
// certificates-only broker into one that admits devices without certificates. Any other mode
// was set by something other than the config grammar and is not ours to second-guess.
//
// 🔴 IT MUST RUN ON EVERY OPTIONS VALUE THE SERVER IS GIVEN, NOT ONCE. ProcessConfigFile builds
// the MQTT TLS config afresh (opts.go, `o.MQTT.TLSConfig, err = GenTLSConfig(tc)`), and a reload
// hands the server those new options with the MQTT TLS config excluded from the "unsupported
// change" diff (reload.go, the "mqtt" case). So a reload that skips this does not fail: it
// SUCCEEDS, and the listener quietly stops asking for certificates. That is why this binary owns
// the SIGHUP path (signals_unix.go) instead of leaving it to Server.Reload, and why the probe in
// probe.go watches the listener itself rather than trusting that this ran.
//
// 🔴 ONE RELOAD PATH IS NOT THIS BINARY'S: a $SYS.REQ.SERVER.<id>.RELOAD request, which
// nats-server answers by calling Server.Reload itself (server/events.go, reloadConfig), bypassing
// this hook. It is closed by permissions, not by code: the only system-account login is held to
// an allow list that refuses it (core/natsauth SysPublishAllow, pinned by
// TestTheSysLoginCannotReloadTheBroker). Were it ever sent, the probe would see the result and
// BrokerMqttClientAuthLost would fire.
//
// The config is cloned rather than edited in place so that this never alters a *tls.Config some
// other listener might hold.
func applyDeviceTLS(opts *server.Options) {
	tc := opts.MQTT.TLSConfig
	if tc == nil || tc.ClientAuth != tls.NoClientCert {
		return
	}
	c := tc.Clone()
	c.ClientAuth = tls.RequestClientCert
	opts.MQTT.TLSConfig = c
}
