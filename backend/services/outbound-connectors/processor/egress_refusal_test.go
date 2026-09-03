// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/http"
	"net/netip"
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

// blockedExecutor builds an executor whose client refuses everything the guard refuses —
// the real guard, with no allowances, which is exactly production's configuration.
func blockedExecutor(store *fakeSecretStore) *Executor {
	return NewExecutor(NewSecretResolver(store), nil,
		&http.Client{Transport: egress.NewGuard(nil).Transport()}, 5*time.Second)
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
	e := NewExecutor(NewSecretResolver(&fakeSecretStore{}), nil,
		&http.Client{Transport: guard.Transport()}, 200*time.Millisecond)

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

// The executor must use the client it was constructed with. A constructor that ignored it
// would fall back to httpsink.DefaultClient — which is also guarded, so every test above
// would still pass while the operator's configured allowances silently did nothing.
func TestTheExecutorUsesTheClientItWasGiven(t *testing.T) {
	guard := egress.NewGuard([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
	client := &http.Client{Transport: guard.Transport()}
	e := NewExecutor(NewSecretResolver(&fakeSecretStore{}), nil, client, 5*time.Second)

	if e.client != client {
		t.Fatal("NewExecutor discarded the client, so the configured egress allowances would " +
			"never reach a dispatch")
	}
}
