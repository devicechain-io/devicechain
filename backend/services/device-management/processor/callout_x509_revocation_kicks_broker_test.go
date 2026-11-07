// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/x509trust"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/require"
)

// REVOCATION KICKS ON A REAL BROKER: a certificate revoked while its device is connected ends that
// session (and only that one), a CA-level revocation ends the anchor's, the device's reconnects are
// refused as cert_revoked, and a session admitted in the race with the revocation is ended by the
// re-sweep. The kick is made the way event-sources makes it (CONNZ then KICK over the system login),
// through the model's real revoke + durable queue + drain; the callout is the real responder.

// revLive connects a certificate device that reconnects on its own, as firmware does, so "stays
// disconnected" can only mean the broker keeps refusing it.
func (f *x509Fixture) revLive(device string, leaf *testCert, inters ...*testCert) mqtt.Client {
	f.t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(f.serverCA.cert)
	opts := mqtt.NewClientOptions().AddBroker(fmt.Sprintf("ssl://127.0.0.1:%d", f.port)).
		SetClientID(clientIDOf(device)).SetUsername(certUser()).
		SetTLSConfig(&tls.Config{RootCAs: pool, Certificates: []tls.Certificate{*presenting(leaf, inters...)}, MinVersion: tls.VersionTLS12}).
		SetAutoReconnect(true).SetConnectRetry(false).SetMaxReconnectInterval(200 * time.Millisecond).
		SetConnectTimeout(5 * time.Second)
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	require.True(f.t, tok.WaitTimeout(15*time.Second))
	require.NoError(f.t, tok.Error())
	f.t.Cleanup(func() { c.Disconnect(50) })
	return c
}

func (f *x509Fixture) revoke(anchor string, c *testCert) error {
	f.t.Helper()
	fp := x509trust.Fingerprint(c.cert)
	_, err := f.api.RevokeDeviceCertificate(f.ctx, &model.DeviceCertificateRevokeRequest{AnchorToken: anchor, FingerprintSha256: &fp})
	return err
}

// askedKicker passes every kick on to the real kicker and remembers which devices were asked
// about, in order. A device that is kicked and reconnects at once looks, from its session alone,
// like one that was never touched; the record is what tells the leaf kick from the anchor-wide one.
type askedKicker struct {
	inner model.SessionKicker
	mu    sync.Mutex
	asked []string
}

func (k *askedKicker) KickDeviceSessions(ctx context.Context, tenant, reason string, tokens []string) error {
	k.mu.Lock()
	k.asked = append(k.asked, tokens...)
	k.mu.Unlock()
	return k.inner.KickDeviceSessions(ctx, tenant, reason, tokens)
}

func (k *askedKicker) take() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := slices.Clone(k.asked)
	k.asked = nil
	slices.Sort(out)
	return slices.Compact(out)
}

// 🔑 A LEAF REVOKED WHILE ITS SESSION IS LIVE: that device's session ends and its reconnects are
// refused cert_revoked, while a sibling under the same CA stays connected untouched. Then a CA-level
// revocation (an intermediate): every session under the anchor is ended, the devices under the
// still-valid intermediate come straight back (a cheap reconnect), those under the revoked one are
// refused, and a device on another anchor is untouched throughout.
func TestX509RevocationEndsLiveSessions(t *testing.T) {
	f := newX509Fixture(t)
	f.api.RevocationResweepAfter = time.Hour // the re-sweep is the race test's; keep it out of this one
	root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
	otherRoot := newTestCert(t, nil, certOpts{cn: "Other Root", ca: true})
	interA := newTestCert(t, root, certOpts{cn: "Line A", ca: true})
	interB := newTestCert(t, root, certOpts{cn: "Line B", ca: true})
	f.anchor("maker", root)
	f.anchor("other", otherRoot)
	leafA1 := newTestCert(t, interA, certOpts{cn: "SN-A1"})
	leafA2 := newTestCert(t, interA, certOpts{cn: "SN-A2"})
	leafB1 := newTestCert(t, interB, certOpts{cn: "SN-B1"})
	leafX := newTestCert(t, otherRoot, certOpts{cn: "SN-X"})
	for _, d := range []string{"dev-a1", "dev-a2", "dev-b1", "dev-x"} {
		f.device(d)
	}
	f.pin("c-a1", "dev-a1", "maker", leafA1, interA)
	f.pin("c-a2", "dev-a2", "maker", leafA2, interA)
	f.pin("c-b1", "dev-b1", "maker", leafB1, interB)
	f.pin("c-x", "dev-x", "other", leafX)

	rec := &askedKicker{inner: brokerKicker{srv: f.srv, t: t}}
	f.api.SessionKicker = rec
	a1 := f.revLive("dev-a1", leafA1, interA)
	a2 := f.revLive("dev-a2", leafA2, interA)
	b1 := f.revLive("dev-b1", leafB1, interB)
	x := f.revLive("dev-x", leafX)
	// The control: nothing but the revocation ends a session.
	time.Sleep(time.Second)
	for name, c := range map[string]mqtt.Client{"a1": a1, "a2": a2, "b1": b1, "x": x} {
		require.True(t, c.IsConnectionOpen(), "%s must be connected before anything is revoked", name)
	}

	before := f.counts()
	require.NoError(t, f.revoke("maker", leafA1), "the revocation and its kick")
	require.Eventually(t, func() bool { return !a1.IsConnectionOpen() }, 10*time.Second, 25*time.Millisecond,
		"the revoked certificate's session was not ended")
	require.Never(t, a1.IsConnectionOpen, 2*time.Second, 50*time.Millisecond, "the revoked certificate reconnected")
	require.Greater(t, f.count(x509ResultCertRevoked), before[x509ResultCertRevoked], "its reconnects must be refused as cert_revoked")
	require.True(t, a2.IsConnectionOpen(), "a sibling under the same CA must be untouched by a leaf revocation")
	require.True(t, b1.IsConnectionOpen())
	require.True(t, x.IsConnectionOpen(), "another anchor's device must be untouched")
	require.Equal(t, []string{"dev-a1"}, rec.take(), "a leaf revocation asks the broker about that device alone")

	// CA-level: the intermediate B is revoked. The kick is anchor-wide.
	require.NoError(t, f.revoke("maker", interB))
	require.Eventually(t, func() bool { return !b1.IsConnectionOpen() }, 10*time.Second, 25*time.Millisecond,
		"a device under the revoked intermediate kept its session")
	require.Never(t, b1.IsConnectionOpen, 2*time.Second, 50*time.Millisecond,
		"a device under a revoked intermediate reconnected")
	require.Eventually(t, a2.IsConnectionOpen, 10*time.Second, 25*time.Millisecond,
		"a device under the still-valid intermediate must come straight back after the anchor-wide kick")
	require.False(t, a1.IsConnectionOpen(), "the leaf revoked earlier stays refused")
	require.True(t, x.IsConnectionOpen(), "another anchor's device is untouched by the anchor-wide kick")
	require.Equal(t, []string{"dev-a1", "dev-a2", "dev-b1"}, rec.take(),
		"a CA-level revocation asks about every device under the anchor, and no other anchor's")
}

// pausingApi lets a test hold ONE authorization between its revocation read and the grant: the
// decorated call reads the revocation state for real, and only then waits.
type pausingApi struct {
	model.DeviceManagementApi
	armed   atomic.Bool
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *pausingApi) ResolveX509Revocation(ctx context.Context, anchor *model.DeviceTrustAnchor,
	keys x509trust.RevocationQuery, now time.Time) (x509trust.RevocationState, error) {
	state, err := p.DeviceManagementApi.ResolveX509Revocation(ctx, anchor, keys, now)
	if p.armed.CompareAndSwap(true, false) {
		p.once.Do(func() { close(p.reached) })
		select {
		case <-p.release:
		case <-time.After(4 * time.Second):
		}
	}
	return state, err
}

// raceDevice runs the admission of dev-race up to its revocation read (held there), commits a
// revocation of its leaf, then lets the admission go. It returns the connect's outcome.
func raceDevice(t *testing.T, f *x509Fixture, pause *pausingApi, leaf *testCert) (mqtt.Client, error) {
	t.Helper()
	pause.armed.Store(true)
	type result struct {
		c   mqtt.Client
		err error
	}
	done := make(chan result, 1)
	go func() {
		pool := x509.NewCertPool()
		pool.AddCert(f.serverCA.cert)
		opts := mqtt.NewClientOptions().AddBroker(fmt.Sprintf("ssl://127.0.0.1:%d", f.port)).
			SetClientID(clientIDOf("dev-race")).SetUsername(certUser()).
			SetTLSConfig(&tls.Config{RootCAs: pool, Certificates: []tls.Certificate{*presenting(leaf)}, MinVersion: tls.VersionTLS12}).
			SetAutoReconnect(false).SetConnectRetry(false).SetConnectTimeout(8 * time.Second)
		c := mqtt.NewClient(opts)
		tok := c.Connect()
		tok.WaitTimeout(10 * time.Second)
		done <- result{c, tok.Error()}
	}()
	select {
	case <-pause.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the authorization never reached the revocation read")
	}
	// The authorization holds "not revoked". The revocation commits now, with its first sweep.
	require.NoError(t, f.revoke("maker", leaf))
	close(pause.release)
	res := <-done
	return res.c, res.err
}

func newRaceFixture(t *testing.T) (*x509Fixture, *pausingApi, *testCert) {
	t.Helper()
	pause := &pausingApi{reached: make(chan struct{}), release: make(chan struct{})}
	f := newX509FixtureWith(t, fixtureOpts{wrap: func(api *model.Api) model.DeviceManagementApi {
		pause.DeviceManagementApi = api
		return pause
	}})
	f.api.RevocationResweepAfter = 700 * time.Millisecond
	root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
	f.anchor("maker", root)
	leaf := newTestCert(t, root, certOpts{cn: "SN-RACE"})
	f.device("dev-race")
	f.pin("c-race", "dev-race", "maker", leaf)
	return f, pause, leaf
}

// drainUntil runs the background drain until cond holds, as the periodic task does.
func drainUntil(t *testing.T, f *x509Fixture, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := f.api.DrainSessionKicks(f.ctx, model.SessionKickDrainPageSize)
		require.NoError(t, err)
		return cond()
	}, 15*time.Second, 100*time.Millisecond, msg)
}

// 🔑 AN ADMISSION IN FLIGHT WHEN THE REVOCATION COMMITS ends up refused, whichever way the broker
// resolves it. The authorization has read "not revoked" (held there); the revocation commits and
// its first sweep runs. The embedded broker lists a connection that is still being authorized, so
// the first sweep closes it and the connect fails; were it ever granted instead (a broker that
// lists only authorized sessions), the re-sweep below ends it. Either way no session survives the
// re-sweep and the reconnect is refused cert_revoked.
func TestX509AnAdmissionInFlightAtRevocationDoesNotSurvive(t *testing.T) {
	f, pause, leaf := newRaceFixture(t)
	c, err := raceDevice(t, f, pause, leaf)
	if err == nil {
		t.Cleanup(func() { c.Disconnect(50) })
		t.Log("the broker granted the in-flight admission; the re-sweep must end it")
	} else {
		t.Logf("the first sweep closed the in-flight admission: %v", err)
	}
	drainUntil(t, f, func() bool { return err != nil || !c.IsConnectionOpen() },
		"the session admitted in the race window survived the re-sweep")

	before := f.counts()
	require.Error(t, f.revLiveTry("dev-race", leaf), "a reconnect after the revocation must be refused")
	f.moved(before, x509ResultCertRevoked)
}

// swallowingKicker answers its first call "nothing found, nothing to end" without asking the
// broker: a scan that missed the session (a listing taken before it registered, or a page that did
// not reach it). Every later call goes to the real kicker.
type swallowingKicker struct {
	inner   model.SessionKicker
	swallow atomic.Bool
}

func (k *swallowingKicker) KickDeviceSessions(ctx context.Context, tenant, reason string, tokens []string) error {
	if k.swallow.CompareAndSwap(true, false) {
		return nil
	}
	return k.inner.KickDeviceSessions(ctx, tenant, reason, tokens)
}

// 🔑 THE RE-SWEEP ENDS A SESSION THE FIRST SWEEP MISSED. The same paused admission, but the first
// sweep finds nothing (it is made to), so the authorization is granted after the kick has come and
// gone. The control: the session is open and nothing before the re-sweep ends it, so the race is
// real; the re-sweep, run when due, ends it, and the reconnect is refused.
func TestX509TheReSweepEndsASessionTheFirstSweepMissed(t *testing.T) {
	f, pause, leaf := newRaceFixture(t)
	k := &swallowingKicker{inner: brokerKicker{srv: f.srv, t: t}}
	k.swallow.Store(true)
	f.api.SessionKicker = k

	c, err := raceDevice(t, f, pause, leaf)
	require.NoError(t, err, "the admission read 'not revoked' before the revocation and is granted")
	t.Cleanup(func() { c.Disconnect(50) })
	require.True(t, c.IsConnectionOpen(), "the control: the first sweep missed this session")
	require.Never(t, func() bool { return !c.IsConnectionOpen() }, 300*time.Millisecond, 50*time.Millisecond,
		"nothing before the re-sweep may end it, or this test proves nothing")

	drainUntil(t, f, func() bool { return !c.IsConnectionOpen() },
		"the re-sweep did not end the session the first sweep missed")

	before := f.counts()
	require.Error(t, f.revLiveTry("dev-race", leaf), "a reconnect after the revocation must be refused")
	f.moved(before, x509ResultCertRevoked)
}

// revLiveTry makes one connect attempt and returns its error.
func (f *x509Fixture) revLiveTry(device string, leaf *testCert, inters ...*testCert) error {
	f.t.Helper()
	_, err := f.connect(clientIDOf(device), certUser(), "", *presenting(leaf, inters...))
	return err
}
