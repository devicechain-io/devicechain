// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// newTestConsumer builds a consumer whose executor sends to an httptest server (or nil for cases
// that never execute), with egress rate limiting OFF (nil limiter). ms is nil so metrics are
// nil-safe no-ops.
func newTestConsumer(dead messaging.MessageWriter, store *fakeSecretStore) *DispatchConsumer {
	e := NewExecutor(NewSecretResolver(store), nil, 5*time.Second)
	e.client = loopbackClient()
	return NewDispatchConsumer(nil, &fakeReader{}, dead, e, nil, 5*time.Second, nil, 1, 1)
}

// newTestConsumerWithRate builds a consumer with an egress rate limiter and wait budget, to exercise
// the SD-3 rate gate.
func newTestConsumerWithRate(dead messaging.MessageWriter, store *fakeSecretStore, rate *core.TenantRateLimiter, waitBudget time.Duration) *DispatchConsumer {
	e := NewExecutor(NewSecretResolver(store), nil, 5*time.Second)
	e.client = loopbackClient()
	return NewDispatchConsumer(nil, &fakeReader{}, dead, e, rate, waitBudget, nil, 1, 1)
}

// newTestConsumerWithGate builds a consumer carrying the ADR-077 lifecycle gate, with egress rate
// limiting off so the gate is the only thing that can refuse.
//
// It goes through the REAL constructor, which is what makes the lifecycle tests below cover the
// plumbing as well as the branch: assigning the field by struct literal instead would leave
// `tenantDeleted: tenantDeleted` deletable from NewDispatchConsumer with the whole suite still green.
func newTestConsumerWithGate(dead messaging.MessageWriter, store *fakeSecretStore, tenantDeleted func(string) bool) *DispatchConsumer {
	e := NewExecutor(NewSecretResolver(store), nil, 5*time.Second)
	e.client = loopbackClient()
	return NewDispatchConsumer(nil, &fakeReader{}, dead, e, nil, 5*time.Second, tenantDeleted, 1, 1)
}

// countingServer is an httptest server that records how many outbound sends actually reached it.
//
// The lifecycle tests assert on THIS rather than on the ack flag, because the property under test is
// not "the message was disposed of correctly" — it is "nothing left the platform". Those are
// different claims, and only one of them is the reason this gate exists.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
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

// httpDispatch is a well-formed httpCall dispatch for tenant "acme" pointed at url. Every lifecycle
// test below sends exactly this, so the only variable across them is the gate's answer.
func httpDispatch(t *testing.T, url string, ack messaging.Acknowledger) messaging.Message {
	t.Helper()
	return wireMsg(t, dispatchSubject, 1, &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme", RuleID: "r1",
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: url}}, ack)
}

// TestHandleDeletedTenantIsRefusedWithoutSending is the point of the gate: a dispatch for a tenant
// that has been deleted must not reach the endpoint.
//
// It asserts on the endpoint's own hit count rather than on the disposition, because this is the one
// refusal in the service whose failure is irreversible. Every other thing a deleted tenant can leave
// behind is data on our disks that the sweep reclaims on a later pass; a dispatch that gets out is a
// request that has already landed on somebody else's server, and no purge of ours can reach it.
func TestHandleDeletedTenantIsRefusedWithoutSending(t *testing.T) {
	srv, hits := countingServer(t)
	dead := &fakeWriter{}
	c := newTestConsumerWithGate(dead, &fakeSecretStore{}, func(string) bool { return true })
	ack := &fakeAck{}

	c.handle(context.Background(), httpDispatch(t, srv.URL, ack))

	if got := hits.Load(); got != 0 {
		t.Fatalf("a deleted tenant's dispatch reached the endpoint %d time(s); nothing may leave the platform for a deleted tenant", got)
	}
	if !ack.acked {
		t.Fatalf("a refused dispatch must be acked so it stops redelivering, got acked=%v", ack.acked)
	}
}

// TestHandleLiveTenantIsStillDispatched is the counterweight, and without it every assertion above is
// satisfied by a gate that refuses everything — which would be a total outbound outage passing as a
// working feature.
func TestHandleLiveTenantIsStillDispatched(t *testing.T) {
	srv, hits := countingServer(t)
	dead := &fakeWriter{}
	c := newTestConsumerWithGate(dead, &fakeSecretStore{}, func(string) bool { return false })
	ack := &fakeAck{}

	c.handle(context.Background(), httpDispatch(t, srv.URL, ack))

	if got := hits.Load(); got != 1 {
		t.Fatalf("a live tenant's dispatch must still be sent; endpoint saw %d request(s)", got)
	}
	if !ack.acked {
		t.Fatalf("a delivered dispatch must be acked, got acked=%v", ack.acked)
	}
}

// TestHandleUnconfiguredGateAdmits pins the fail-open default. NewTenantLifecycleGate returns nil on
// an instance with no user-management endpoint configured, and this service must then behave exactly
// as it did before the gate existed rather than panic on the nil call or refuse everything.
func TestHandleUnconfiguredGateAdmits(t *testing.T) {
	srv, hits := countingServer(t)
	dead := &fakeWriter{}
	c := newTestConsumerWithGate(dead, &fakeSecretStore{}, nil)
	ack := &fakeAck{}

	c.handle(context.Background(), httpDispatch(t, srv.URL, ack))

	if got := hits.Load(); got != 1 {
		t.Fatalf("an unconfigured gate must admit (fail open); endpoint saw %d request(s)", got)
	}
}

// TestTheLifecycleGateIsAskedAboutTheSubjectTenant pins WHICH tenant the gate is asked about.
//
// A gate handed the wrong string still refuses and still admits, so every test above passes with the
// argument wrong — and the failure it would hide is the bad one: asking about a constant, or about a
// field that is empty on some dispatch kinds, silently gates nothing at all.
func TestTheLifecycleGateIsAskedAboutTheSubjectTenant(t *testing.T) {
	srv, _ := countingServer(t)
	var asked []string
	dead := &fakeWriter{}
	c := newTestConsumerWithGate(dead, &fakeSecretStore{}, func(tenant string) bool {
		asked = append(asked, tenant)
		return false
	})

	c.handle(context.Background(), httpDispatch(t, srv.URL, &fakeAck{}))

	if len(asked) != 1 || asked[0] != "acme" {
		t.Fatalf("the gate must be asked exactly once about the dispatch's own tenant, got %v", asked)
	}
}

// TestHandleDeletedTenantIsRefusedBeforeTheRateWait pins the gate's POSITION relative to the rate
// gate, which is a correctness property and not a micro-optimization.
//
// The rate gate dead-letters what it sheds, onto {instance}.{tenant}.connector-dispatch.dead — a
// TENANT-SCOPED subject, i.e. inside the namespace the purge is reclaiming. The broker store's next
// pass would erase those messages and report rows erased, and a store that erases rows loses its
// clean-since, restarting the settle window the purge cannot complete without. So a deleted tenant
// whose dispatches were rate-shed rather than refused would hold its own purge open for as long as
// its backlog took to drain.
//
// The two orderings are told apart by whether anything was written to the dead-letter subject, not by
// timing: the limiter here has no token to give, so if the rate gate ran first this would dead-letter.
func TestHandleDeletedTenantIsRefusedBeforeTheRateWait(t *testing.T) {
	// 1 burst token, next token ~1000s away; drain the burst so the dispatch under test cannot get one.
	rl := core.NewTenantRateLimiter(func(string) (float64, int) { return 0.001, 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_ = rl.Wait(ctx, "acme")
	cancel()

	srv, hits := countingServer(t)
	dead := &fakeWriter{}
	e := NewExecutor(NewSecretResolver(&fakeSecretStore{}), nil, 5*time.Second)
	e.client = loopbackClient()
	c := NewDispatchConsumer(nil, &fakeReader{}, dead, e, rl, 40*time.Millisecond,
		func(string) bool { return true }, 1, 1)
	ack := &fakeAck{}

	c.handle(context.Background(), httpDispatch(t, srv.URL, ack))

	if len(dead.written()) != 0 {
		t.Fatalf("refusing a deleted tenant must write NOTHING to its own dead-letter subject (it would "+
			"restart the purge's settle window); wrote %d", len(dead.written()))
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("a deleted tenant's dispatch reached the endpoint %d time(s)", got)
	}
	if !ack.acked {
		t.Fatalf("a refused dispatch must be acked, got acked=%v", ack.acked)
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

// TestTheRefusalIsRecordedAsTenantDeleted asserts the metric OUTCOME, not just that the message was
// disposed of.
//
// It earns its place because the design nominates that counter as the operator's only signal: the
// refusal logs at Debug precisely so a wholesale tenant deletion cannot flood the log, which leaves
// connector_dispatch_total{outcome="tenant_deleted"} as the one place a refusal is visible. A
// refusal recorded as "sent" would tell an operator a deleted tenant's dispatches were DELIVERED —
// the exact opposite of what happened — and every other assertion in this file would stay green.
//
// The counter is built here rather than through newDispatchMetrics(ms) because that path registers
// into the global Prometheus registry, where a second construction in this package panics.
func TestTheRefusalIsRecordedAsTenantDeleted(t *testing.T) {
	srv, _ := countingServer(t)
	vec := prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "test_connector_dispatch_total"}, []string{"action", "outcome"})
	dead := &fakeWriter{}
	c := newTestConsumerWithGate(dead, &fakeSecretStore{}, func(string) bool { return true })
	c.metrics = &dispatchMetrics{dispatched: vec}

	c.handle(context.Background(), httpDispatch(t, srv.URL, &fakeAck{}))

	if got := testutil.ToFloat64(vec.WithLabelValues(connectorwire.ConnectorKindHTTPCall, outcomeTenantDeleted)); got != 1 {
		t.Fatalf("a refusal must be counted as %q for its own action; got %v", outcomeTenantDeleted, got)
	}
	if got := testutil.ToFloat64(vec.WithLabelValues(connectorwire.ConnectorKindHTTPCall, outcomeSent)); got != 0 {
		t.Fatalf("a refusal must never be counted as %q — that reads as delivered; got %v", outcomeSent, got)
	}
}

// TestAMalformedTenantIsDroppedWithoutConsultingTheGate pins the gate's UPPER bound, which the
// before-the-rate-wait test does not: that one proves the gate is not too late, this one proves it
// is not too early.
//
// The subject tenant is grammar-validated (ADR-042) before anything is allowed to key state by it,
// and the gate keys a resolver cache — one entry and one user-management query per distinct value.
// Hoisted above that validation, a malformed multi-KB subject segment (reachable with broker write
// access, the same threat the validation itself defends) would seed unbounded cache cardinality and
// a query storm. Nothing else in this file would notice the hoist.
func TestAMalformedTenantIsDroppedWithoutConsultingTheGate(t *testing.T) {
	var asked []string
	dead := &fakeWriter{}
	c := newTestConsumerWithGate(dead, &fakeSecretStore{}, func(tenant string) bool {
		asked = append(asked, tenant)
		return false
	})
	ack := &fakeAck{}

	c.handle(context.Background(),
		messaging.NewConsumedMessage("inst.bad tenant!.connector-dispatch", []byte("{}"), 1, nil, ack))

	if len(asked) != 0 {
		t.Fatalf("the gate must not be consulted for a tenant that failed token validation; it was asked about %v", asked)
	}
	if !ack.acked {
		t.Fatalf("a malformed-tenant dispatch must still be dropped as poison, got acked=%v", ack.acked)
	}
}
