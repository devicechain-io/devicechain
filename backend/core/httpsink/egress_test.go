// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package httpsink

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 This is the test that pins the WIRING, and it is the one to keep if any go.
//
// egress has its own tests for which addresses are refused. What nothing else asserts is
// that DefaultClient — the client every caller passing nil gets — actually dials through
// the guard. Delete the Transport from DefaultClient and every other test in this
// package still passes, because they all inject their own client.
//
// It asserts on the server's state rather than the error text: "was the destination
// reached", not "did something return an error", because a Transport broken in some
// unrelated way would also produce an error.
func TestDefaultClientRefusesAPrivateDestinationBeforeConnecting(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer srv.Close()

	err := Send(context.Background(), nil, Request{URL: srv.URL, Body: []byte("{}")})

	require.Error(t, err, "a loopback destination must be refused")
	assert.ErrorIs(t, err, egress.ErrBlocked)
	assert.False(t, reached, "the request body reached the destination")
}

// The counterweight: the SAME nil-client path still delivers to a permitted destination.
// Without it, a DefaultClient that refused everything — a misconfigured transport, a guard
// with an empty allow AND deny set inverted — would pass the test above.
//
// It cannot use a real remote host (a unit test must not need the network), so it proves
// the complementary half locally: the guard's own allow list admits the same server the
// test above refuses. That is the same code path, one decision flipped.
func TestAGuardedClientWithAnAllowanceStillDelivers(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: loopbackAllowedTransport()}
	require.NoError(t, Send(context.Background(), client, Request{URL: srv.URL, Body: []byte("{}")}))
	assert.True(t, reached)
}

// Auth.Validate: the header that carried a tenant-supplied secret past the reserved-header
// filter, because it was written after it.
func TestAuthHeaderRejectsInternalServiceIdentity(t *testing.T) {
	for _, name := range []string{"X-DC-Service-Secret", "x-dc-tenant", "X-Dc-Idempotency-Key", "X-DC-Anything"} {
		t.Run(name, func(t *testing.T) {
			err := Auth{Header: name}.Validate()
			require.Error(t, err, "an X-DC-* auth header carries internal service identity")
			assert.Contains(t, err.Error(), "reserved")
		})
	}
}

// 🔴 The counterweight that stops the obvious "fix" of reusing IsReservedHeader here.
// IsReservedHeader rejects Authorization, which is this field's legitimate default and the
// header the zero Auth writes. A validator built on it would refuse the common case.
func TestAuthHeaderStillAllowsTheHeadersThatAreThePoint(t *testing.T) {
	require.True(t, IsReservedHeader("Authorization"),
		"precondition: IsReservedHeader rejects Authorization, so it must not be reused for this")

	assert.NoError(t, Auth{}.Validate(), "the zero value means Authorization: Bearer and must pass")
	assert.NoError(t, Auth{Header: "Authorization", Scheme: "Bearer"}.Validate())
	assert.NoError(t, Auth{Header: "X-API-Key"}.Validate())
	assert.NoError(t, Auth{Header: "X-Custom-Token", Scheme: "Token"}.Validate())
}

func TestAuthHeaderRejectsAMalformedName(t *testing.T) {
	assert.Error(t, Auth{Header: "Bad Header"}.Validate())
	assert.Error(t, Auth{Header: "X-Evil\r\nInjected"}.Validate())
}

// Send must refuse rather than drop. Dropping would deliver the payload with no credential
// at all: the endpoint rejects it, the operator sees delivery failures, and nothing says
// why. The assertion is that the server was never reached AND that an error came back.
func TestSendRefusesAReservedAuthHeaderRatherThanDroppingIt(t *testing.T) {
	var gotIdentity string
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reached = true
		gotIdentity = req.Header.Get("X-DC-Service-Secret")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := Send(context.Background(), srv.Client(), Request{
		URL:    srv.URL,
		Secret: "the-real-service-secret",
		Auth:   Auth{Header: "X-DC-Service-Secret"},
	})

	require.Error(t, err)
	assert.False(t, reached, "the request was sent; the secret left the process")
	assert.Empty(t, gotIdentity)
}

// And the same request with a legitimate custom auth header still delivers, so the check
// above is not passing because Send refuses everything with a Secret set.
func TestSendStillDeliversWithACustomAuthHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Get("X-API-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	require.NoError(t, Send(context.Background(), srv.Client(), Request{
		URL:    srv.URL,
		Secret: "tok-abc",
		Auth:   Auth{Header: "X-API-Key"},
	}))
	assert.Equal(t, "tok-abc", got)
}

// loopbackAllowedTransport is the guard with exactly one allowance, which is how a test
// reaches an httptest server without stepping outside the dial path production uses.
func loopbackAllowedTransport() http.RoundTripper {
	return egress.NewGuard([]netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	}).Transport()
}
