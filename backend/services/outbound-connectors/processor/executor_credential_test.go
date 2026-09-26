// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
)

// An httpCall action that names a secret handle presents that secret as a Bearer token. These
// pin what happens when the handle does not produce one: the call is never sent without it,
// and a redelivery — which would ask the store the same question — is not requested.

func countingEndpoint(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func httpCallWithHandle(url string) *connectorwire.ConnectorDispatchRequest {
	return &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme", IdempotencyKey: "idem-1",
		Payload:  `{}`,
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: url, SecretRef: "webhook/auth"},
	}
}

// A handle naming no stored secret used to be retried to the redelivery cap and then
// dead-lettered as exhausted, which reads as "the endpoint kept failing".
func TestHTTPCallWithAMissingSecretIsTerminal(t *testing.T) {
	srv, hits := countingEndpoint(t)
	res := newTestExecutor(&fakeSecretStore{}).Execute(context.Background(), httpCallWithHandle(srv.URL))
	if res.outcome != outcomeInvalid || res.retryable {
		t.Fatalf("Execute = (%s, retryable=%v, %v), want invalid/terminal", res.outcome, res.retryable, res.err)
	}
	if res.err == nil || !strings.Contains(res.err.Error(), "no secret is stored") {
		t.Fatalf("err = %v, want it to say the handle names no stored secret", res.err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("the endpoint received %d request(s)", got)
	}
}

// A stored-but-empty value reached Send as "" and went out with no credential at all,
// reported as sent.
func TestHTTPCallWithAnEmptyStoredSecretSendsNothing(t *testing.T) {
	srv, hits := countingEndpoint(t)
	store := &fakeSecretStore{values: map[string][]byte{"webhook/auth": []byte("")}}
	res := newTestExecutor(store).Execute(context.Background(), httpCallWithHandle(srv.URL))
	if got := hits.Load(); got != 0 {
		t.Fatalf("the endpoint received %d unauthenticated request(s) (outcome %s)", got, res.outcome)
	}
	if res.outcome != outcomeInvalid || res.retryable {
		t.Fatalf("Execute = (%s, retryable=%v, %v), want invalid/terminal", res.outcome, res.retryable, res.err)
	}
	if res.err == nil || !strings.Contains(res.err.Error(), "auth refused") {
		t.Fatalf("err = %v, want an auth refusal", res.err)
	}
}
