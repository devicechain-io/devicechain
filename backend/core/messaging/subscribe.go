// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"fmt"
	"time"

	nats "github.com/nats-io/nats.go"
)

// SubscribeConfirmTimeout bounds the round trip below. It is generous because the
// only thing that makes it expire is a broker that has stopped answering, and a
// service that cannot reach the broker at startup has a bigger problem than this
// call — the point of the bound is that startup fails loudly instead of hanging.
//
// A var rather than a const solely so subscribe_test.go can shorten it; nothing in
// production writes to it.
//
// ⚠️ Because it is process-wide state, a test that overrides it must NOT call
// t.Parallel(), and neither may anything else in the package while it is overridden.
// There is no parallelism in this package today, which is the only reason this is safe.
var SubscribeConfirmTimeout = 5 * time.Second

// ConfirmSubscribed blocks until the server has registered every subscription this
// connection has issued so far.
//
// # 🔴 Why a subscribe alone is not enough, and what it costs to believe it is
//
// nats.go's Subscribe/QueueSubscribe/ChanSubscribe append a SUB to the connection's
// write buffer and return. None of them waits for the server. So a component can
// subscribe, report itself started, and be handed nothing — because for the next
// microseconds the server has never heard of the subscription, and core NATS DROPS a
// publish with no subscriber rather than queueing or retrying it. The message is not
// late; it is gone.
//
// This flushes, which sends a PING and waits for the PONG. A connection's protocol
// messages are processed in order, so a PONG is proof the server already handled the
// SUB ahead of it.
//
// # When you do NOT need this
//
// If the SAME connection subscribes and then publishes the thing it expects a reply
// to, ordering is already guaranteed: both writes land in one buffer under one lock,
// so the server reads SUB before PUB. Flushing there buys nothing and costs a broker
// round trip — see the request/reply gather in tenant_purge_detect.go, which
// deliberately does not flush and explains why.
//
// The case that NEEDS this is the other one: a DIFFERENT connection publishes, so
// there is no ordering relationship with our SUB at all, and the only way to know the
// server can route to us is to ask it. Every long-lived responder and watcher is that
// case.
//
// The auth callout is the worked example of the cost. It answers device connections,
// so a request that lands in the window is a device REFUSED — with a bare EOF and no
// reason on the wire — while the service logs that devices are now authenticated.
// callout_race_test.go holds the SUB in flight and drives exactly that.
func ConfirmSubscribed(nc *nats.Conn) error {
	if err := nc.FlushTimeout(SubscribeConfirmTimeout); err != nil {
		return fmt.Errorf("the broker did not confirm our subscriptions within %v, so it "+
			"cannot yet route to them: %w", SubscribeConfirmTimeout, err)
	}
	return nil
}

// SubscribeSynced is nats.Conn.Subscribe that does not return until the server can
// route to the subscription. See ConfirmSubscribed for when this matters.
func SubscribeSynced(nc *nats.Conn, subject string, cb nats.MsgHandler) (*nats.Subscription, error) {
	sub, err := nc.Subscribe(subject, cb)
	return confirm(nc, sub, err)
}

// QueueSubscribeSynced is nats.Conn.QueueSubscribe with the same guarantee.
func QueueSubscribeSynced(nc *nats.Conn, subject, queue string, cb nats.MsgHandler) (*nats.Subscription, error) {
	sub, err := nc.QueueSubscribe(subject, queue, cb)
	return confirm(nc, sub, err)
}

// confirm adds the round trip to a just-created subscription, and tears the
// subscription down if the round trip fails.
//
// That cleanup is the reason these wrappers exist rather than a note telling callers
// to flush. Hand-rolled, the natural shape is subscribe, flush, return the error —
// which on a failed flush hands the caller an error while leaving a LIVE subscription
// behind, delivering into a component that has just reported it could not start. The
// caller then has no handle to close it with.
func confirm(nc *nats.Conn, sub *nats.Subscription, err error) (*nats.Subscription, error) {
	if err != nil {
		return nil, err
	}
	if cerr := ConfirmSubscribed(nc); cerr != nil {
		_ = sub.Unsubscribe()
		return nil, cerr
	}
	return sub, nil
}
