// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

// calloutBoundsRig is a responder subscribed on a real broker, plus a way to send it
// authorization requests the way nats-server does and read the replies.
type calloutBoundsRig struct {
	t    *testing.T
	conn *nats.Conn
	r    *CalloutResponder
	kp   nkeys.KeyPair
}

func newCalloutBoundsRig(t *testing.T, authFn func(context.Context, *model.PresentedCredential) (*model.Device, error),
	maxInFlight int, lookupTimeout, stopWait time.Duration) *calloutBoundsRig {
	t.Helper()
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := startCalloutBroker(t, creds)
	r := mustResponder(t, conn, fakeAuthApi{authFn: authFn}, testChecker(t, credentialtest.NewStore()), creds.IssuerSeed, nil)
	r.Configure(maxInFlight, lookupTimeout, stopWait, nil)
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	kp, err := nkeys.CreateServer()
	if err != nil {
		t.Fatal(err)
	}
	return &calloutBoundsRig{t: t, conn: conn, r: r, kp: kp}
}

// send delivers one access-token authorization request and returns the subscription its
// reply arrives on.
func (g *calloutBoundsRig) send() *nats.Subscription {
	g.t.Helper()
	ukp, _ := nkeys.CreateUser()
	userNkey, _ := ukp.PublicKey()
	claims := jwt.NewAuthorizationRequestClaims(userNkey)
	claims.UserNkey = userNkey
	claims.Server.ID = "NTESTSERVER"
	claims.ConnectOptions.Username = "acme-corp:dev1"
	claims.ClientInformation.MQTT = testClientID
	encoded, err := claims.Encode(g.kp)
	if err != nil {
		g.t.Fatal(err)
	}
	inbox := nats.NewInbox()
	sub, err := g.conn.SubscribeSync(inbox)
	if err != nil {
		g.t.Fatal(err)
	}
	// Handed to the dispatcher directly: the service login may not publish on the
	// system subject the broker uses, and the dispatcher is what Start subscribes with.
	g.r.dispatch(&nats.Msg{Subject: AuthCalloutSubject, Reply: inbox, Data: []byte(encoded)})
	return sub
}

// denied reads one reply and reports whether it is the generic denial.
func denied(t *testing.T, sub *nats.Subscription, within time.Duration) (bool, bool) {
	t.Helper()
	msg, err := sub.NextMsg(within)
	if err != nil {
		return false, false
	}
	resp, err := jwt.DecodeAuthorizationResponseClaims(string(msg.Data))
	if err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	return resp.Error == genericAuthFailure, true
}

func sensorDevice() *model.Device {
	d := &model.Device{}
	d.Token = "sensor-001"
	return d
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// With maxInFlight requests already being authorized, the next is refused at once with
// the generic denial, and is never handed to the credential store.
func TestCalloutRefusesARequestOverTheInFlightBound(t *testing.T) {
	var entered, concurrent, peak atomic.Int32
	release := make(chan struct{})
	g := newCalloutBoundsRig(t, func(ctx context.Context, _ *model.PresentedCredential) (*model.Device, error) {
		entered.Add(1)
		n := concurrent.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer concurrent.Add(-1)
		<-release
		return sensorDevice(), nil
	}, 2, 5*time.Second, 5*time.Second)

	first, second := g.send(), g.send()
	waitFor(t, "two requests in flight", func() bool { return entered.Load() == 2 })

	third := g.send()
	isDenial, got := denied(t, third, 2*time.Second)
	if !got || !isDenial {
		t.Fatalf("the request over the bound got denial=%v reply=%v, want the generic denial", isDenial, got)
	}
	if g.r.RefusedBusy() != 1 || g.r.InFlight() != 2 {
		t.Fatalf("refusedBusy=%d inFlight=%d, want 1 and 2", g.r.RefusedBusy(), g.r.InFlight())
	}
	if entered.Load() != 2 {
		t.Fatalf("%d lookups started, want 2: the refused request reached the store", entered.Load())
	}

	close(release)
	for i, sub := range []*nats.Subscription{first, second} {
		if isDenial, got := denied(t, sub, 5*time.Second); !got || isDenial {
			t.Fatalf("admitted request %d: denial=%v reply=%v, want a grant", i, isDenial, got)
		}
	}
	if peak.Load() > 2 {
		t.Fatalf("peak concurrency %d exceeds the bound of 2", peak.Load())
	}
	waitFor(t, "in-flight to drain", func() bool { return g.r.InFlight() == 0 })
	// The slot is free again: a later request is admitted.
	if isDenial, got := denied(t, g.send(), 5*time.Second); !got || isDenial {
		t.Fatalf("a request after the drain was refused: denial=%v reply=%v", isDenial, got)
	}
}

// Every request's credential work carries a deadline, and a lookup that never returns is
// cut off by it and answered with the generic denial.
func TestCalloutLookupCarriesADeadline(t *testing.T) {
	var remaining atomic.Int64
	g := newCalloutBoundsRig(t, func(ctx context.Context, _ *model.PresentedCredential) (*model.Device, error) {
		if dl, ok := ctx.Deadline(); ok {
			remaining.Store(int64(time.Until(dl)))
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}, 4, 200*time.Millisecond, 5*time.Second)

	isDenial, got := denied(t, g.send(), 3*time.Second)
	if !got || !isDenial {
		t.Fatalf("a lookup that never returned got denial=%v reply=%v, want the generic denial after the deadline", isDenial, got)
	}
	if r := time.Duration(remaining.Load()); r <= 0 || r > 200*time.Millisecond {
		t.Fatalf("the lookup's context had %v left at entry, want a deadline within 200ms", r)
	}
}

// Stop returns only after the requests in flight have finished.
func TestCalloutStopWaitsForRequestsInFlight(t *testing.T) {
	var entered atomic.Int32
	release := make(chan struct{})
	g := newCalloutBoundsRig(t, func(ctx context.Context, _ *model.PresentedCredential) (*model.Device, error) {
		entered.Add(1)
		<-release
		return sensorDevice(), nil
	}, 4, 5*time.Second, time.Minute)

	sub := g.send()
	waitFor(t, "a request in flight", func() bool { return entered.Load() == 1 })

	stopped := make(chan struct{})
	go func() {
		_ = g.r.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a request was still in flight")
	case <-time.After(300 * time.Millisecond):
	}
	released := time.Now()
	close(release)
	// stopWait is a minute: returning promptly after the release shows Stop waits for the
	// handlers themselves, not for the whole bound.
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return promptly after the in-flight request finished")
	}
	if d := time.Since(released); d > time.Second {
		t.Fatalf("Stop took %v after the release, want well under a second", d)
	}
	if g.r.InFlight() != 0 {
		t.Fatalf("inFlight=%d after Stop", g.r.InFlight())
	}
	if isDenial, got := denied(t, sub, 2*time.Second); !got || isDenial {
		t.Fatalf("the request in flight at Stop was not answered with a grant: denial=%v reply=%v", isDenial, got)
	}
}

// A handler that outlasts the wait is cancelled, and Stop returns at the bound rather
// than hanging.
func TestCalloutStopGivesUpAndCancelsAStuckRequest(t *testing.T) {
	var entered atomic.Int32
	cancelled := make(chan struct{})
	g := newCalloutBoundsRig(t, func(ctx context.Context, _ *model.PresentedCredential) (*model.Device, error) {
		entered.Add(1)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}, 4, time.Minute, 150*time.Millisecond)

	g.send()
	waitFor(t, "a request in flight", func() bool { return entered.Load() == 1 })

	start := time.Now()
	_ = g.r.Stop()
	if d := time.Since(start); d < 100*time.Millisecond || d > 3*time.Second {
		t.Fatalf("Stop took %v, want about the 150ms bound", d)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("the stuck request was not cancelled by Stop")
	}
}

// A request that arrives after Stop is neither handled nor answered.
func TestCalloutDispatchAfterStopDoesNothing(t *testing.T) {
	var entered atomic.Int32
	g := newCalloutBoundsRig(t, func(ctx context.Context, _ *model.PresentedCredential) (*model.Device, error) {
		entered.Add(1)
		return sensorDevice(), nil
	}, 4, 5*time.Second, time.Second)

	if err := g.r.Stop(); err != nil {
		t.Fatal(err)
	}
	sub := g.send()
	if _, got := denied(t, sub, 500*time.Millisecond); got {
		t.Fatal("a request after Stop was answered")
	}
	if entered.Load() != 0 || g.r.InFlight() != 0 || g.r.RefusedBusy() != 0 {
		t.Fatalf("a request after Stop reached a handler: entered=%d inFlight=%d refusedBusy=%d",
			entered.Load(), g.r.InFlight(), g.r.RefusedBusy())
	}
}
