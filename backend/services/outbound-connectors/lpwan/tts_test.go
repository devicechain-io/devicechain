// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/integrationcodec"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

func ttsTarget(t *testing.T, base string) connectorspec.LoRaWANDownlinkTarget {
	t.Helper()
	u, err := url.Parse(base)
	require.NoError(t, err)
	return connectorspec.LoRaWANDownlinkTarget{NetworkServer: connectorspec.NetworkServerTTSV3, BaseURL: u,
		Integration: "tts1", ApplicationID: "my-app", APIKey: testKey}
}

func TestTTSEnqueueRequestIsExactAndNamesTheDownlinkItself(t *testing.T) {
	var gotPath, gotAuth, gotMethod string
	var gotBody struct {
		Downlinks []map[string]any `json:"downlinks"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotMethod = r.URL.Path, r.Header.Get("Authorization"), r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody.Downlinks = nil
		require.NoError(t, json.Unmarshal(b, &gotBody))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	key, err := NewTTSWithTransport(http.DefaultTransport).Enqueue(ctx5(t), ttsTarget(t, srv.URL+"/tts"), "dev-1",
		Params{FPort: 10, Data: []byte{0x0a, 0x0b}, Confirmed: true}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/tts/api/v3/as/applications/my-app/devices/dev-1/down/push", gotPath)
	require.Equal(t, "Bearer "+testKey, gotAuth)
	require.Len(t, gotBody.Downlinks, 1)
	d := gotBody.Downlinks[0]
	require.EqualValues(t, 10, d["f_port"])
	require.Equal(t, "Cgs=", d["frm_payload"])
	require.Equal(t, true, d["confirmed"])
	require.Equal(t, "NORMAL", d["priority"])
	// The push returns no id, so the platform names the downlink: its prefix and 128 random bits, sent
	// as the downlink's own correlation id and returned as the key reports are matched by.
	require.True(t, strings.HasPrefix(key, integrationcodec.DownlinkCorrelationPrefix))
	require.Len(t, key, len("dc:")+32)
	require.Equal(t, []any{key}, d["correlation_ids"])
	// A confirmed downlink bounds the Application Server's re-queues after a nack (it would otherwise
	// send it up to 8 more times, each a fresh downlink the device may act on).
	require.Equal(t, map[string]any{"max_attempts": float64(1)}, d["confirmed_retry"])

	// An unconfirmed downlink carries no retry setting.
	_, err = NewTTSWithTransport(http.DefaultTransport).Enqueue(ctx5(t), ttsTarget(t, srv.URL), "dev-1",
		Params{FPort: 1, Data: []byte{1}}, time.Time{})
	require.NoError(t, err)
	_, has := gotBody.Downlinks[0]["confirmed_retry"]
	require.False(t, has)

	key2, err := NewTTSWithTransport(http.DefaultTransport).Enqueue(ctx5(t), ttsTarget(t, srv.URL), "dev-1",
		Params{FPort: 1, Data: []byte{1}}, time.Time{})
	require.NoError(t, err)
	require.NotEqual(t, key, key2, "every downlink has its own id")
}

func TestTTSEnqueueClassifiesByHowFarItGot(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
		reach  string
		ok     bool
	}{
		"200 accepted":                    {200, `{}`, "", true},
		"200 unreadable body is still ok": {200, `nope`, "", true},
		"404 unknown device":              {404, `{"code":5,"message":"device not found"}`, cmddownlink.ReachUnsent, false},
		"401 bad key":                     {401, `{"code":16}`, cmddownlink.ReachUnsent, false},
		"400 invalid":                     {400, `{"code":3}`, cmddownlink.ReachUnsent, false},
		"500":                             {500, ``, cmddownlink.ReachUnanswered, false},
		"408":                             {408, ``, cmddownlink.ReachUnanswered, false},
		"409 aborted is ambiguous":        {409, `{"code":10}`, cmddownlink.ReachUnanswered, false},
		"400 unreadable body ambiguous":   {400, `<html>`, cmddownlink.ReachUnanswered, false},
		"302 redirect not chased":         {302, ``, cmddownlink.ReachUnanswered, false},
	} {
		t.Run(name, func(t *testing.T) {
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
			_, err := NewTTSWithTransport(http.DefaultTransport).Enqueue(ctx5(t), ttsTarget(t, srv.URL), "dev-1",
				Params{FPort: 1, Data: []byte{1}}, time.Time{})
			require.Equal(t, int32(1), hits.Load())
			if c.ok {
				require.NoError(t, err)
				return
			}
			se := sendErr(t, err)
			require.Equal(t, c.reach, se.Reach)
			require.NotContains(t, se.Text, testKey)
			require.NotContains(t, se.Text, "device not found", "the network server's own words are never shown")
		})
	}
}

func TestTTSEnqueueRefusesWhatCannotBeAPathOrBelongsToAnotherServer(t *testing.T) {
	c := NewTTSWithTransport(http.DefaultTransport)
	tg := ttsTarget(t, "https://tts.example.com")
	// The id pattern counts dash-separated groups, so a 37-character dashed id still matches it; the
	// length is what refuses it (and a 71-character one).
	for _, id := range []string{"../etc", "Dev1", "a/b", "", "-x", strings.Repeat("a", 37), strings.Repeat("a-", 18) + "a",
		strings.Repeat("a-", 35) + "a"} {
		_, err := c.Enqueue(ctx5(t), tg, id, Params{FPort: 1, Data: []byte{1}}, time.Time{})
		require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach, id)
	}
	bad := tg
	bad.ApplicationID = "../x"
	_, err := c.Enqueue(ctx5(t), bad, "dev-1", Params{FPort: 1, Data: []byte{1}}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach)
	cs := tg
	cs.NetworkServer = connectorspec.NetworkServerChirpStackV4
	_, err = c.Enqueue(ctx5(t), cs, "dev-1", Params{FPort: 1, Data: []byte{1}}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach, "never sent through another network server's client")
}

func TestTTSDialsThroughTheEgressGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()
	_, err := NewTTS(egress.NewGuard(nil)).Enqueue(ctx5(t), ttsTarget(t, srv.URL), "dev-1", Params{FPort: 1, Data: []byte{1}}, time.Time{})
	se := sendErr(t, err)
	require.True(t, se.Blocked, "a loopback network server needs an operator allowance")
	require.Equal(t, cmddownlink.ReachUnsent, se.Reach)
}

func TestTheRouterNeverSendsThroughAnotherNetworkServersClient(t *testing.T) {
	cs, tts := &fakeEnqueuer{id: "cs-id"}, &fakeEnqueuer{id: "tts-id"}
	r := Router{ChirpStack: cs, TTS: tts}
	id, err := r.Enqueue(ctx5(t), ttsTarget(t, "https://x.io"), "dev-1", Params{}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, "tts-id", id)
	require.Len(t, tts.calls, 1)
	require.Empty(t, cs.calls)
	id, err = r.Enqueue(ctx5(t), target(t, "https://x.io"), "0102030405060708", Params{}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, "cs-id", id)

	_, err = Router{ChirpStack: cs}.Enqueue(ctx5(t), ttsTarget(t, "https://x.io"), "dev-1", Params{}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach)
	unknown := ttsTarget(t, "https://x.io")
	unknown.NetworkServer = "other"
	_, err = r.Enqueue(ctx5(t), unknown, "d", Params{}, time.Time{})
	require.Equal(t, cmddownlink.ReachUnsent, sendErr(t, err).Reach)
}

func TestServiceDevicesReadsTheTTSDeviceID(t *testing.T) {
	q := &fakeQuerier{body: `{"devicesByToken":[{"token":"dev-1","aliases":[
		{"scheme":"lorawan.deveui","value":"0a0b0c0d0e0f1011"},{"scheme":"tts.device-id","value":"Sensor-1"}]}]}`}
	d := NewServiceDevices(q, "http://dm/graphql")
	id, err := d.TTSDeviceID(context.Background(), "acme", "dev-1")
	require.NoError(t, err)
	require.Equal(t, "sensor-1", id, "the canonical stored form")
	// Each identity is cached on its own: reading one does not answer for the other.
	eui, err := d.DevEUI(context.Background(), "acme", "dev-1")
	require.NoError(t, err)
	require.Equal(t, "0A0B0C0D0E0F1011", eui)

	for name, body := range map[string]string{
		"no alias":  `{"devicesByToken":[{"token":"dev-1","aliases":[{"scheme":"lorawan.deveui","value":"0102030405060708"}]}]}`,
		"malformed": `{"devicesByToken":[{"token":"dev-1","aliases":[{"scheme":"tts.device-id","value":"-bad-"}]}]}`,
	} {
		_, err := NewServiceDevices(&fakeQuerier{body: body}, "u").TTSDeviceID(context.Background(), "acme", "dev-1")
		require.ErrorIs(t, err, ErrNoDeviceID, name)
	}
}
