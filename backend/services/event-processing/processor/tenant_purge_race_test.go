// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
	nats "github.com/nats-io/nats.go"
)

// The responder's startup window, and why it is worth its own test rather than
// inheriting the auth-callout one.
//
// # 🔴 What is at stake here is a FALSE CLEAN, not a slow start
//
// The requester is user-management, in another process on its own connection, so
// nothing orders its publish against this responder's SUB. A request that arrives
// before the server has registered the subscription is answered by the BROKER, not by
// us: no interest, no responders.
//
// The caller does not always treat that as a deferral. When it knows of partitions
// holding checkpoints it defers and retries, which is safe. But the complement — no
// responder AND no checkpointed partition — is recorded as a CLEAN erasure of the
// tenant's DETECT state, and that record is byte-identical to a real one. Nothing
// re-checks it. So an unconfirmed subscribe here can write a lie into the tenant
// deletion ledger, which is a worse outcome than the callout's refused device: the
// device retries, the ledger does not.
//
// A partition only appears in the expected set once it has committed a checkpoint, so
// "no checkpointed partition" is the ordinary state of a quiet instance, not a corner.
func TestPurgeResponderIsRoutableWhenStartReturns(t *testing.T) {
	// Comfortably inside DetectPurgeWindow (5s), which the reply must still fit in —
	// the responder's answer crosses the held proxy too.
	const held = 500 * time.Millisecond

	srv := purgeBroker(t)

	// The responder reaches the broker through the proxy, so its SUB can be held in
	// flight. The requester connects DIRECTLY, which is the whole point: a different
	// connection, with no ordering relationship to ours.
	proxy := dctest.StartTCPProxy(t, srv.Addr().String())
	respConn, err := nats.Connect("nats://"+proxy.Addr(), nats.NoReconnect())
	if err != nil {
		t.Fatalf("responder connect through proxy: %v", err)
	}
	t.Cleanup(respConn.Close)
	reqConn := purgeConn(t, srv)

	rig := newEvictionRig(t, newTestStore(t), "acme")
	rig.start(t)

	proxy.Hold(held)
	started := time.Now()
	r := NewTenantPurgeResponder(respConn, evictInstance, rig.rp)
	if err := r.Start(); err != nil {
		t.Fatalf("responder Start: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop() })
	startCost := time.Since(started)

	// The request goes out the instant Start() returns — which is exactly what the
	// coordinator does, since Start() returning is the only readiness signal there is.
	ctx, cancel := context.WithTimeout(context.Background(), messaging.DetectPurgeWindow+2*time.Second)
	defer cancel()
	res, err := messaging.PurgeTenantDetect(ctx, reqConn, evictInstance, "acme", nil)
	if err != nil {
		t.Fatalf("PurgeTenantDetect: %v", err)
	}

	if res.NoResponders {
		t.Fatalf("the broker reported NO RESPONDERS %v after Start() returned, so the "+
			"subscription was still in flight.\n"+
			"With no checkpointed partition — the ordinary state of a quiet instance — "+
			"the caller turns exactly this observation into a CLEAN erasure record for "+
			"the tenant's DETECT state, byte-identical to a real one and never "+
			"re-checked. The engine is running and holds the tenant's state.", startCost)
	}
	if len(res.Replies) == 0 {
		t.Fatal("no partition answered a request sent to a running responder")
	}

	// The counterweight: the pass above is only meaningful if Start() actually PAID for
	// it. A version that returned early and got lucky must not read as a fix.
	if startCost < held {
		t.Fatalf("Start() returned in %v, faster than the %v the SUB was held for, so it "+
			"cannot have confirmed the subscription; the answer above arrived by luck.",
			startCost, held)
	}
}
