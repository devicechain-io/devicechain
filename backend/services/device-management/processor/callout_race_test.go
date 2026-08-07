// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/natsauth"
	dctest "github.com/devicechain-io/dc-microservice/test"
	nats "github.com/nats-io/nats.go"
)

// The auth-callout startup window: Start() must not report a responder that is live
// until the SERVER says it is.
//
// # 🔴 Why this test forces the window instead of looking for it
//
// nats.go's QueueSubscribe appends SUB to the connection's write buffer and returns.
// It does not wait for the server to acknowledge anything. So Start() could return —
// and log "device connections are now broker-authenticated" — while the server has no
// such subscription, and a device connecting in that gap gets its authorization
// request published to nobody. Core NATS drops a publish with no subscriber; the
// server does not retry it. The device waits out the authorization timeout and is
// refused with no reason on the wire.
//
// That window is normally microseconds wide, which is exactly the problem: it is real
// often enough to have failed CI twice, and rare enough that a fix would look like it
// worked whether or not it did. So this test does not race it. It holds the SUB in
// flight with a proxy and connects a device while it is provably still in the air —
// making the failure deterministic and the fix's effect a measurement rather than an
// absence.
//
// The consequence under test is the user-visible one — a DEVICE IS REFUSED — not the
// intermediate "a subscription registered late". Anyone can assert the latter; only
// the former is the reason to care.

func TestCalloutStartDoesNotReturnBeforeTheServerCanRouteToIt(t *testing.T) {
	const (
		// Wide enough that the device connect below is unambiguously inside the
		// window (it takes ~a millisecond against a loopback broker), short enough
		// that a correct Start() only costs this much.
		subInFlight = 750 * time.Millisecond
		// The server's own patience for a callout reply. The deployed value is 5s;
		// nothing here needs it to be, and the refusal path costs exactly this.
		authTimeout = 2 * time.Second
	)

	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	srv, mqttPort := startCalloutServer(t, creds, authTimeout)

	// The responder reaches the server through the proxy. The DEVICE does not — it
	// connects to the MQTT port directly, so nothing about its own connect is slowed
	// and the only thing in flight is the responder's SUB.
	proxy := dctest.StartTCPProxy(t, srv.Addr().String())
	nc, err := nats.Connect("nats://"+proxy.Addr(),
		nats.UserInfo(natsauth.ServiceUser, creds.ServicePassword),
		nats.NoReconnect())
	if err != nil {
		t.Fatalf("service connect through proxy: %v", err)
	}
	t.Cleanup(nc.Close)

	api := fakeAuthApi{authFn: func(context.Context, *model.PresentedCredential) (*model.Device, error) {
		d := &model.Device{}
		d.Token = "sensor-001"
		return d, nil
	}}
	r := NewCalloutResponder(nc, api, creds.IssuerSeed, "inst-1", nil)

	// The window opens here and everything the responder writes from now on — the SUB
	// included — sits in the proxy for subInFlight before the server sees it.
	proxy.Hold(subInFlight)

	started := time.Now()
	if err := r.Start(); err != nil {
		t.Fatalf("start responder: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop() })
	startCost := time.Since(started)

	// A device connects the instant Start() returns. That is the whole test: by every
	// signal the service has — the call returning, its own log line — the responder is
	// live. Unfixed, the SUB is still in the proxy and this is refused; fixed, Start()
	// has already paid the wait and the window is closed before this line runs.
	//
	// Note the responder's REPLY also crosses the held proxy, so in the fixed run this
	// connect spends ~subInFlight of the authTimeout budget. The margin is deliberate;
	// if this ever flakes, that ratio is the first thing to check.
	const requiredID = "inst-1:acme-corp:sensor-001"
	windowErr := mqttConnectAsDevice(t, mqttPort, requiredID, "acme-corp:dev1", "s3cret")

	// 🔑 THE CONTROL, and it decides whether the line above means anything. A refusal is
	// what a broken fixture produces for free — a proxy that mangles the stream, a
	// credential that never worked, a broker that is not accepting MQTT at all. So
	// close the window and connect the SAME device again. If THIS fails, the
	// arrangement is broken and the result above is not evidence of anything.
	proxy.Release()
	if err := mqttConnectAsDevice(t, mqttPort, requiredID+":control", "acme-corp:dev1", "s3cret"); err != nil {
		t.Fatalf("CONTROL FAILED: with the window closed the same device still could not "+
			"connect (%v). The fixture is broken, so no conclusion can be drawn about the "+
			"in-window attempt (which returned %v).", err, windowErr)
	}

	if windowErr != nil {
		t.Fatalf("a device connecting %v after Start() returned was REFUSED (%v).\n"+
			"Start() took %v, so it did not wait for the server to register the "+
			"subscription — it returned while the SUB was still in flight, logged that "+
			"device connections are now broker-authenticated, and the server published "+
			"this device's authorization request to nobody. Core NATS drops that publish "+
			"and does not retry it, so the device waited out the %v authorization timeout "+
			"and was refused with no reason on the wire.\n"+
			"The control below passed, so the broker, the credential and the proxy are "+
			"all fine: the only difference is that this attempt landed inside the window.",
			startCost, windowErr, startCost, authTimeout)
	}

	// The counterweight to the assertion above: Start() is only allowed to close the
	// window by WAITING for the server, and waiting is the cost. If it ever returns
	// faster than the SUB could possibly have reached the server, it is not waiting —
	// it has been "fixed" by something that does not establish the property (a sleep
	// that got tuned down, a flush on the wrong connection), and the pass above would
	// then be luck rather than proof.
	if startCost < subInFlight {
		t.Fatalf("Start() returned in %v, faster than the %v the SUB was held for. The "+
			"device connect passed, but Start() cannot have confirmed the subscription "+
			"with the server, so that pass is not evidence the window is closed.", startCost, subInFlight)
	}
}
