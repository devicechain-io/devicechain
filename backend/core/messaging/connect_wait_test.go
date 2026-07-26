// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// These pin the property that a service's initialization does not proceed on a
// connection that has not actually connected.
//
// The failure it prevents is not obvious from the symptom. nats.Connect with
// RetryOnFailedConnect returns a non-nil conn and a NIL error while still
// dialling; everything that reads connection-derived state then gets a zero
// value, and nats.go's KV layer turns that into "key-value requires at least
// server version 2.6.2" against a 2.14 server. Observed on a live cluster:
// device-management crashlooped every restart with an error blaming the server.

// TestWaitForConnectedReturnsOnAConnectedConn is the common case: the connection
// is already up by the time initialization looks, and the wait costs nothing.
func TestWaitForConnectedReturnsOnAConnectedConn(t *testing.T) {
	srv := runTestServer(t)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	start := time.Now()
	if err := waitForConnected(nc, 5*time.Second); err != nil {
		t.Fatalf("an established connection must satisfy the wait immediately: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("the wait took %s on an already-connected conn; it must not poll needlessly", elapsed)
	}
}

// TestWaitForConnectedTimesOutOnAStillDiallingConn is the case that matters.
//
// With RetryOnFailedConnect against a broker that is not there, the conn is
// non-nil and the error is nil — so without this wait, initialization proceeds
// and the KV path fails with a message about the server's VERSION.
func TestWaitForConnectedTimesOutOnAStillDiallingConn(t *testing.T) {
	// A port nothing is listening on. RetryOnFailedConnect makes this succeed.
	nc, err := nats.Connect("nats://127.0.0.1:1",
		nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1))
	if err != nil {
		t.Fatalf("RetryOnFailedConnect is supposed to return a conn and no error "+
			"against an unreachable broker; got %v", err)
	}
	defer nc.Close()

	if nc.IsConnected() {
		t.Fatal("the fixture is wrong: this conn must NOT be connected")
	}
	err = waitForConnected(nc, 300*time.Millisecond)
	if err == nil {
		t.Fatal("a connection that never connected must not satisfy the wait; " +
			"proceeding past it is what makes js.KeyValue report a bogus server version")
	}
	if !strings.Contains(err.Error(), "still") {
		t.Fatalf("the error should name the connection's status; got %q", err)
	}
}

// TestConnectedConnCanCreateKeyValue is the end of the causal chain, asserted
// rather than reasoned about: on a CONNECTED conn the KV layer works. Paired with
// the case below, it shows the version error is about connectedness and nothing
// else.
func TestConnectedConnCanCreateKeyValue(t *testing.T) {
	srv := runTestServer(t)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	if _, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: "probe", MaxBytes: 1 << 20}); err != nil {
		t.Fatalf("a connected conn must be able to create a KV bucket: %v", err)
	}
}

// TestUnconnectedConnReportsTheMisleadingVersionError documents the actual
// upstream behaviour this guard exists for, so a future reader does not have to
// rediscover that "server version 2.6.2" means "not connected yet".
func TestUnconnectedConnReportsTheMisleadingVersionError(t *testing.T) {
	nc, err := nats.Connect("nats://127.0.0.1:1",
		nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	_, err = js.CreateKeyValue(&nats.KeyValueConfig{Bucket: "probe", MaxBytes: 1 << 20})
	if err == nil {
		t.Skip("upstream no longer refuses KV on an unconnected conn; the guard is " +
			"now belt-and-braces rather than load-bearing")
	}
	if !strings.Contains(err.Error(), "2.6.2") {
		t.Logf("note: upstream's message has changed to %q — the guard still holds, "+
			"but the comment in nats.go naming this error is now stale", err)
	}
}

// runTestServer starts a plain in-process JetStream server.
func runTestServer(t *testing.T) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv
}
