// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package test holds test helpers shared across the platform's modules.
//
// 🔑 IT IMPORTS NOTHING FROM core, AND THAT CONSTRAINT IS LOAD-BEARING RATHER THAN
// tidiness. A helper here has to be usable from core's OWN package tests, and Go
// forbids the cycle that appears the moment this package imports one of them — so a
// fixture that depends on core lives in a subpackage (msgtest) instead. The messaging
// mocks were moved out for exactly this reason: they made helpers such as MQTTPort unreachable from
// core/messaging's tests, which is one of the places that most needs it.
package test

import (
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// MQTTPort returns the port srv's MQTT listener bound. Configure the listener with
// Port -1 and call this after ReadyForConnections: the server binds the port itself,
// so nothing can take it between a probe and the bind.
//
// 🔴 THIS REPLACED A FreeTCPPort HELPER (bind a probe listener, close it, hand the number
// to the server), which was inherently racy: anything on the machine could take the port
// between the close and the server's bind, and the failure was a flaky test. Do not
// reintroduce one. A test that must restart a broker on the SAME port reads the port back
// from the first start (see TestExternalMqttSourceResubscribesAfterBrokerRestart).
func MQTTPort(t testing.TB, srv *natsserver.Server) int {
	t.Helper()
	vz, err := srv.Varz(nil)
	if err != nil {
		t.Fatalf("read the broker's varz: %v", err)
	}
	if vz.MQTT.Port == 0 {
		t.Fatalf("the broker reports no MQTT port (listener failed to bind?)")
	}
	return vz.MQTT.Port
}
