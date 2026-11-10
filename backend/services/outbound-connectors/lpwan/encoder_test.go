// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/payloadencode"
	"github.com/stretchr/testify/require"
)

const thermostatEncoder = `{"fPort":12,"fields":[{"param":"setpoint","type":"i16be","scale":0.1},{"bits":{"type":"u8","fields":[{"param":"mode","shift":0,"width":3}]}}]}`

func TestEncodedCommandQueuesExactBytesOnBothServers(t *testing.T) {
	for _, tts := range []bool{false, true} {
		t.Run(map[bool]string{false: "ChirpStack", true: "TTS"}[tts], func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls++
				var body map[string]json.RawMessage
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				if tts {
					var ds []map[string]any
					require.NoError(t, json.Unmarshal(body["downlinks"], &ds))
					require.Len(t, ds, 1)
					require.Equal(t, float64(12), ds[0]["f_port"])
					require.Equal(t, "ANcD", ds[0]["frm_payload"])
				} else {
					var q map[string]any
					require.NoError(t, json.Unmarshal(body["queueItem"], &q))
					require.Equal(t, float64(12), q["fPort"])
					require.Equal(t, "ANcD", q["data"])
				}
				_, _ = w.Write([]byte(`{"id":"queued"}`))
			}))
			defer srv.Close()
			r := newRig(t, nil)
			var err error
			r.devices.encoder, err = payloadencode.Parse([]byte(thermostatEncoder))
			require.NoError(t, err)
			if tts {
				r.links.target = ttsTarget(t, srv.URL)
				r.devices.ttsID = "dev-1"
				r.a.sender = NewTTSWithTransport(http.DefaultTransport)
			} else {
				r.links.target = target(t, srv.URL)
				r.a.sender = NewChirpStackWithTransport(http.DefaultTransport)
			}
			r.handle(t, subject, envelope(t, `{"setpoint":21.5,"mode":3}`), routed())
			require.Equal(t, 1, calls)
			require.Len(t, r.resp.envs, 1)
			require.True(t, r.resp.envs[0].Success)
			// The same real network fake must see no call on either refusal.
			r.handle(t, subject, envelope(t, `{"setpoint":4000,"mode":3}`), routed())
			require.Equal(t, 1, calls)
			require.Equal(t, cmddownlink.ReachUnsent, r.resp.envs[1].Reach)
			require.Contains(t, *r.resp.envs[1].Error, "setpoint")
			r.devices.encoderErr = errors.New("read unavailable")
			r.handle(t, subject, envelope(t, goodParams), routed())
			require.Equal(t, 1, calls)
			require.Equal(t, cmddownlink.ReachUnsent, r.resp.envs[2].Reach)
			require.Contains(t, *r.resp.envs[2].Error, "definition could not be read")
		})
	}
}

func TestEncodedConfirmedKeepsAcknowledgementGate(t *testing.T) {
	r := newRig(t, nil)
	e, err := payloadencode.Parse([]byte(`{"fPort":1,"confirmed":true,"fields":[{"const":"01"}]}`))
	require.NoError(t, err)
	r.devices.encoder = e
	r.handle(t, subject, envelope(t, `{}`), routed())
	require.Empty(t, r.send.calls)
	require.Equal(t, cmddownlink.ReachUnsent, r.resp.envs[0].Reach)
	require.Contains(t, *r.resp.envs[0].Error, "acknowledgement feed")
}

func TestNoEncoderKeepsRawContractOnBothServers(t *testing.T) {
	for _, tts := range []bool{false, true} {
		t.Run(map[bool]string{false: "ChirpStack", true: "TTS"}[tts], func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls++
				var body map[string]json.RawMessage
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				if tts {
					var ds []map[string]any
					require.NoError(t, json.Unmarshal(body["downlinks"], &ds))
					require.Len(t, ds, 1)
					require.Equal(t, float64(10), ds[0]["f_port"])
					require.Equal(t, "Cgs=", ds[0]["frm_payload"])
				} else {
					var q map[string]any
					require.NoError(t, json.Unmarshal(body["queueItem"], &q))
					require.Equal(t, float64(10), q["fPort"])
					require.Equal(t, "Cgs=", q["data"])
				}
				_, _ = w.Write([]byte(`{"id":"raw"}`))
			}))
			defer srv.Close()
			r := newRig(t, nil)
			if tts {
				r.links.target = ttsTarget(t, srv.URL)
				r.devices.ttsID = "dev-1"
				r.a.sender = NewTTSWithTransport(http.DefaultTransport)
			} else {
				r.links.target = target(t, srv.URL)
				r.a.sender = NewChirpStackWithTransport(http.DefaultTransport)
			}
			r.handle(t, subject, envelope(t, goodParams), routed())
			require.Equal(t, 1, calls)
			require.True(t, r.resp.envs[0].Success)
		})
	}
}

func TestEncoderCacheIncludesAbsenceAndExpires(t *testing.T) {
	for _, hasEncoder := range []bool{true, false} {
		encoded := "null"
		if hasEncoder {
			b, _ := json.Marshal(thermostatEncoder)
			encoded = string(b)
		}
		q := &fakeQuerier{body: `{"deviceCommandVocabulary":{"constrained":true,"commands":[{"commandKey":"setLed","encoder":` + encoded + `}]}}`}
		d := NewServiceDevices(q, "u")
		now := time.Now()
		d.now = func() time.Time { return now }
		first, err := d.Encoder(context.Background(), "acme", "dev-1", "setLed")
		require.NoError(t, err)
		require.Equal(t, hasEncoder, first != nil)
		q.body = `{}`
		second, err := d.Encoder(context.Background(), "acme", "dev-1", "setLed")
		require.NoError(t, err)
		require.Same(t, first, second)
		now = now.Add(61 * time.Second)
		_, err = d.Encoder(context.Background(), "acme", "dev-1", "setLed")
		require.Error(t, err)
		_, err = d.Encoder(context.Background(), "other", "dev-1", "setLed")
		require.Error(t, err)
		require.Contains(t, EncoderQuery, "commandKey encoder")
		require.True(t, strings.Contains(EncoderQuery, "deviceCommandVocabulary"))
	}
}
