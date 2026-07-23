// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"strings"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	coapdtls "github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	udpclient "github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-lwm2m-ingest/config"
	"github.com/devicechain-io/dc-lwm2m-ingest/server"
)

// A 16-byte PSK (>= the cipher-suite minimum) shared by the integration clients.
var itPSK = []byte("0123456789abcdef")

// itBindings maps the two integration identities to distinct tenancy bindings. The
// tenants/externalIds are deliberately UNRELATED to any `ep` a client asserts, so a test
// that sends a hostile `ep` and then reads what was resolved proves tenancy came from the
// credential, not the payload (D1).
var itBindings = map[string]config.PskBinding{
	"dev-1": {Tenant: "acme", ExternalId: "plant-a/sensor-1", DeviceTypeToken: "sensor", AutoRegister: true},
	"dev-2": {Tenant: "beta", ExternalId: "plant-b/sensor-9", DeviceTypeToken: "sensor", AutoRegister: true},
}

// startIntegration stands up a real DTLS-PSK CoAP server with the /rd handlers mounted
// over a real Registry (fake resolver + emitter so the test can read what was resolved and
// emitted). It returns the address plus the fakes.
func startIntegration(t *testing.T) (string, *fakeResolver, *fakeEmitter) {
	t.Helper()
	res := &fakeResolver{token: "tok-1", outcome: adapter.ResolveCreated}
	em := &fakeEmitter{}
	reg := New(res, em, adapter.NewEpochSource(nil), Metrics{}, Options{})
	handlers := NewHandlers(reg, itBindings, Metrics{})

	srv, err := server.New(server.Config{
		Addr:        "127.0.0.1:0",
		Credentials: map[string][]byte{"dev-1": itPSK, "dev-2": itPSK},
		CIDLength:   8,
		MaxSessions: 1000,
	}, server.Metrics{}, handlers.Mount)
	require.NoError(t, err)
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Stop)
	return srv.Addr().String(), res, em
}

// dial opens a DTLS-PSK CoAP client presenting the given identity.
func dial(t *testing.T, addr, identity string) *udpclient.Conn {
	t.Helper()
	ccfg := &piondtls.Config{
		PSK:             func([]byte) ([]byte, error) { return itPSK, nil },
		PSKIdentityHint: []byte(identity),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	}
	conn, err := coapdtls.Dial(addr, ccfg)
	require.NoError(t, err, "DTLS-PSK dial for %q", identity)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func query(kvs ...string) []message.Option {
	opts := make([]message.Option, len(kvs))
	for i, kv := range kvs {
		opts[i] = message.Option{ID: message.URIQuery, Value: []byte(kv)}
	}
	return opts
}

// locationRegID extracts the regId from a 2.01 Created response's Location-Path (/rd/{id}).
func locationRegID(t *testing.T, resp interface{ Options() message.Options }) string {
	t.Helper()
	loc, err := resp.Options().LocationPath()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(loc, "/rd/"), "location %q must be under /rd/", loc)
	return strings.TrimPrefix(loc, "/rd/")
}

// TestRegisterRecoversIdentityAndBindsTenancyFromCredential is the load-bearing pin for
// D1 over a REAL DTLS-PSK handshake: the handler recovers the authenticated PSK identity
// from the connection (the fragile concrete-type chain), resolves it to ITS binding, and
// emits presence under that tenant — while a hostile `ep` in the request payload is
// ignored. If go-coap ever changes how it wraps the pion conn, identity recovery breaks
// and this reddens rather than tenancy silently misattributing.
func TestRegisterRecoversIdentityAndBindsTenancyFromCredential(t *testing.T) {
	addr, res, em := startIntegration(t)
	conn := dial(t, addr, "dev-1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A deliberately hostile ep: a different tenant's device id. It must be ignored.
	resp, err := conn.Post(ctx, "/rd", message.TextPlain, nil, query("ep=beta/plant-b/sensor-9", "lt=300")...)
	require.NoError(t, err)
	require.Equal(t, codes.Created, resp.Code(), "a valid Register must be 2.01 Created")
	regId := locationRegID(t, resp)
	require.NotEmpty(t, regId)

	// Tenancy came from the credential (dev-1 -> acme / plant-a/sensor-1), NOT the ep.
	call := res.lastCall()
	assert.Equal(t, "acme", call.tenant, "tenant must be the credential's, not the ep's")
	assert.Equal(t, "plant-a/sensor-1", call.externalId, "external id must be the credential's, not the ep's")

	conns := em.connects()
	require.Len(t, conns, 1)
	assert.Equal(t, "acme", conns[0].tenant)
	assert.Equal(t, "plant-a/sensor-1", conns[0].ev.ExternalId)
	assert.True(t, conns[0].ev.Connected)
}

// TestRegisterUpdateDeregisterRoundTrip exercises the full CoAP code mapping over a real
// handshake: 2.01 Created (+ Location-Path), 2.04 Changed on Update, 2.02 Deleted on
// Deregister, and a DISCONNECTED emitted on deregister.
func TestRegisterUpdateDeregisterRoundTrip(t *testing.T) {
	addr, _, em := startIntegration(t)
	conn := dial(t, addr, "dev-1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := conn.Post(ctx, "/rd", message.TextPlain, nil, query("lt=300")...)
	require.NoError(t, err)
	require.Equal(t, codes.Created, resp.Code())
	regId := locationRegID(t, resp)

	upd, err := conn.Post(ctx, "/rd/"+regId, message.TextPlain, nil, query("lt=300")...)
	require.NoError(t, err)
	assert.Equal(t, codes.Changed, upd.Code(), "Update must be 2.04 Changed")
	assert.Empty(t, em.disconnects(), "an Update emits no presence")

	del, err := conn.Delete(ctx, "/rd/"+regId)
	require.NoError(t, err)
	assert.Equal(t, codes.Deleted, del.Code(), "Deregister must be 2.02 Deleted")
	require.Len(t, em.disconnects(), 1, "Deregister emits a DISCONNECTED")

	// The location is gone: a second Update is 4.04 (the device would re-Register).
	again, err := conn.Post(ctx, "/rd/"+regId, message.TextPlain, nil, query("lt=300")...)
	require.NoError(t, err)
	assert.Equal(t, codes.NotFound, again.Code())
}

// TestForeignIdentityCannotDeregisterOverTheWire pins per-op authz end to end: a second
// authenticated device (dev-2) cannot deregister dev-1's registration — it gets a uniform
// 4.04 and dev-1's registration is untouched.
func TestForeignIdentityCannotDeregisterOverTheWire(t *testing.T) {
	addr, _, em := startIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	victim := dial(t, addr, "dev-1")
	resp, err := victim.Post(ctx, "/rd", message.TextPlain, nil, query("lt=300")...)
	require.NoError(t, err)
	require.Equal(t, codes.Created, resp.Code())
	regId := locationRegID(t, resp)

	// dev-2 tries to deregister dev-1's registration.
	attacker := dial(t, addr, "dev-2")
	del, err := attacker.Delete(ctx, "/rd/"+regId)
	require.NoError(t, err)
	assert.Equal(t, codes.NotFound, del.Code(), "a foreign identity must get a uniform 4.04")
	assert.Empty(t, em.disconnects(), "a rejected foreign deregister must emit no presence")

	// dev-1's registration is intact: its owner can still update it.
	upd, err := victim.Post(ctx, "/rd/"+regId, message.TextPlain, nil, query("lt=300")...)
	require.NoError(t, err)
	assert.Equal(t, codes.Changed, upd.Code(), "the victim's own registration must still be live")
}
