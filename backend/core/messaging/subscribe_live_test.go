// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// SubscribeLive must not hand back a channel the broker cannot yet feed.
//
// A live feed is the least forgiving place to leave a subscribe unconfirmed: there is
// no replay, no acks and no gap detection, so an event published into the window is
// not delayed, it is gone — and indistinguishable from one that never happened. Every
// caller is a GraphQL subscription resolver, so the missed events are the first ones
// after a client opens a feed, which is precisely when a console is deciding what to
// render.
//
// Internal to the package because it constructs a NatsManager around a proxied
// connection, which the exported constructor cannot be asked to do.
func TestSubscribeLiveIsRoutableWhenItReturns(t *testing.T) {
	const held = 500 * time.Millisecond

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

	proxy := dctest.StartTCPProxy(t, srv.Addr().String())
	nc, err := nats.Connect("nats://"+proxy.Addr(), nats.NoReconnect())
	if err != nil {
		t.Fatalf("connect through proxy: %v", err)
	}
	t.Cleanup(nc.Close)

	cfg := &config.InstanceConfiguration{}
	cfg.ApplyDefaults()
	nmgr := &NatsManager{
		Microservice: &core.Microservice{
			InstanceId:            "test",
			FunctionalArea:        "area",
			InstanceConfiguration: *cfg,
		},
		nc: nc,
	}

	before := srv.NumSubscriptions()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxy.Hold(held)
	started := time.Now()
	if _, err := nmgr.SubscribeLive(ctx, "acme", streams.AlarmEvents); err != nil {
		t.Fatalf("SubscribeLive: %v", err)
	}
	cost := time.Since(started)

	if got := srv.NumSubscriptions(); got <= before {
		t.Fatalf("SubscribeLive returned a channel while the server still held %d "+
			"subscriptions (was %d): a publisher on another connection would be dropped "+
			"in the window, and a live feed has no replay to recover it from.", got, before)
	}
	if cost < held {
		t.Fatalf("SubscribeLive returned in %v, faster than the %v the SUB was held for, "+
			"so it cannot have confirmed anything; the count above rose by luck.", cost, held)
	}
}
