// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
	"github.com/stretchr/testify/require"
)

func webhookTarget(t *testing.T, base string, correlation bool) connectorspec.LoRaWANDownlinkTarget {
	t.Helper()
	body := `{"devEui":"{{devEui}}","fPort":{{fPort}},"data":"{{payloadBase64}}","hex":"{{payloadHex}}","confirmed":{{confirmed}}}`
	headers := map[string]string{"X-Test": "literal"}
	if correlation {
		headers["X-Ref"] = "{{correlationId}}"
	}
	cfg, _ := json.Marshal(map[string]any{"networkServer": "webhook", "baseUrl": base, "integration": "cs1", "urlTemplate": "/devices/{{devEui}}/downlinks?data={{payloadBase64}}", "method": "PATCH", "bodyTemplate": body, "headers": headers, "authHeader": "X-Key", "authPrefix": ""})
	tg, err := connectorspec.Build(connectorspec.LoRaWANDownlinkType, cfg, testKey)
	require.NoError(t, err)
	return tg.(connectorspec.LoRaWANDownlinkTarget)
}
func TestWebhookExactRequestAndGuard(t *testing.T) {
	var method, path, query, body string
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, query = r.Method, r.URL.Path, r.URL.RawQuery
		headers = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = w.Write([]byte("not a queue id"))
	}))
	defer srv.Close()
	tg := webhookTarget(t, srv.URL+"/api", true)
	client := NewWebhook(egress.NewGuard([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}))
	client.rand = func(b []byte) (int, error) {
		for i := range b {
			b[i] = byte(i)
		}
		return len(b), nil
	}
	id, err := client.Enqueue(ctx5(t), tg, "aabbccddeeff0011", Params{FPort: 10, Data: []byte{251, 255}, Confirmed: true}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, "dc:000102030405060708090a0b0c0d0e0f", id)
	require.Equal(t, "PATCH", method)
	require.Equal(t, "/api/devices/AABBCCDDEEFF0011/downlinks", path)
	require.Equal(t, "data=%2B%2F8%3D", query)
	require.Equal(t, `{"devEui":"AABBCCDDEEFF0011","fPort":10,"data":"+/8=","hex":"fbff","confirmed":true}`, body)
	require.Equal(t, testKey, headers.Get("X-Key"))
	require.Equal(t, id, headers.Get("X-Ref"))
	require.Equal(t, "literal", headers.Get("X-Test"))
	require.Equal(t, "application/json", headers.Get("Content-Type"))
	require.Equal(t, "*/*", headers.Get("Accept"))
	_, err = NewWebhook(nil).Enqueue(ctx5(t), tg, "0011223344556677", Params{}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach)
	require.True(t, sendErr(t, err).Blocked)
}
func TestWebhookStatusesRedirectsAndSecrets(t *testing.T) {
	var redirectHits atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectHits.Add(1) }))
	defer dest.Close()
	for _, status := range []int{200, 204, 400, 401, 404, 408, 499, 500, 503, 301, 302, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", dest.URL)
				w.WriteHeader(status)
				_, _ = w.Write([]byte("server-private-text " + testKey))
			}))
			defer srv.Close()
			id, err := NewWebhookWithTransport(http.DefaultTransport).Enqueue(ctx5(t), webhookTarget(t, srv.URL, true), "0011223344556677", Params{}, time.Time{})
			if status < 300 {
				require.NoError(t, err)
				require.Regexp(t, `^dc:[0-9a-f]{32}$`, id)
				return
			}
			reach := cmddownlink.ReachUnsent
			if status < 400 || status >= 500 || status == 408 || status == 499 {
				reach = cmddownlink.ReachUnanswered
			}
			require.Equal(t, reach, sendErr(t, err).Reach)
			require.NotContains(t, err.Error(), testKey)
			require.NotContains(t, err.Error(), "server-private-text")
		})
	}
	require.Zero(t, redirectHits.Load())
}
func TestWebhookRefusalsAndRouter(t *testing.T) {
	c := NewWebhookWithTransport(http.DefaultTransport)
	tg := webhookTarget(t, "https://ns.example.com", true)
	c.rand = func([]byte) (int, error) { return 0, errors.New("secret random error") }
	_, err := c.Enqueue(context.Background(), tg, "0011223344556677", Params{}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach)
	require.NotContains(t, err.Error(), "secret random error")
	tg.Webhook = nil
	_, err = c.Enqueue(ctx5(t), tg, "0011223344556677", Params{}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach)
	tg = webhookTarget(t, "https://ns.example.com", true)
	tg.NetworkServer = connectorspec.NetworkServerTTSV3
	_, err = c.Enqueue(ctx5(t), tg, "0011223344556677", Params{}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach)
	tg.NetworkServer = connectorspec.NetworkServerWebhook
	cs, tts, web := &fakeEnqueuer{}, &fakeEnqueuer{}, &fakeEnqueuer{id: ttsKey}
	id, err := (Router{ChirpStack: cs, TTS: tts, Webhook: web}).Enqueue(ctx5(t), tg, "0011223344556677", Params{}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, ttsKey, id)
	require.Len(t, web.calls, 1)
	require.Empty(t, cs.calls)
	require.Empty(t, tts.calls)
	_, err = (Router{TTS: tts}).Enqueue(ctx5(t), tg, "0011223344556677", Params{}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach)
}
func TestWebhookAdapterConfirmationAndAck(t *testing.T) {
	r, _ := newConfirmedRig(t)
	r.links.target = webhookTarget(t, "https://ns.example.com", false)
	r.handle(t, subject, envelope(t, confirmedParams), routed())
	require.Zero(t, r.claim.calls)
	require.Empty(t, r.send.calls)
	require.Equal(t, cmddownlink.ReachUnsent, r.resp.envs[0].Reach)
	require.Contains(t, *r.resp.envs[0].Error, "{{correlationId}}")
	require.Equal(t, 1, r.m.get(ResultRefusedConfirm))
	require.JSONEq(t, `{"reason":"UNSUPPORTED"}`, *r.resp.envs[0].Payload)
	var sentID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sentID = r.Header.Get("X-Ref")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	r, pend := newConfirmedRig(t)
	r.links.target = webhookTarget(t, srv.URL, true)
	r.a.sender = NewWebhook(egress.NewGuard([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}))
	r.handle(t, subject, envelope(t, confirmedParams), routed())
	require.Empty(t, r.resp.envs)
	require.Equal(t, 1, pend.size())
	for _, p := range pend.rows {
		require.Equal(t, "webhook", p.DeviceRef)
		require.Equal(t, "0102030405060708", p.DevEUI)
		require.Equal(t, sentID, p.QueueItemID)
		require.Regexp(t, `^dc:[0-9a-f]{32}$`, sentID)
	}
	feed := newAckRig(t)
	feed.now = r.now
	feed.pend = pend
	feed.f.pending = pend
	rep := cmddownlink.DownlinkReport{Integration: "cs1", Kind: "ack", QueueItemID: sentID, DeviceRef: "webhook", DeviceToken: "wrong-device", Acknowledged: true}
	feed.deliver(t, rep, 1)
	require.Empty(t, feed.resp.envs)
	require.Equal(t, 1, feed.m.get(AckMismatch))
	require.Equal(t, 1, pend.size())
	rep.DeviceToken = "dev-1"
	feed.deliver(t, rep, 1)
	require.Len(t, feed.resp.envs, 1)
	require.True(t, feed.resp.envs[0].Success)
	require.Zero(t, pend.size())
	r, pend = newConfirmedRig(t)
	r.links.target = webhookTarget(t, "https://ns.example.com", false)
	r.send.id = ttsKey
	r.handle(t, subject, envelope(t, goodParams), routed())
	require.True(t, r.resp.envs[0].Success)
	require.Contains(t, *r.resp.envs[0].Payload, `"stage":"queued"`)
	require.Zero(t, pend.size())
}
func TestWebhookFailedIsAlwaysPotentiallySent(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, func(p *Pending) { p.DeviceRef = "webhook"; p.QueueItemID = ttsKey })
	rep := report("failed", false)
	rep.DeviceRef = "webhook"
	rep.DeviceToken = "dev-1"
	rep.QueueItemID = ttsKey
	rep.ErrorNamespace = "webhook"
	rep.ErrorName = "failed"
	require.False(t, neverTransmitted(&Pending{}, rep))
	r.deliver(t, rep, 1)
	require.Len(t, r.resp.envs, 1)
	require.Equal(t, cmddownlink.ReachUnanswered, r.resp.envs[0].Reach)
	require.Equal(t, 1, r.m.get(AckFailedMaybeSent))
}

func TestWebhookTransportAmbiguity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer srv.Close()
	_, err := NewWebhookWithTransport(http.DefaultTransport).Enqueue(ctx5(t), webhookTarget(t, srv.URL, true), "0011223344556677", Params{}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnanswered, sendErr(t, err).Reach)
	c := NewWebhookWithTransport(&http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.DNSError{Err: "private diagnostic " + testKey, Name: "ns.example.com"}
	}})
	_, err = c.Enqueue(ctx5(t), webhookTarget(t, "https://ns.example.com", true), "0011223344556677", Params{}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach)
	require.NotContains(t, err.Error(), testKey)
	require.NotContains(t, err.Error(), "private diagnostic")
}
func TestWebhookDefaultsAndRenderedAuthorityDefense(t *testing.T) {
	var auth, contentType, method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		method = r.Method
		contentType = r.Header.Get("Content-Type")
	}))
	defer srv.Close()
	cfg, _ := json.Marshal(map[string]any{"networkServer": "webhook", "baseUrl": srv.URL, "integration": "cs1", "urlTemplate": "/{{devEui}}"})
	target, err := connectorspec.Build(connectorspec.LoRaWANDownlinkType, cfg, testKey)
	require.NoError(t, err)
	tg := target.(connectorspec.LoRaWANDownlinkTarget)
	_, err = NewWebhookWithTransport(http.DefaultTransport).Enqueue(ctx5(t), tg, "0011223344556677", Params{}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, "Bearer "+testKey, auth)
	require.Equal(t, http.MethodPost, method)
	require.Empty(t, contentType)
	// A target bypassing the config parser still cannot add userinfo to the rendered request.
	bad := *tg.BaseURL
	bad.User = url.UserPassword("user", "secret")
	tg.BaseURL = &bad
	_, err = NewWebhookWithTransport(http.DefaultTransport).Enqueue(ctx5(t), tg, "0011223344556677", Params{}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach)
}
