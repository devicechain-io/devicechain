// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-notification-management/model"
)

// A webhook channel whose stored secret was missing used to POST with NO credential at all,
// and nothing said so: the shared HTTP sender applied auth only when the secret was
// non-empty, and the config had no field recording whether auth was intended. Every test in
// this file asserts on what reached the WIRE (a hit counter on a real listener) and on the
// refusal's reason — `err != nil` alone would also be satisfied by a bad URL.

// countingAdapter delegates to a real adapter and counts how many attempts the dispatcher
// made, which is what separates "refused once" from "retried to exhaustion".
type countingAdapter struct {
	inner ChannelAdapter
	calls int
}

func (c *countingAdapter) Deliver(ctx context.Context, channel *model.NotificationChannel,
	secret string, recipients []string, msg *RenderedNotification) error {
	c.calls++
	return c.inner.Deliver(ctx, channel, secret, recipients, msg)
}

// hitCountingServer is an endpoint that records every request it is sent and the
// Authorization header values each one carried.
func hitCountingServer(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Value) {
	t.Helper()
	var hits atomic.Int32
	var auth atomic.Value
	auth.Store([]string(nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth.Store(r.Header.Values("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &auth
}

func deliverWebhook(t *testing.T, srv *httptest.Server, config, secret string) error {
	t.Helper()
	adapter := &webhookAdapter{client: srv.Client()}
	channel := channelWith("hook", model.ChannelTypeWebhook, config)
	return adapter.Deliver(context.Background(), channel, secret, nil,
		&RenderedNotification{Payload: map[string]any{"text": "hi"}})
}

// The defect itself: a channel that DECLARES bearer auth, delivered with no secret, went
// out unauthenticated and reported success.
func TestWebhookDeclaringBearerWithNoSecretSendsNothing(t *testing.T) {
	srv, hits, _ := hitCountingServer(t)
	err := deliverWebhook(t, srv, `{"url":"`+srv.URL+`","auth":"bearer"}`, "")
	if got := hits.Load(); got != 0 {
		t.Fatalf("the endpoint received %d request(s) from a channel whose declared credential is missing", got)
	}
	if err == nil || !strings.Contains(err.Error(), "auth refused") {
		t.Fatalf("err = %v, want an auth refusal", err)
	}
}

// The other direction: "none" with a stored secret is the same misconfiguration (a tenant
// believing the endpoint is authenticated), and on main it presented the secret anyway.
func TestWebhookDeclaringNoneWithAStoredSecretSendsNothing(t *testing.T) {
	srv, hits, auth := hitCountingServer(t)
	err := deliverWebhook(t, srv, `{"url":"`+srv.URL+`","auth":"none"}`, "s")
	if got := hits.Load(); got != 0 {
		t.Fatalf("the endpoint received %d request(s) (Authorization %v) from a channel declaring no auth",
			got, auth.Load())
	}
	if err == nil || !strings.Contains(err.Error(), "auth refused") {
		t.Fatalf("err = %v, want an auth refusal", err)
	}
}

// A channel saved before `auth` existed says nothing about intent, so it is not guessed at.
func TestWebhookWithoutAnAuthModeSendsNothing(t *testing.T) {
	srv, hits, _ := hitCountingServer(t)
	err := deliverWebhook(t, srv, `{"url":"`+srv.URL+`"}`, "")
	if got := hits.Load(); got != 0 {
		t.Fatalf("the endpoint received %d request(s) from a channel with no stated auth mode", got)
	}
	if err == nil || !strings.Contains(err.Error(), "missing auth") {
		t.Fatalf("err = %v, want a refusal naming the missing auth key", err)
	}
}

// The counterweight that rules out "refuse every secretless webhook": an explicitly
// anonymous channel (a Slack incoming webhook, whose credential is in the URL) still
// delivers, and carries NO Authorization header.
func TestWebhookAnonymousPostsWithNoAuthorizationHeader(t *testing.T) {
	srv, hits, auth := hitCountingServer(t)
	if err := deliverWebhook(t, srv, `{"url":"`+srv.URL+`","auth":"none"}`, ""); err != nil {
		t.Fatalf("an anonymous channel was refused: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1", got)
	}
	if got := auth.Load().([]string); len(got) != 0 {
		t.Fatalf("an anonymous channel sent Authorization %q", got)
	}
}

// Terminal: the dispatcher makes ONE attempt and classifies it refused, instead of spending
// every attempt and logging "exhausted attempts".
func TestACredentialRefusalIsNotRetried(t *testing.T) {
	srv, hits, _ := hitCountingServer(t)
	ca := &countingAdapter{inner: &webhookAdapter{client: srv.Client()}}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeWebhook: ca})
	n.timeout = 2 * time.Second
	d := delivery{channel: channelWith("hook", model.ChannelTypeWebhook, `{"url":"`+srv.URL+`","auth":"bearer"}`)}

	if got := n.deliverWithRetry(context.Background(), d, &RenderedNotification{Payload: map[string]any{}}); got != deliveryRefused {
		t.Fatalf("deliverWithRetry = %v, want deliveryRefused (%v)", got, deliveryRefused)
	}
	if ca.calls != 1 {
		t.Fatalf("a credential refusal was attempted %d times; redelivery sends the same config", ca.calls)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("the endpoint received %d request(s)", got)
	}
}

// silentSMTPListener accepts TCP connections and never sends a greeting, counting accepts.
// A dial reaching it is exactly what "refused before dialing" must not do.
func silentSMTPListener(t *testing.T) (port int, accepts *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	accepts = &atomic.Int32{}
	done := make(chan struct{})
	var conns []net.Conn
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			conns = append(conns, c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return ln.Addr().(*net.TCPAddr).Port, accepts
}

func loopbackSMTPAdapter(t *testing.T) *smtpAdapter {
	t.Helper()
	return &smtpAdapter{guard: egress.NewGuard([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})}
}

func smtpUsernameOnlyChannel(port int) *model.NotificationChannel {
	return channelWith("mail", model.ChannelTypeSMTP, fmt.Sprintf(
		`{"host":"127.0.0.1","port":%d,"from":"a@x.invalid","username":"u","security":"none"}`, port))
}

// SMTP knew this case already, but learned it only AFTER a TCP connect (and, by default, a
// STARTTLS handshake) to a tenant-chosen host. Recipients are set so the earlier
// no-recipients refusal cannot answer for it.
func TestSMTPUsernameWithoutSecretIsRefusedBeforeDialing(t *testing.T) {
	port, accepts := silentSMTPListener(t)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := loopbackSMTPAdapter(t).Deliver(ctx, smtpUsernameOnlyChannel(port), "",
		[]string{"ops@x.invalid"}, &RenderedNotification{Subject: "s", TextBody: "b"})
	if got := accepts.Load(); got != 0 {
		t.Fatalf("the mail server was connected to %d time(s) for a channel with no password", got)
	}
	if err == nil || !strings.Contains(err.Error(), "no secret") {
		t.Fatalf("err = %v, want the missing-secret refusal", err)
	}
}

// And it is terminal: one attempt, classified refused.
func TestSMTPCredentialRefusalIsNotRetried(t *testing.T) {
	port, accepts := silentSMTPListener(t)
	ca := &countingAdapter{inner: loopbackSMTPAdapter(t)}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: ca})
	n.timeout = 200 * time.Millisecond
	d := delivery{channel: smtpUsernameOnlyChannel(port), recipients: []string{"ops@x.invalid"}}

	if got := n.deliverWithRetry(context.Background(), d, &RenderedNotification{Subject: "s", TextBody: "b"}); got != deliveryRefused {
		t.Fatalf("deliverWithRetry = %v, want deliveryRefused (%v)", got, deliveryRefused)
	}
	if ca.calls != 1 {
		t.Fatalf("a credential refusal was attempted %d times", ca.calls)
	}
	if got := accepts.Load(); got != 0 {
		t.Fatalf("the mail server was connected to %d time(s)", got)
	}
}
