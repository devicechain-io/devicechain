// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging_test

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// startTestServer runs an embedded nats-server with no authorization, and returns it.
func startTestServer(t *testing.T) *natsserver.Server {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("new nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(20 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv
}

// The property the wrappers exist for: when one returns, the SERVER can route to the
// subscription — not merely the client's write buffer holds a SUB.
//
// It is asserted from the server's own subscription count rather than by sending a
// message, deliberately. A send-and-receive test passes whether or not the wrapper
// waits, because the send itself takes long enough for the SUB to land on a loopback
// broker; it would be a test of the fixture's timing. The server's count is the
// server's own answer to "do you know about this yet".
func TestSubscribeSyncedIsRoutableWhenItReturns(t *testing.T) {
	const held = 500 * time.Millisecond
	srv := startTestServer(t)
	proxy := dctest.StartTCPProxy(t, srv.Addr().String())

	nc, err := nats.Connect("nats://"+proxy.Addr(), nats.NoReconnect())
	if err != nil {
		t.Fatalf("connect through proxy: %v", err)
	}
	t.Cleanup(nc.Close)

	before := srv.NumSubscriptions()

	// Hold the SUB in flight, so a wrapper that does not wait returns while the
	// server's count is provably unchanged.
	proxy.Hold(held)
	started := time.Now()
	sub, err := messaging.SubscribeSynced(nc, "routable.test", func(*nats.Msg) {})
	if err != nil {
		t.Fatalf("SubscribeSynced: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	cost := time.Since(started)

	if got := srv.NumSubscriptions(); got <= before {
		t.Fatalf("SubscribeSynced returned while the server still had %d subscriptions "+
			"(was %d before): it did not wait for the server, so a publisher on another "+
			"connection could be dropped in the window it left open.", got, before)
	}
	// The counterweight. The assertion above can also be satisfied by luck — the
	// flusher racing the check — so require that the call actually PAID the wait.
	// Without this, a wrapper that stopped confirming would still pass intermittently.
	if cost < held {
		t.Fatalf("SubscribeSynced returned in %v, faster than the %v the SUB was held "+
			"for, so it cannot have confirmed anything with the server; the count above "+
			"rose by luck rather than because the call waited.", cost, held)
	}
}

// The reason these are wrappers rather than a note telling callers to flush: when the
// confirmation fails the subscription must not survive. The natural hand-rolled shape
// (subscribe, flush, return the flush error) hands back an error while leaving a LIVE
// subscription delivering into a component that has just reported it could not start —
// and no handle to close it with.
func TestSubscribeSyncedLeavesNoSubscriptionWhenConfirmationFails(t *testing.T) {
	srv := startTestServer(t)
	proxy := dctest.StartTCPProxy(t, srv.Addr().String())

	nc, err := nats.Connect("nats://"+proxy.Addr(), nats.NoReconnect())
	if err != nil {
		t.Fatalf("connect through proxy: %v", err)
	}
	t.Cleanup(nc.Close)

	// Shortened so the failure path costs a fraction of a second rather than the
	// production five. Restored immediately: this is process-wide state.
	restore := messaging.SubscribeConfirmTimeout
	messaging.SubscribeConfirmTimeout = 200 * time.Millisecond
	t.Cleanup(func() { messaging.SubscribeConfirmTimeout = restore })

	// The server stops hearing us while the socket stays open, so the round trip times
	// out instead of failing fast — which is the case the cleanup exists for. A closed
	// connection would not exercise it: Subscribe itself fails there and the
	// confirmation is never reached.
	proxy.Drop()

	sub, err := messaging.SubscribeSynced(nc, "unconfirmed.test", func(*nats.Msg) {})
	if err == nil {
		_ = sub.Unsubscribe()
		t.Fatal("SubscribeSynced succeeded while the broker could not hear the connection; " +
			"it is not confirming anything.")
	}
	if sub != nil {
		t.Fatalf("SubscribeSynced returned both an error (%v) and a subscription", err)
	}
	if got := nc.NumSubscriptions(); got != 0 {
		t.Fatalf("after a failed SubscribeSynced the client still holds %d subscription(s): "+
			"the caller was told startup failed but the handler is still live, and it has "+
			"no handle to tear down.", got)
	}
}

// The same-connection exception, pinned so the rule does not get over-applied. A
// connection that subscribes and then publishes is already ordered by its own write
// buffer, so nothing here needs a confirmation — this is the shape the DETECT purge
// gather relies on, and a future "add a flush everywhere" sweep would cost a broker
// round trip per pass for nothing.
func TestSameConnectionSubscribeThenPublishNeedsNoConfirmation(t *testing.T) {
	srv := startTestServer(t)
	nc, err := nats.Connect(srv.ClientURL(), nats.NoReconnect())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)

	// Run it enough times that an unordered implementation would show up. A single
	// pass proves nothing here: the claim is about an invariant, not one outcome.
	for i := 0; i < 200; i++ {
		//subconfirm:ok the raw call IS the subject: this loop exists to prove it needs no
		// confirmation when the same connection publishes
		sub, err := nc.SubscribeSync("ordered.test")
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		if err := nc.Publish("ordered.test", []byte("x")); err != nil {
			t.Fatalf("publish: %v", err)
		}
		if _, err := sub.NextMsg(2 * time.Second); err != nil {
			t.Fatalf("iteration %d: a publish on the SAME connection as the subscribe was "+
				"not delivered (%v). If this ever fails, the no-flush reasoning in "+
				"tenant_purge_detect.go is wrong and that gather needs a confirmation.", i, err)
		}
		if err := sub.Unsubscribe(); err != nil {
			t.Fatalf("unsubscribe: %v", err)
		}
	}
}
