// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// newTestConsumer builds a consumer whose executor sends to an httptest server (or nil for cases
// that never execute), with egress rate limiting OFF (nil limiter). ms is nil so metrics are
// nil-safe no-ops.
func newTestConsumer(dead messaging.MessageWriter, store *fakeSecretStore) *DispatchConsumer {
	e := NewExecutor(NewSecretResolver(store), nil, 5*time.Second)
	return NewDispatchConsumer(nil, &fakeReader{}, dead, e, nil, 5*time.Second, 1, 1)
}

// newTestConsumerWithRate builds a consumer with an egress rate limiter and wait budget, to exercise
// the SD-3 rate gate.
func newTestConsumerWithRate(dead messaging.MessageWriter, store *fakeSecretStore, rate *core.TenantRateLimiter, waitBudget time.Duration) *DispatchConsumer {
	e := NewExecutor(NewSecretResolver(store), nil, 5*time.Second)
	return NewDispatchConsumer(nil, &fakeReader{}, dead, e, rate, waitBudget, 1, 1)
}

func wireMsg(t *testing.T, subject string, numDelivered int, req *connectorwire.ConnectorDispatchRequest, ack messaging.Acknowledger) messaging.Message {
	t.Helper()
	b, err := connectorwire.MarshalConnectorDispatchRequest(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return messaging.NewConsumedMessage(subject, b, numDelivered, nil, ack)
}

const dispatchSubject = "inst.acme.connector-dispatch"

// TestHandleSuccessAcks executes a good httpCall and acks it, with no dead-letter.
func TestHandleSuccessAcks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	dead := &fakeWriter{}
	c := newTestConsumer(dead, &fakeSecretStore{})
	ack := &fakeAck{}
	msg := wireMsg(t, dispatchSubject, 1, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL}}, ack)

	c.handle(context.Background(), msg)
	if !ack.acked {
		t.Fatalf("want acked, got acked=%v", ack.acked)
	}
	if len(dead.written()) != 0 {
		t.Fatalf("a successful send must not dead-letter")
	}
}

// TestHandleNoTenantDropped drops a message whose subject carries no parseable tenant.
func TestHandleNoTenantDropped(t *testing.T) {
	dead := &fakeWriter{}
	c := newTestConsumer(dead, &fakeSecretStore{})
	ack := &fakeAck{}
	c.handle(context.Background(), messaging.NewConsumedMessage("no-tenant", []byte("{}"), 1, nil, ack))
	if !ack.acked || len(dead.written()) != 0 {
		t.Fatalf("no-tenant poison should be dropped (acked), got acked=%v dead=%d", ack.acked, len(dead.written()))
	}
}

// TestHandleUndecodableDropped drops an undecodable body.
func TestHandleUndecodableDropped(t *testing.T) {
	dead := &fakeWriter{}
	c := newTestConsumer(dead, &fakeSecretStore{})
	ack := &fakeAck{}
	c.handle(context.Background(), messaging.NewConsumedMessage(dispatchSubject, []byte("{bad"), 1, nil, ack))
	if !ack.acked || len(dead.written()) != 0 {
		t.Fatalf("undecodable poison should be dropped (acked), got acked=%v dead=%d", ack.acked, len(dead.written()))
	}
}

// TestHandleStructurallyInvalidDropped drops a decodable but structurally-invalid dispatch (a kind
// with no matching variant — the forged-projection shape).
func TestHandleStructurallyInvalidDropped(t *testing.T) {
	dead := &fakeWriter{}
	c := newTestConsumer(dead, &fakeSecretStore{})
	ack := &fakeAck{}
	// httpCall kind with no HTTPCall config: connectorwire.Validate rejects it.
	msg := wireMsg(t, dispatchSubject, 1, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme"}, ack)
	c.handle(context.Background(), msg)
	if !ack.acked || len(dead.written()) != 0 {
		t.Fatalf("structurally-invalid poison should be dropped (acked), got acked=%v dead=%d", ack.acked, len(dead.written()))
	}
}

// TestHandleTenantMismatchDropped drops a message whose payload tenant disagrees with its subject.
func TestHandleTenantMismatchDropped(t *testing.T) {
	dead := &fakeWriter{}
	c := newTestConsumer(dead, &fakeSecretStore{})
	ack := &fakeAck{}
	msg := wireMsg(t, dispatchSubject, 1, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "globex",
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: "https://x/y"}}, ack)
	c.handle(context.Background(), msg)
	if !ack.acked || len(dead.written()) != 0 {
		t.Fatalf("tenant-mismatch forgery should be dropped (acked), got acked=%v dead=%d", ack.acked, len(dead.written()))
	}
}

// TestHandleTransientLeftUnackedBelowCap leaves a transient failure below the redelivery cap UNACKED
// (AckWait-paced retry, never an immediate nak that would burn MaxDeliver in ~1.4ms; no dead-letter).
func TestHandleTransientLeftUnackedBelowCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	dead := &fakeWriter{}
	c := newTestConsumer(dead, &fakeSecretStore{})
	ack := &fakeAck{}
	msg := wireMsg(t, dispatchSubject, 1, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL}}, ack)
	c.handle(context.Background(), msg)
	if ack.acked {
		t.Fatalf("transient below cap should be left unacked (AckWait retry), not acked: acked=%v", ack.acked)
	}
	if len(dead.written()) != 0 {
		t.Fatalf("must not dead-letter below the cap")
	}
}

// TestHandleTransientDeadLettersAtCap dead-letters (and acks) a transient failure once the redelivery
// cap is exhausted.
func TestHandleTransientDeadLettersAtCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	dead := &fakeWriter{}
	c := newTestConsumer(dead, &fakeSecretStore{})
	ack := &fakeAck{}
	msg := wireMsg(t, dispatchSubject, messaging.MaxDeliver, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL}}, ack)
	c.handle(context.Background(), msg)
	if !ack.acked {
		t.Fatalf("at the cap the message should be acked after dead-lettering, got acked=%v", ack.acked)
	}
	w := dead.written()
	if len(w) != 1 {
		t.Fatalf("want 1 dead-lettered message, got %d", len(w))
	}
	if dead.tenants[0] != "acme" {
		t.Fatalf("dead-letter must be scoped to the tenant, got %q", dead.tenants[0])
	}
}

// TestHandleUnsupportedDeadLetters dead-letters (and acks) a publish dispatch this build cannot run.
func TestHandleUnsupportedDeadLetters(t *testing.T) {
	dead := &fakeWriter{}
	c := newTestConsumer(dead, &fakeSecretStore{})
	ack := &fakeAck{}
	msg := wireMsg(t, dispatchSubject, 1, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindPublish, Tenant: "acme",
		Publish: &connectorwire.PublishDispatch{ConnectorRef: "kafka-main"}}, ack)
	c.handle(context.Background(), msg)
	if !ack.acked || len(dead.written()) != 1 {
		t.Fatalf("unsupported publish should dead-letter + ack, got acked=%v dead=%d", ack.acked, len(dead.written()))
	}
}

// TestHandleDeadLetterWriteFailureLeftUnackedBelowCap does NOT ack (avoids silent loss) when the
// dead-letter write fails BELOW the redelivery cap — the message is left unacked so JetStream
// redelivers (after AckWait) and dead-lettering is retried on the next attempt.
func TestHandleDeadLetterWriteFailureLeftUnackedBelowCap(t *testing.T) {
	dead := &fakeWriter{fail: context.DeadlineExceeded}
	c := newTestConsumer(dead, &fakeSecretStore{})
	ack := &fakeAck{}
	msg := wireMsg(t, dispatchSubject, 1, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindPublish, Tenant: "acme",
		Publish: &connectorwire.PublishDispatch{ConnectorRef: "kafka-main"}}, ack)
	c.handle(context.Background(), msg)
	if ack.acked {
		t.Fatalf("below the cap a failed dead-letter write must be left unacked (retry later), got acked=%v", ack.acked)
	}
}

// TestHandleDeadLetterWriteFailureAtCapAcksAsLoss records an explicit LOSS (and acks, since no
// redelivery will follow) rather than leaving it unacked, which JetStream would not redeliver past
// the cap — the message must never be stranded forever.
func TestHandleDeadLetterWriteFailureAtCapAcksAsLoss(t *testing.T) {
	dead := &fakeWriter{fail: context.DeadlineExceeded}
	c := newTestConsumer(dead, &fakeSecretStore{})
	ack := &fakeAck{}
	msg := wireMsg(t, dispatchSubject, messaging.MaxDeliver, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindPublish, Tenant: "acme",
		Publish: &connectorwire.PublishDispatch{ConnectorRef: "kafka-main"}}, ack)
	c.handle(context.Background(), msg)
	if !ack.acked {
		t.Fatalf("at the cap a failed dead-letter write must ack-as-loss (leaving it unacked would strand it), got acked=%v", ack.acked)
	}
}

// TestHandleMalformedTenantDropped drops (as poison) a dispatch whose subject tenant is present but
// not a valid token, before it can seed a rate-limiter bucket keyed by the malformed value.
func TestHandleMalformedTenantDropped(t *testing.T) {
	dead := &fakeWriter{}
	c := newTestConsumer(dead, &fakeSecretStore{})
	ack := &fakeAck{}
	// A subject whose tenant segment contains an illegal token character.
	c.handle(context.Background(), messaging.NewConsumedMessage("inst.bad tenant!.connector-dispatch", []byte("{}"), 1, nil, ack))
	if !ack.acked {
		t.Fatalf("a malformed-tenant dispatch must be dropped (acked), got acked=%v", ack.acked)
	}
	if len(dead.written()) != 0 {
		t.Fatalf("a malformed-tenant poison drop must not dead-letter")
	}
}

// TestHandleRateAdmitSends admits a dispatch under a generous rate ceiling and delivers it (the
// limiter present but never the bottleneck).
func TestHandleRateAdmitSends(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	rl := core.NewTenantRateLimiter(func(string) (float64, int) { return 1000, 1000 })
	dead := &fakeWriter{}
	c := newTestConsumerWithRate(dead, &fakeSecretStore{}, rl, 5*time.Second)
	ack := &fakeAck{}
	msg := wireMsg(t, dispatchSubject, 1, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL}}, ack)

	c.handle(context.Background(), msg)
	if !ack.acked {
		t.Fatalf("an admitted dispatch must send + ack, got acked=%v", ack.acked)
	}
	if len(dead.written()) != 0 {
		t.Fatalf("an admitted dispatch must not dead-letter")
	}
}

// TestHandleRateShedDeadLetters sheds a dispatch whose tenant cannot get a token within the wait
// budget: it is dead-lettered (not left for redelivery, so no poison-cap churn) and acked. The rate gate returns
// before the executor, so no outbound send occurs (the URL is unreachable, proving it is never
// dialed).
func TestHandleRateShedDeadLetters(t *testing.T) {
	// 1 burst token, next token ~1000s away; drain the burst so the dispatch under test cannot get one.
	rl := core.NewTenantRateLimiter(func(string) (float64, int) { return 0.001, 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_ = rl.Wait(ctx, "acme")
	cancel()

	dead := &fakeWriter{}
	c := newTestConsumerWithRate(dead, &fakeSecretStore{}, rl, 40*time.Millisecond)
	ack := &fakeAck{}
	msg := wireMsg(t, dispatchSubject, 1, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: "http://127.0.0.1:0/never-dialed"}}, ack)

	start := time.Now()
	c.handle(context.Background(), msg)
	if time.Since(start) > time.Second {
		t.Fatalf("shed should be fast (bounded by the wait budget), took %v", time.Since(start))
	}
	if !ack.acked {
		t.Fatalf("a rate-shed dispatch must be acked after dead-letter (never left for redelivery, to avoid poison-cap churn), got acked=%v", ack.acked)
	}
	if len(dead.written()) != 1 {
		t.Fatalf("a rate-shed dispatch must be written to the dead-letter subject once, got %d", len(dead.written()))
	}
}

// TestHandleRateShedBelowCapLeftUnackedOnDeadLetterWriteFailure confirms the shed reuses the terminal
// dead-letter path: below the redelivery cap, a dead-letter WRITE failure leaves the message unacked
// for a later (AckWait-paced) retry rather than stranding the shed message.
func TestHandleRateShedBelowCapLeftUnackedOnDeadLetterWriteFailure(t *testing.T) {
	rl := core.NewTenantRateLimiter(func(string) (float64, int) { return 0.001, 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_ = rl.Wait(ctx, "acme")
	cancel()

	dead := &fakeWriter{fail: context.DeadlineExceeded}
	c := newTestConsumerWithRate(dead, &fakeSecretStore{}, rl, 40*time.Millisecond)
	ack := &fakeAck{}
	msg := wireMsg(t, dispatchSubject, 1, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: "http://127.0.0.1:0/never-dialed"}}, ack)

	c.handle(context.Background(), msg)
	if ack.acked {
		t.Fatalf("below the cap a failed dead-letter write on a shed must be left unacked to retry, got acked=%v", ack.acked)
	}
}
