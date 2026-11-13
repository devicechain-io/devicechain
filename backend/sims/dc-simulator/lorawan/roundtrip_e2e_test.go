// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lorawan

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
	"github.com/devicechain-io/dc-outbound-connectors/lpwan"
	"github.com/stretchr/testify/require"
)

func roundTripTarget(t *testing.T, base, key string) connectorspec.LoRaWANDownlinkTarget {
	t.Helper()
	u, err := url.Parse(base)
	require.NoError(t, err)
	return connectorspec.LoRaWANDownlinkTarget{
		NetworkServer: connectorspec.NetworkServerChirpStackV4, BaseURL: u, APIKey: key, Integration: "lora",
	}
}

// The adapter copies the encoder's result into Params. These are the thermostat
// bytes for {"setpoint":21.5,"mode":3} on its confirmed command's port.
func roundTripParams() lpwan.Params {
	return lpwan.Params{FPort: 12, Confirmed: true, Data: []byte{0x00, 0xd7, 0x03}}
}

func TestDownlinkRoundTrip(t *testing.T) {
	for _, mode := range []string{"ack", "nack"} {
		t.Run(mode, func(t *testing.T) {
			var logs bytes.Buffer
			d, err := NewDownlink("api-key", mode, []string{testEUI}, &logs)
			require.NoError(t, err)
			queue := httptest.NewServer(d)
			defer queue.Close()
			d.Listening(queue.Listener.Addr().String())
			client := lpwan.NewChirpStackWithTransport(http.DefaultTransport)
			target := roundTripTarget(t, queue.URL, "api-key")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			expires := time.Now().Add(time.Minute)
			var mu sync.Mutex
			var posts []received
			var queueID string
			var enqueueErr error
			ups := 0
			integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				event := r.URL.Query().Get("event")
				posts = append(posts, received{event: event, contentType: r.Header.Get("Content-Type"), body: body})
				if event == "up" {
					ups++
					if ups == 1 {
						// Run's pre-POST queue snapshot must exclude this enqueue.
						queueID, enqueueErr = client.Enqueue(ctx, target, testEUI, roundTripParams(), expires)
					}
				}
				w.WriteHeader(http.StatusAccepted)
			}))
			defer integration.Close()
			c := cfg(ChirpStack)
			c.URL = integration.URL + "/api/integrations/acme/lora"
			c.DevEUIs, c.Token, c.HostHeader = []string{testEUI}, "integration-secret", "tenant.example"
			c.Uplinks, c.Interval, c.Downlink = 2, 50*time.Millisecond, d
			sum, err := Run(ctx, c)
			require.NoError(t, err)
			mu.Lock()
			got := append([]received(nil), posts...)
			id, sendErr := queueID, enqueueErr
			mu.Unlock()
			require.NoError(t, sendErr)
			require.NotEmpty(t, id)
			require.Equal(t, 1, *sum.Queued)
			var events []string
			for _, p := range got {
				events = append(events, p.event)
			}
			require.Equal(t, []string{"up", "up", "txack", "ack"}, events)

			var queued []map[string]any
			dec := json.NewDecoder(strings.NewReader(logs.String()))
			for dec.More() {
				var entry map[string]any
				require.NoError(t, dec.Decode(&entry))
				if entry["event"] == "downlink_queued" {
					queued = append(queued, entry)
				}
			}
			require.Len(t, queued, 1)
			require.Equal(t, id, queued[0]["id"])
			require.Equal(t, "00d703", queued[0]["hex"])
			require.Equal(t, float64(12), queued[0]["fPort"])
			require.Equal(t, true, queued[0]["confirmed"])

			pending := &roundTripPending{}
			// Same fields as Adapter.awaitAck, including the claimed dispatch nonce
			// and the earlier of the command expiry and acknowledgement deadline.
			tenantCtx := core.WithTenant(context.Background(), "acme")
			require.NoError(t, pending.Add(tenantCtx, lpwan.Pending{
				Tenant: "acme", Integration: target.Integration, DeviceToken: "dev-1", DevEUI: testEUI,
				CommandToken: "cmd-1", DispatchNonce: "claimed-nonce", QueueItemID: strings.ToLower(id),
				DeadlineAt: minTime(expires, time.Now().Add(lpwan.DefaultAckWait)),
			}))
			reader := &roundTripReader{messages: make(chan messaging.Message, 2)}
			for _, post := range got[2:] {
				// decodeReport uses integrationcodec.Decode with the real prepared
				// ChirpStack descriptor and DownlinkReports request flag.
				ev := decodeReport(t, post)
				require.Equal(t, post.event, ev.Kind)
				require.Equal(t, testEUI, ev.Identifier)
				require.Equal(t, id, ev.QueueItemID)
				// Mirror handleDownlinkReports after resolving the integration's device.
				report := cmddownlink.DownlinkReport{
					Integration: target.Integration, Kind: ev.Kind, DevEUI: ev.Identifier,
					QueueItemID: ev.QueueItemID, DeviceRef: ev.DeviceRef, DeviceToken: "dev-1",
					Attempt: ev.Attempt, ErrorNamespace: ev.ErrorNamespace, ErrorName: ev.ErrorName,
					Acknowledged: ev.Acknowledged, FCntDown: ev.FCntDown,
				}
				body, err := json.Marshal(report)
				require.NoError(t, err)
				// onDownlinkReport publishes this shape; the broker adds the tenant subject.
				msg := messaging.Message{Key: []byte(report.QueueItemID), Value: body}
				msg.Subject, msg.NumDelivered = "inst.acme.lpwan-downlink-events", 1
				reader.messages <- msg
			}
			responses := &roundTripResponses{}
			feed := lpwan.NewAckFeed(reader, responses, pending, nil, lpwan.AckFeedOptions{
				Workers: 1, ReadPacer: core.NewReadPacer(nil, "roundtrip"), SweepEvery: time.Hour,
				ResponseAttempts: 1, ResponseBackoff: time.Millisecond,
			})
			feed.Start()
			t.Cleanup(feed.Stop)
			require.Eventually(t, func() bool { return responses.count() == 1 }, 3*time.Second, time.Millisecond)
			feed.Stop()
			require.Len(t, responses.envs, 1)
			require.Equal(t, []string{"acme"}, responses.tenants)
			require.Equal(t, []string{"dev-1"}, responses.devices)
			env := responses.envs[0]
			require.Equal(t, "cmd-1", env.CommandToken)
			require.Equal(t, "claimed-nonce", env.DispatchNonce)
			require.NotNil(t, env.Payload)
			if mode == "ack" {
				// SUCCESSFUL / SUCCEEDED: the device answered successfully.
				require.True(t, env.Success)
				require.Empty(t, env.Reach)
				require.Nil(t, env.Error)
				require.JSONEq(t, `{"stage":"acknowledged","queueItemId":"`+id+`","fCntDown":0}`, *env.Payload)
			} else {
				// TIMEOUT / UNKNOWN: the device may have received the frame.
				require.False(t, env.Success)
				require.Equal(t, cmddownlink.ReachUnanswered, env.Reach)
				require.NotNil(t, env.Error)
				require.JSONEq(t, `{"reason":"NOT_ACKNOWLEDGED"}`, *env.Payload)
			}
			require.Nil(t, pending.row, "settled pending record must be removed")
			require.True(t, pending.transmitted, "txack must record transmission before ack settles")
		})
	}

	for _, tc := range []struct{ name, key, eui, text string }{
		{"wrong API key", "wrong-key", testEUI, "the network server refused the downlink (HTTP 401, UNAUTHENTICATED)"},
		{"unknown DevEUI", "api-key", "0000000000000000", "the network server refused the downlink (HTTP 404, NOT_FOUND)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			d, err := NewDownlink("api-key", "ack", []string{testEUI}, &logs)
			require.NoError(t, err)
			server := httptest.NewServer(d)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			id, err := lpwan.NewChirpStackWithTransport(http.DefaultTransport).Enqueue(
				ctx, roundTripTarget(t, server.URL, tc.key), tc.eui, roundTripParams(), time.Now().Add(time.Minute))
			require.Empty(t, id)
			var sendErr *lpwan.SendError
			require.ErrorAs(t, err, &sendErr)
			require.Equal(t, cmddownlink.ReachUnsent, sendErr.Reach, "401 and 404 are definitive refusals")
			require.False(t, sendErr.Blocked)
			require.Equal(t, tc.text, sendErr.Text, "the real client must read the fake's gRPC-coded refusal")
			require.Zero(t, d.count())
		})
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

type roundTripReader struct{ messages chan messaging.Message }

func (r *roundTripReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	select {
	case msg := <-r.messages:
		return msg, nil
	case <-ctx.Done():
		return messaging.Message{}, ctx.Err()
	}
}
func (*roundTripReader) HandleResponse(error) {}

type roundTripResponses struct {
	mu               sync.Mutex
	envs             []cmddownlink.ResponseEnvelope
	tenants, devices []string
}

func (w *roundTripResponses) WriteToDevice(ctx context.Context, device string, msgs ...messaging.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	tenant, _ := core.TenantFromContext(ctx)
	for _, msg := range msgs {
		var env cmddownlink.ResponseEnvelope
		if err := json.Unmarshal(msg.Value, &env); err != nil {
			return err
		}
		w.envs = append(w.envs, env)
		w.tenants = append(w.tenants, tenant)
		w.devices = append(w.devices, device)
	}
	return nil
}
func (w *roundTripResponses) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.envs)
}

// One-record PendingStore with the same tenant/id matching and settle lease as
// ackfeed_test.go's in-memory store. All access is serialized with the feed.
type roundTripPending struct {
	mu          sync.Mutex
	row         *lpwan.Pending
	lease       time.Time
	transmitted bool
}

func (s *roundTripPending) Add(_ context.Context, p lpwan.Pending) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.ID = 1
	s.row = &p
	return nil
}
func (s *roundTripPending) Lookup(_ context.Context, tenant, id string) (*lpwan.Pending, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row == nil || s.row.Tenant != tenant || s.row.QueueItemID != id {
		return nil, lpwan.ErrNoPending
	}
	p := *s.row
	return &p, nil
}
func (s *roundTripPending) MarkTransmitted(_ context.Context, tenant string, id uint, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row != nil && s.row.Tenant == tenant && s.row.ID == id && s.row.TransmittedAt == nil {
		s.row.TransmittedAt = &at
		s.transmitted = true
	}
	return nil
}
func (s *roundTripPending) Claim(_ context.Context, tenant string, id uint, now time.Time, lease time.Duration) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row == nil || s.row.Tenant != tenant || s.row.ID != id || s.lease.After(now) {
		return time.Time{}, false, nil
	}
	s.lease = now.Add(lease)
	return s.lease, true, nil
}
func (s *roundTripPending) Delete(_ context.Context, tenant string, id uint, token time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row != nil && s.row.Tenant == tenant && s.row.ID == id && s.lease.Equal(token) {
		s.row = nil
	}
	return nil
}
func (s *roundTripPending) DeleteGone(_ context.Context, tenant string, id uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row != nil && s.row.Tenant == tenant && s.row.ID == id {
		s.row = nil
	}
	return nil
}
func (s *roundTripPending) Expired(_ context.Context, now time.Time, limit int) ([]lpwan.Pending, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row != nil && limit > 0 && !s.row.DeadlineAt.After(now) && !s.lease.After(now) {
		return []lpwan.Pending{*s.row}, nil
	}
	return nil, nil
}
