// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/egress"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

const testKey = "cs-api-key-DO-NOT-LEAK"

func target(t *testing.T, base string) connectorspec.LoRaWANDownlinkTarget {
	t.Helper()
	u, err := url.Parse(base)
	require.NoError(t, err)
	return connectorspec.LoRaWANDownlinkTarget{
		NetworkServer: connectorspec.NetworkServerChirpStackV4, BaseURL: u, Integration: "cs1", APIKey: testKey,
	}
}

func sendErr(t *testing.T, err error) *SendError {
	t.Helper()
	var se *SendError
	require.ErrorAs(t, err, &se)
	return se
}

func ctx5(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestChirpStackEnqueueRequestIsExact(t *testing.T) {
	var gotPath, gotAuth, gotCT, gotMethod string
	var gotBody map[string]map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotCT, gotMethod = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Method
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &gotBody))
		_, _ = w.Write([]byte(`{"id":"0b1c2d3e-0000-4000-8000-000000000001"}`))
	}))
	defer srv.Close()

	// A path prefix on the base URL (a reverse-proxy mount) is kept.
	tg := target(t, srv.URL+"/chirp")
	id, err := NewChirpStackWithTransport(http.DefaultTransport).Enqueue(ctx5(t), tg, "0102030405060708",
		Params{FPort: 10, Data: []byte{1, 2, 3}, Confirmed: false}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, "0b1c2d3e-0000-4000-8000-000000000001", id)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/chirp/api/devices/0102030405060708/queue", gotPath)
	require.Equal(t, "Bearer "+testKey, gotAuth)
	require.Equal(t, "application/json", gotCT)
	require.Equal(t, map[string]map[string]any{"queueItem": {
		"devEui": "0102030405060708", "fPort": float64(10), "confirmed": false, "data": "AQID",
	}}, gotBody)
}

func TestChirpStackOutcomeClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		reach  string
		ok     bool
		has    string
	}{
		{"200 with id", 200, `{"id":"abc"}`, "", true, ""},
		{"200 without id", 200, `{}`, cmddownlink.ReachUnanswered, false, "without a queue item id"},
		{"200 garbage", 200, `<html>`, cmddownlink.ReachUnanswered, false, "without a queue item id"},
		{"401", 401, `{"code":16,"message":"authentication failed"}`, cmddownlink.ReachUnsent, false, "(HTTP 401, UNAUTHENTICATED)"},
		{"404", 404, `{"message":"object does not exist"}`, cmddownlink.ReachUnsent, false, "HTTP 404"},
		{"429", 429, ``, cmddownlink.ReachUnsent, false, "HTTP 429"},
		{"400 payload too large", 400, `{"code":3,"message":"payload exceeds max payload size"}`, cmddownlink.ReachUnsent, false, "INVALID_ARGUMENT"},
		{"500", 500, `{"code":13,"message":"boom"}`, cmddownlink.ReachUnanswered, false, "HTTP 500, INTERNAL"},
		{"499 cancelled", 499, `{"code":1,"message":"x"}`, cmddownlink.ReachUnanswered, false, "CANCELLED"},
		{"a 4xx carrying an ambiguous gRPC code", 400, `{"code":14,"message":"x"}`, cmddownlink.ReachUnanswered, false, "UNAVAILABLE"},
		{"408 is not proof of non-delivery", 408, ``, cmddownlink.ReachUnanswered, false, "may have been queued"},
		{"502 html", 502, `<html>bad gateway</html>`, cmddownlink.ReachUnanswered, false, "HTTP 502"},
		{"redirect is not followed and proves nothing", 302, ``, cmddownlink.ReachUnanswered, false, "HTTP 302"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if c.status == 302 {
					w.Header().Set("Location", "http://127.0.0.1:1/elsewhere")
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			id, err := NewChirpStackWithTransport(http.DefaultTransport).Enqueue(ctx5(t), target(t, srv.URL),
				"0102030405060708", Params{FPort: 1, Data: []byte{1}}, time.Time{})
			require.Equal(t, int32(1), hits.Load(), "exactly one request, never a retry or a redirect chase")
			if c.ok {
				require.NoError(t, err)
				require.Equal(t, "abc", id)
				return
			}
			se := sendErr(t, err)
			require.Equal(t, c.reach, se.Reach)
			require.Contains(t, se.Text, c.has)
			require.NotContains(t, se.Text, testKey)
		})
	}
}

func TestChirpStackErrorTextNeverCarriesTheCredentialOrControlBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		msg := "bad token " + testKey + "\r\nX-Injected: 1 " + strings.Repeat("x", 600)
		b, _ := json.Marshal(map[string]string{"message": msg})
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	_, err := NewChirpStackWithTransport(http.DefaultTransport).Enqueue(ctx5(t), target(t, srv.URL),
		"0102030405060708", Params{FPort: 1, Data: []byte{1}}, time.Time{})
	se := sendErr(t, err)
	require.NotContains(t, se.Text, testKey)
	require.NotContains(t, se.Text, "\r")
	require.NotContains(t, se.Text, "\n")
	require.Less(t, len(se.Text), 400, "the network server's text is clipped")
}

func TestChirpStackHungServerIsUnansweredAfterTheRequestWasWritten(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := NewChirpStackWithTransport(http.DefaultTransport).Enqueue(ctx, target(t, srv.URL),
		"0102030405060708", Params{FPort: 1, Data: []byte{1}}, time.Time{})
	se := sendErr(t, err)
	require.Equal(t, cmddownlink.ReachUnanswered, se.Reach, "the request reached the server, so it may have been queued")
}

func TestChirpStackConnectFailureIsUnsent(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // nothing listens any more
	_, err := NewChirpStackWithTransport(http.DefaultTransport).Enqueue(ctx5(t), target(t, base),
		"0102030405060708", Params{FPort: 1, Data: []byte{1}}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach, "nothing was written, so nothing was queued")
}

func TestChirpStackOversizedResponseIsNotRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"abc","pad":"` + strings.Repeat("x", maxResponseBytes+10) + `"}`))
	}))
	defer srv.Close()
	_, err := NewChirpStackWithTransport(http.DefaultTransport).Enqueue(ctx5(t), target(t, srv.URL),
		"0102030405060708", Params{FPort: 1, Data: []byte{1}}, time.Time{})
	se := sendErr(t, err)
	require.Equal(t, cmddownlink.ReachUnanswered, se.Reach)
}

// The real guard, not a stand-in: a loopback destination is refused before a byte is written.
func TestChirpStackEgressGuardRefusesLoopback(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	}))
	defer srv.Close()
	_, err := NewChirpStack(egress.NewGuard(nil)).Enqueue(ctx5(t), target(t, srv.URL),
		"0102030405060708", Params{FPort: 1, Data: []byte{1}}, time.Time{})
	se := sendErr(t, err)
	require.True(t, se.Blocked)
	require.Equal(t, cmddownlink.ReachUnsent, se.Reach)
	require.Zero(t, hits.Load(), "the guard refuses before the request is made")

	// Control: the same server is reachable when the operator allows loopback, so the refusal above
	// is the guard and not a broken fixture.
	allow := egress.NewGuard([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
	id, err := NewChirpStack(allow).Enqueue(ctx5(t), target(t, srv.URL), "0102030405060708",
		Params{FPort: 1, Data: []byte{1}}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, "abc", id)
}

func TestChirpStackRefusesWhatItCannotSend(t *testing.T) {
	c := NewChirpStackWithTransport(http.DefaultTransport)
	tg := target(t, "http://127.0.0.1:1")
	_, err := c.Enqueue(ctx5(t), tg, "../etc/passwd", Params{FPort: 1, Data: []byte{1}}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach)
	tg.NetworkServer = "ttsv3"
	_, err = c.Enqueue(ctx5(t), tg, "0102030405060708", Params{FPort: 1, Data: []byte{1}}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach)
}

func TestChirpStackSendsTheExpiryWhenThereIsOne(t *testing.T) {
	var bodies []map[string]map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &b)
		bodies = append(bodies, b)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()
	c := NewChirpStackWithTransport(http.DefaultTransport)
	exp := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	_, err := c.Enqueue(ctx5(t), target(t, srv.URL), "0102030405060708", Params{FPort: 1, Data: []byte{1}}, exp)
	require.NoError(t, err)
	_, err = c.Enqueue(ctx5(t), target(t, srv.URL), "0102030405060708", Params{FPort: 1, Data: []byte{1}}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, "2026-11-07T12:00:00Z", bodies[0]["queueItem"]["expiresAt"])
	require.NotContains(t, bodies[1]["queueItem"], "expiresAt")
}
