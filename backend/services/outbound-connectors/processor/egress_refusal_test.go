// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/egress"
)

// A review found the terminal classification had NO test: replacing the
// `errors.Is(err, egress.ErrBlocked)` branch with `false` left the whole suite green,
// so the commit's central operational claim had nothing behind it. This is that
// evidence, and it is written as an end-to-end dispatch rather than a unit check on the
// branch, because what matters is the DISPOSITION the consumer then applies.

// blockedExecutor builds an executor over the real guard with no allowances, which is
// exactly production's default configuration.
func blockedExecutor(store *fakeSecretStore) *Executor {
	return NewExecutor(NewSecretResolver(store), nil, egress.NewGuard(nil), 5*time.Second)
}

// TestABlockedDestinationIsTerminalNotRetryable pins the classification. Left in the
// transient branch, a refusal would be retried to the redelivery cap and dead-lettered as
// an ordinary failure — five attempts at an address that cannot become public, and an
// operator reading "dead" when the truth is "you pointed this at a private address".
func TestABlockedDestinationIsTerminalNotRetryable(t *testing.T) {
	e := blockedExecutor(&fakeSecretStore{})
	res := e.Execute(context.Background(), &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme", IdempotencyKey: "idem-1",
		Payload:  `{}`,
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: "http://169.254.169.254/latest/meta-data/"},
	})

	if res.err == nil {
		t.Fatal("a dispatch to the metadata address must not succeed")
	}
	if res.retryable {
		t.Fatal("a blocked destination was classified retryable; it would burn the redelivery " +
			"cap and dead-letter as an ordinary failure")
	}
	if res.outcome != outcomeBlocked {
		t.Fatalf("outcome = %q, want %q — a blocked dispatch must be countable apart from an "+
			"invalid one, because they mean different things to whoever is looking",
			res.outcome, outcomeBlocked)
	}
}

// The counterweight. Without it, an executor that classified EVERY send failure as
// terminal would satisfy the test above while quietly removing redelivery for a
// briefly-down endpoint — a strictly worse failure, because it is silent.
func TestAnOrdinaryUnreachableEndpointIsStillRetryable(t *testing.T) {
	// An explicitly allowed address that nothing answers on. The allowance is what makes
	// the test say what it means: the guard permits this destination, so the failure that
	// follows comes from the network rather than from the boundary, and the classification
	// under test is the one for a real endpoint being down.
	guard := egress.NewGuard([]netip.Prefix{netip.MustParsePrefix("192.0.2.1/32")})
	e := NewExecutor(NewSecretResolver(&fakeSecretStore{}), nil, guard, 200*time.Millisecond)

	res := e.Execute(context.Background(), &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme", IdempotencyKey: "idem-2",
		Payload:  `{}`,
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: "http://192.0.2.1/hook"},
	})

	if res.err == nil {
		t.Skip("192.0.2.1 answered; the network this test runs on is not what it assumes")
	}
	if !res.retryable {
		t.Fatalf("an unreachable-but-permitted endpoint was classified terminal (%q); a "+
			"briefly-down endpoint must still be redelivered", res.outcome)
	}
}

// The executor must deliver through the guard it was constructed with. A constructor that
// ignored it would fall back to a guard with no allowances — also refusing, so every test
// above would still pass while the operator's configured allowances silently did nothing.
// The evidence is the delivery itself: the same loopback endpoint is reached through a
// guard that allows it and refused through one that does not.
func TestTheExecutorUsesTheGuardItWasGiven(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	req := &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme", IdempotencyKey: "idem-3",
		Payload: `{}`, HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL},
	}

	allowed := NewExecutor(NewSecretResolver(&fakeSecretStore{}), nil,
		egress.NewGuard([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}), 5*time.Second)
	res := allowed.Execute(context.Background(), req)
	if res.outcome != outcomeSent || hits.Load() != 1 {
		t.Fatalf("through a guard allowing loopback: outcome %q, %d request(s), err %v; want sent, 1",
			res.outcome, hits.Load(), res.err)
	}

	refused := NewExecutor(NewSecretResolver(&fakeSecretStore{}), nil, egress.NewGuard(nil), 5*time.Second)
	res = refused.Execute(context.Background(), req)
	if res.outcome != outcomeBlocked || hits.Load() != 1 {
		t.Fatalf("through a guard with no allowances: outcome %q, %d request(s); want blocked, still 1",
			res.outcome, hits.Load())
	}
}
