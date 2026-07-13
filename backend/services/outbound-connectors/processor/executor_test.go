// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
)

func newTestExecutor(store *fakeSecretStore) *Executor {
	return NewExecutor(NewSecretResolver(store), 10*time.Second)
}

// TestExecuteHTTPCallSuccess sends the rendered payload, presents the resolved secret as a Bearer
// token, stamps the idempotency key, and classifies the result as sent.
func TestExecuteHTTPCallSuccess(t *testing.T) {
	var gotAuth, gotIdem, gotBody, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		gotIdem = req.Header.Get("X-DC-Idempotency-Key")
		gotType = req.Header.Get("Content-Type")
		buf := make([]byte, req.ContentLength)
		req.Body.Read(buf)
		gotBody = string(buf)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &fakeSecretStore{values: map[string][]byte{"webhook/auth": []byte("tok-abc")}}
	e := newTestExecutor(store)
	req := &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme", IdempotencyKey: "idem-1",
		Payload:  `{"hello":"world"}`,
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL, SecretRef: "webhook/auth"},
	}
	res := e.Execute(context.Background(), req)
	if res.err != nil || res.outcome != outcomeSent {
		t.Fatalf("Execute = (%s,%v), want sent/nil", res.outcome, res.err)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Fatalf("Authorization = %q, want Bearer tok-abc", gotAuth)
	}
	if gotIdem != "idem-1" {
		t.Fatalf("idempotency header = %q, want idem-1", gotIdem)
	}
	if gotBody != `{"hello":"world"}` {
		t.Fatalf("body = %q", gotBody)
	}
	if gotType != "application/json" {
		t.Fatalf("content-type = %q", gotType)
	}
}

// TestExecuteHTTPCallNoSecret sends unauthenticated when no handle was authored (a token-in-URL
// webhook), and does not touch the store.
func TestExecuteHTTPCallNoSecret(t *testing.T) {
	var hadAuth atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "" {
			hadAuth.Store(true)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	store := &fakeSecretStore{}
	e := newTestExecutor(store)
	res := e.Execute(context.Background(), &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL},
	})
	if res.err != nil || res.outcome != outcomeSent {
		t.Fatalf("Execute = (%s,%v), want sent/nil", res.outcome, res.err)
	}
	if hadAuth.Load() {
		t.Fatal("no secret was authored; the call must be unauthenticated")
	}
	if store.resolveCalls() != 0 {
		t.Fatalf("no handle ⇒ no store resolve, got %d", store.resolveCalls())
	}
}

// TestExecuteHTTPCallSecretResolveFailsClosed never sends when a handle was authored but the secret
// cannot be resolved; the failure is retryable (so a transient store blip recovers).
func TestExecuteHTTPCallSecretResolveFailsClosed(t *testing.T) {
	var reached atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &fakeSecretStore{failRef: "webhook/auth", err: errors.New("db down")}
	e := newTestExecutor(store)
	res := e.Execute(context.Background(), &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
		HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL, SecretRef: "webhook/auth"},
	})
	if res.err == nil || !res.retryable || res.outcome != outcomeRetry {
		t.Fatalf("Execute = (%s,retryable=%v,%v), want retry/true/err", res.outcome, res.retryable, res.err)
	}
	if reached.Load() {
		t.Fatal("fail-closed violated: an unauthenticated call was sent after a resolve failure")
	}
}

// TestExecuteHTTPCallNon2xxRetryable classifies a non-2xx as a transient (bounded) retry.
func TestExecuteHTTPCallNon2xxRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	res := newTestExecutor(&fakeSecretStore{}).Execute(context.Background(),
		&connectorwire.ConnectorDispatchRequest{Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
			HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL}})
	if !res.retryable || res.outcome != outcomeRetry {
		t.Fatalf("non-2xx should be retryable, got %s/%v", res.outcome, res.retryable)
	}
}

// TestExecuteHTTPCallBadURLTerminal re-validates the URL at execution (defense-in-depth vs a config
// that bypassed the publish gate) and classifies a bad URL as terminal-invalid, never sending.
func TestExecuteHTTPCallBadURLTerminal(t *testing.T) {
	res := newTestExecutor(&fakeSecretStore{}).Execute(context.Background(),
		&connectorwire.ConnectorDispatchRequest{Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
			HTTPCall: &connectorwire.HTTPCallDispatch{URL: "ftp://evil/x"}})
	if res.retryable || res.err == nil || res.outcome != outcomeInvalid {
		t.Fatalf("bad URL should be terminal-invalid, got %s/retryable=%v/%v", res.outcome, res.retryable, res.err)
	}
}

// TestExecuteHTTPCallBadMethodTerminal rejects a non-POST method as terminal-invalid.
func TestExecuteHTTPCallBadMethodTerminal(t *testing.T) {
	res := newTestExecutor(&fakeSecretStore{}).Execute(context.Background(),
		&connectorwire.ConnectorDispatchRequest{Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
			HTTPCall: &connectorwire.HTTPCallDispatch{URL: "https://x/y", Method: "DELETE"}})
	if res.retryable || res.outcome != outcomeInvalid {
		t.Fatalf("non-POST should be terminal-invalid, got %s/retryable=%v", res.outcome, res.retryable)
	}
}

// TestExecuteHTTPCallClampsForgedTimeout proves a forged/huge TimeoutMs does not overflow the
// deadline into a negative (instantly-expired) duration: the send still completes against a fast
// server and is classified sent, not a spurious retry.
func TestExecuteHTTPCallClampsForgedTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	// A value that would overflow time.Duration(ms)*time.Millisecond into a negative deadline if used
	// verbatim; the executor clamps it to connectorwire.MaxTimeoutMs first.
	res := newTestExecutor(&fakeSecretStore{}).Execute(context.Background(),
		&connectorwire.ConnectorDispatchRequest{Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme",
			HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL, TimeoutMs: math.MaxInt64 / 1_000_000}})
	if res.err != nil || res.outcome != outcomeSent {
		t.Fatalf("a clamped forged timeout should still send, got %s/%v", res.outcome, res.err)
	}
}

// TestExecutePublishUnsupported classifies a publish dispatch as terminal-unsupported in this build
// (the Bento tier is slice C4).
func TestExecutePublishUnsupported(t *testing.T) {
	res := newTestExecutor(&fakeSecretStore{}).Execute(context.Background(),
		&connectorwire.ConnectorDispatchRequest{Kind: connectorwire.ConnectorKindPublish, Tenant: "acme",
			Publish: &connectorwire.PublishDispatch{ConnectorRef: "kafka-main"}})
	if res.retryable || res.err == nil || res.outcome != outcomeUnsupported {
		t.Fatalf("publish should be terminal-unsupported, got %s/retryable=%v/%v", res.outcome, res.retryable, res.err)
	}
}
