// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

// ---- fakes ----

type fakeAck struct{ acked bool }

func (a *fakeAck) Ack() error { a.acked = true; return nil }

type respWriter struct {
	mu      sync.Mutex
	devices []string
	envs    []cmddownlink.ResponseEnvelope
	fail    error
	// onWrite runs inside a publish, before it is recorded (a test hook).
	onWrite func()
}

func (w *respWriter) WriteToDevice(_ context.Context, device string, msgs ...messaging.Message) error {
	if w.onWrite != nil {
		w.onWrite()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fail != nil {
		return w.fail
	}
	for _, m := range msgs {
		var e cmddownlink.ResponseEnvelope
		if err := json.Unmarshal(m.Value, &e); err != nil {
			return err
		}
		w.devices = append(w.devices, device)
		w.envs = append(w.envs, e)
	}
	return nil
}

type fakeClaimer struct {
	calls     int
	nonce     string
	won       bool
	err       error
	gotNonce  string
	gotTenant string
	onClaim   func()
}

func (c *fakeClaimer) ClaimDispatch(_ context.Context, tenant, _ string, nonce string) (string, bool, error) {
	c.calls++
	if c.onClaim != nil {
		c.onClaim()
	}
	c.gotNonce, c.gotTenant = nonce, tenant
	return c.nonce, c.won, c.err
}

type fakeLinks struct {
	target connectorspec.LoRaWANDownlinkTarget
	err    error
	asked  []string
}

func (l *fakeLinks) Resolve(_ context.Context, _, integration string) (connectorspec.LoRaWANDownlinkTarget, error) {
	l.asked = append(l.asked, integration)
	return l.target, l.err
}

type fakeDevices struct {
	eui   string
	ttsID string
	err   error
}

func (d *fakeDevices) DevEUI(context.Context, string, string) (string, error) {
	if d.eui == "" && d.err == nil {
		return "", ErrNoDevEUI
	}
	return d.eui, d.err
}

func (d *fakeDevices) TTSDeviceID(context.Context, string, string) (string, error) {
	if d.ttsID == "" && d.err == nil {
		return "", ErrNoDeviceID
	}
	return d.ttsID, d.err
}

type enqueueCall struct {
	target  connectorspec.LoRaWANDownlinkTarget
	eui     string
	params  Params
	expires time.Time
}

type fakeEnqueuer struct {
	calls []enqueueCall
	id    string
	err   error
}

func (e *fakeEnqueuer) Enqueue(_ context.Context, t connectorspec.LoRaWANDownlinkTarget, eui string, p Params, expires time.Time) (string, error) {
	e.calls = append(e.calls, enqueueCall{t, eui, p, expires})
	return e.id, e.err
}

type counts struct {
	mu sync.Mutex
	m  map[string]int
}

func (c *counts) Count(r string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]int{}
	}
	c.m[r]++
}

func (c *counts) get(r string) int { c.mu.Lock(); defer c.mu.Unlock(); return c.m[r] }

type rig struct {
	a       *Adapter
	resp    *respWriter
	claim   *fakeClaimer
	links   *fakeLinks
	devices *fakeDevices
	send    *fakeEnqueuer
	m       *counts
	now     time.Time
}

func newRig(t *testing.T, mut func(*Options)) *rig {
	t.Helper()
	r := &rig{
		resp:    &respWriter{},
		claim:   &fakeClaimer{nonce: "claimed-nonce", won: true},
		links:   &fakeLinks{target: connectorspec.LoRaWANDownlinkTarget{NetworkServer: connectorspec.NetworkServerChirpStackV4, Integration: "cs1", APIKey: "k"}},
		devices: &fakeDevices{eui: "0102030405060708"},
		send:    &fakeEnqueuer{id: "q-1"},
		m:       &counts{},
		now:     time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
	}
	opts := Options{Workers: 1, ReadPacer: core.NewReadPacer(nil, "test"), Now: func() time.Time { return r.now },
		ResponseAttempts: 1, ResponseBackoff: time.Millisecond}
	if mut != nil {
		mut(&opts)
	}
	r.a = New(&nopReader{}, r.resp, r.claim, r.links, r.devices, r.send, r.m, opts)
	return r
}

type nopReader struct{}

func (nopReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	<-ctx.Done()
	return messaging.Message{}, ctx.Err()
}
func (nopReader) HandleResponse(error) {}

const subject = "inst.acme.device-commands.dev-1"

func envelope(t *testing.T, payload string) []byte {
	t.Helper()
	env := map[string]any{"token": "cmd-1", "deviceToken": "dev-1", "name": "setLed", "dispatchNonce": "env-nonce"}
	if payload != "" {
		env["payload"] = json.RawMessage(payload)
	}
	b, err := json.Marshal(env)
	require.NoError(t, err)
	return b
}

func routed() map[string]string {
	return map[string]string{
		cmddownlink.HeaderTransport: "integration",
		cmddownlink.HeaderSource:    "integration:cs1",
	}
}

func (r *rig) handle(t *testing.T, subj string, value []byte, headers map[string]string) *fakeAck {
	t.Helper()
	ack := &fakeAck{}
	r.a.handle(context.Background(), messaging.NewConsumedMessage(subj, value, 1, headers, ack))
	return ack
}

const goodParams = `{"fPort":10,"hex":"0a0b"}`

// ---- the happy path ----

func TestQueuedDownlinkIsAnsweredSuccessWithTheClaimNonce(t *testing.T) {
	r := newRig(t, nil)
	ack := r.handle(t, subject, envelope(t, goodParams), routed())

	require.True(t, ack.acked)
	require.Equal(t, 1, r.claim.calls)
	require.Equal(t, "env-nonce", r.claim.gotNonce, "the claim quotes the envelope's nonce")
	require.Equal(t, "acme", r.claim.gotTenant)
	require.Len(t, r.send.calls, 1)
	require.Equal(t, "0102030405060708", r.send.calls[0].eui)
	require.Equal(t, Params{FPort: 10, Data: []byte{0x0a, 0x0b}}, r.send.calls[0].params)

	require.Len(t, r.resp.envs, 1)
	e := r.resp.envs[0]
	require.Equal(t, []string{"dev-1"}, r.resp.devices, "answered on the device's own response subject")
	require.True(t, e.Success)
	require.Equal(t, "cmd-1", e.CommandToken)
	require.Equal(t, "claimed-nonce", e.DispatchNonce, "the response quotes the nonce the claim returned, never the envelope's")
	require.Empty(t, e.Reach)
	require.JSONEq(t, `{"stage":"queued","queueItemId":"q-1"}`, *e.Payload)
	require.Equal(t, 1, r.m.get(ResultQueued))
}

// ---- the gates before the claim: each answers UNSENT quoting the envelope's nonce, and calls nothing ----

func TestRefusalsBeforeTheClaim(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(r *rig, headers map[string]string, payload *string)
		result  string
		reason  Reason
		textHas string
	}{
		{"no Dc-Source", func(r *rig, h map[string]string, p *string) { delete(h, cmddownlink.HeaderSource) },
			ResultRefusedNoRoute, ReasonNoRoute, "cannot be routed"},
		{"source is not an integration", func(r *rig, h map[string]string, p *string) { h[cmddownlink.HeaderSource] = "sparkplug:plant" },
			ResultRefusedNoRoute, ReasonNoRoute, "cannot be routed"},
		{"empty integration", func(r *rig, h map[string]string, p *string) { h[cmddownlink.HeaderSource] = "integration:" },
			ResultRefusedNoRoute, ReasonNoRoute, "cannot be routed"},
		{"expired", func(r *rig, h map[string]string, p *string) {
			h[cmddownlink.HeaderCommandExpires] = r.now.Add(-time.Second).Format(time.RFC3339)
		}, ResultRefusedExpired, ReasonExpired, "expired"},
		{"bad params", func(r *rig, h map[string]string, p *string) { *p = `{"fPort":0,"hex":"01"}` },
			ResultRefusedParams, ReasonInvalidParams, "fPort"},
		{"no params", func(r *rig, h map[string]string, p *string) { *p = `` },
			ResultRefusedParams, ReasonInvalidParams, "fPort"},
		{"confirmed is refused, never downgraded", func(r *rig, h map[string]string, p *string) {
			*p = `{"fPort":1,"hex":"01","confirmed":true}`
		}, ResultRefusedConfirm, ReasonUnsupported, "confirmed downlinks are not available"},
		{"no link", func(r *rig, h map[string]string, p *string) { r.links.err = ErrNoLink },
			ResultRefusedNoLink, ReasonNoLink, "no lorawan_downlink connector"},
		{"ambiguous link", func(r *rig, h map[string]string, p *string) { r.links.err = ErrAmbiguous },
			ResultRefusedNoLink, ReasonAmbiguousLink, "more than one"},
		{"unusable link", func(r *rig, h map[string]string, p *string) {
			r.links.err = errors.New("wrapped: " + ErrBadLink.Error())
		},
			ResultRefusedNoLink, ReasonLinkUnreadable, ""},
		{"no DevEUI", func(r *rig, h map[string]string, p *string) { r.devices.err = ErrNoDevEUI },
			ResultRefusedNoDevEUI, ReasonNoDevEUI, "lorawan.deveui"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			h := routed()
			payload := goodParams
			c.mut(r, h, &payload)
			if c.name == "unusable link" {
				r.links.err = ErrBadLink
			}
			ack := r.handle(t, subject, envelope(t, payload), h)

			require.True(t, ack.acked, "a refusal that was answered is acked")
			require.Zero(t, r.claim.calls, "no claim for a command that will not be sent")
			require.Empty(t, r.send.calls, "nothing is sent")
			require.Len(t, r.resp.envs, 1)
			e := r.resp.envs[0]
			require.False(t, e.Success)
			require.Equal(t, cmddownlink.ReachUnsent, e.Reach, "proven not delivered, safe to retry")
			require.Equal(t, "env-nonce", e.DispatchNonce, "no claim was made, so the envelope's nonce")
			require.JSONEq(t, `{"reason":"`+string(c.reason)+`"}`, *e.Payload)
			require.Contains(t, *e.Error, c.textHas)
			require.Equal(t, 1, r.m.get(c.result))
		})
	}
}

// The command's expiry is handed to the network server (it drops the item itself) and is checked
// again after the claim.
func TestExpiryReachesTheNetworkServerAndIsRecheckedAfterTheClaim(t *testing.T) {
	r := newRig(t, nil)
	h := routed()
	exp := r.now.Add(time.Hour)
	h[cmddownlink.HeaderCommandExpires] = exp.Format(time.RFC3339)
	r.handle(t, subject, envelope(t, goodParams), h)
	require.Len(t, r.send.calls, 1)
	require.True(t, r.send.calls[0].expires.Equal(exp))

	// Lookups and the claim take time: the clock passes the expiry while the claim is made.
	r2 := newRig(t, nil)
	h2 := routed()
	h2[cmddownlink.HeaderCommandExpires] = r2.now.Add(time.Second).Format(time.RFC3339)
	r2.claim.onClaim = func() { r2.now = r2.now.Add(time.Minute) }
	ack := r2.handle(t, subject, envelope(t, goodParams), h2)
	require.True(t, ack.acked)
	require.Empty(t, r2.send.calls, "expired during the claim: nothing is sent")
	require.Len(t, r2.resp.envs, 1)
	e := r2.resp.envs[0]
	require.Equal(t, cmddownlink.ReachUnsent, e.Reach)
	require.Equal(t, "claimed-nonce", e.DispatchNonce, "the row is claimed, so the rotated nonce")
	require.JSONEq(t, `{"reason":"EXPIRED"}`, *e.Payload)
}

func TestIntegrationIsReadFromTheSourceAndNothingElse(t *testing.T) {
	r := newRig(t, nil)
	h := routed()
	h[cmddownlink.HeaderSource] = "integration:other-int"
	r.handle(t, subject, envelope(t, goodParams), h)
	require.Equal(t, []string{"other-int"}, r.links.asked)
}

// ---- transient reads leave the message for redelivery ----

func TestTransientLookupFailuresLeaveTheMessageUnackedAndSendNothing(t *testing.T) {
	for _, name := range []string{"link", "deveui"} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			if name == "link" {
				r.links.err = errors.New("db down")
			} else {
				r.devices.err = errors.New("device-management down")
			}
			ack := r.handle(t, subject, envelope(t, goodParams), routed())
			require.False(t, ack.acked)
			require.Empty(t, r.resp.envs, "no answer: a transient failure is not an outcome")
			require.Zero(t, r.claim.calls)
			require.Empty(t, r.send.calls)
			require.Equal(t, 1, r.m.get(ResultLookupFailed))
		})
	}
}

// ---- the claim is the only thing standing between a redelivery and a second downlink ----

func TestLostClaimSendsNothingAndAcks(t *testing.T) {
	r := newRig(t, nil)
	r.claim.won = false
	ack := r.handle(t, subject, envelope(t, goodParams), routed())
	require.True(t, ack.acked)
	require.Empty(t, r.send.calls, "a lost claim means another replica (or a re-arm) owns the dispatch")
	require.Empty(t, r.resp.envs)
	require.Equal(t, 1, r.m.get(ResultClaimLost))
}

func TestClaimErrorSendsNothingAndLeavesTheMessage(t *testing.T) {
	r := newRig(t, nil)
	r.claim.err = errors.New("command-delivery unreachable")
	ack := r.handle(t, subject, envelope(t, goodParams), routed())
	require.False(t, ack.acked)
	require.Empty(t, r.send.calls)
	require.Empty(t, r.resp.envs)
	require.Equal(t, 1, r.m.get(ResultClaimFailed))
}

func TestRedeliveryAfterAClaimedSendDoesNotSendAgain(t *testing.T) {
	r := newRig(t, nil)
	r.handle(t, subject, envelope(t, goodParams), routed())
	require.Len(t, r.send.calls, 1)
	// The broker redelivers the same envelope (the answer was slow). Command-delivery has rotated
	// the nonce, so the claim is lost.
	r.claim.won = false
	r.handle(t, subject, envelope(t, goodParams), routed())
	require.Len(t, r.send.calls, 1, "exactly one network call for one dispatch")
}

// ---- the network server's answer ----

func TestSendFailuresAreAnsweredByHowFarTheyGot(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		reach  string
		result string
		reason Reason
	}{
		{"refused", &SendError{Reach: cmddownlink.ReachUnsent, Text: "the network server refused the downlink (HTTP 400)"},
			cmddownlink.ReachUnsent, ResultUnsent, ReasonNetworkServer},
		{"maybe queued", &SendError{Reach: cmddownlink.ReachUnanswered, Text: "may have been queued"},
			cmddownlink.ReachUnanswered, ResultUnanswered, ReasonNetworkServer},
		{"egress", &SendError{Reach: cmddownlink.ReachUnsent, Blocked: true, Text: "egress"},
			cmddownlink.ReachUnsent, ResultUnsent, ReasonEgressBlocked},
		{"unclassified error is treated as maybe-queued", errors.New("something"),
			cmddownlink.ReachUnanswered, ResultUnanswered, ReasonNetworkServer},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			r.send.err = c.err
			ack := r.handle(t, subject, envelope(t, goodParams), routed())
			require.True(t, ack.acked)
			require.Len(t, r.send.calls, 1)
			e := r.resp.envs[0]
			require.False(t, e.Success)
			require.Equal(t, c.reach, e.Reach)
			require.Equal(t, "claimed-nonce", e.DispatchNonce)
			require.JSONEq(t, `{"reason":"`+string(c.reason)+`"}`, *e.Payload)
			require.Equal(t, 1, r.m.get(c.result))
		})
	}
}

func TestAnUnpublishableAnswerLeavesTheMessage(t *testing.T) {
	r := newRig(t, nil)
	r.resp.fail = errors.New("broker down")
	ack := r.handle(t, subject, envelope(t, goodParams), routed())
	require.False(t, ack.acked, "a redelivery loses the claim and acks; it never re-sends")
	require.Len(t, r.send.calls, 1)
	require.Equal(t, 1, r.m.get(ResultResponseFailed))
}

// ---- routing and poison ----

func TestAnotherTransportsCommandIsAckedAndIgnored(t *testing.T) {
	for _, h := range []map[string]string{
		{cmddownlink.HeaderTransport: "sparkplug"},
		{cmddownlink.HeaderTransport: "lwm2m"},
		{}, // no header at all: unknown is not "mine" for an adapter that cannot name the integration
		nil,
	} {
		r := newRig(t, nil)
		ack := r.handle(t, subject, envelope(t, goodParams), h)
		require.True(t, ack.acked)
		require.Zero(t, r.claim.calls)
		require.Empty(t, r.send.calls)
		require.Empty(t, r.resp.envs)
		require.Equal(t, 1, r.m.get(ResultNotServed))
	}
}

func TestPoisonIsAckedAndCounted(t *testing.T) {
	for name, c := range map[string]struct {
		subj  string
		value []byte
	}{
		"no tenant":        {"nosubject", envelope(t, goodParams)},
		"invalid tenant":   {"inst.Bad Tenant!.device-commands.d", envelope(t, goodParams)},
		"undecodable":      {subject, []byte("{")},
		"no command token": {subject, []byte(`{"deviceToken":"d"}`)},
		"no device token":  {subject, []byte(`{"token":"c"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			ack := r.handle(t, c.subj, c.value, routed())
			require.True(t, ack.acked)
			require.Empty(t, r.send.calls)
			require.Empty(t, r.resp.envs)
			require.Equal(t, 1, r.m.get(ResultPoison))
		})
	}
}

func TestDeletedTenantIsAckedWithoutAnyWork(t *testing.T) {
	r := newRig(t, func(o *Options) { o.TenantDeleted = func(string) bool { return true } })
	ack := r.handle(t, subject, envelope(t, goodParams), routed())
	require.True(t, ack.acked)
	require.Zero(t, r.claim.calls)
	require.Empty(t, r.send.calls)
	require.Empty(t, r.resp.envs)
	require.Equal(t, 1, r.m.get(ResultTenantGone))
}

func TestRateCeilingRefusesUnsentAndConsumesNoClaim(t *testing.T) {
	// A limiter with a burst of 1 and a negligible refill: the second command is over the ceiling.
	lim := core.NewTenantRateLimiter(core.StaticCeiling(0.0001, 1))
	r := newRig(t, func(o *Options) { o.Rate = lim })
	r.handle(t, subject, envelope(t, goodParams), routed())
	require.Len(t, r.send.calls, 1)

	ack := r.handle(t, subject, envelope(t, goodParams), routed())
	require.True(t, ack.acked)
	require.Len(t, r.send.calls, 1, "the second command was not sent")
	require.Equal(t, 1, r.claim.calls, "and it never claimed the dispatch")
	e := r.resp.envs[1]
	require.Equal(t, cmddownlink.ReachUnsent, e.Reach)
	require.JSONEq(t, `{"reason":"RATE_LIMITED"}`, *e.Payload)
}

func TestNewRefusesMissingCollaborators(t *testing.T) {
	require.Panics(t, func() { New(nil, nil, nil, nil, nil, nil, nil, Options{}) })
}

// ---- Start/Stop drive the read loop end to end ----

type queueReader struct {
	ch chan messaging.Message
}

func (q *queueReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	select {
	case m := <-q.ch:
		return m, nil
	case <-ctx.Done():
		return messaging.Message{}, ctx.Err()
	}
}
func (q *queueReader) HandleResponse(error) {}

func TestStartConsumesMessagesAndStopIsIdempotent(t *testing.T) {
	r := newRig(t, nil)
	q := &queueReader{ch: make(chan messaging.Message, 1)}
	r.a.reader = q
	ack := &fakeAck{}
	q.ch <- messaging.NewConsumedMessage(subject, envelope(t, goodParams), 1, routed(), ack)
	r.a.Start()
	require.Eventually(t, func() bool {
		r.resp.mu.Lock()
		defer r.resp.mu.Unlock()
		return len(r.resp.envs) == 1
	}, 5*time.Second, 5*time.Millisecond)
	r.a.Stop()
	r.a.Stop()
}
