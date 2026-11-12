// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lorawan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/integrationcodec"
	"github.com/stretchr/testify/require"
)

const testEUI = "70B3D57ED0050004"

// Same fields and envelope as v1.4.0 outbound-connectors/lpwan/chirpstack.go.
type csQueueItem struct {
	DevEui    string `json:"devEui"`
	FPort     int    `json:"fPort"`
	Confirmed bool   `json:"confirmed"`
	Data      string `json:"data"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

func enqueue(base string, item csQueueItem) (string, error) {
	body, err := json.Marshal(map[string]csQueueItem{"queueItem": item})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/devices/"+item.DevEui+"/queue", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer api-key")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		ID string `json:"id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("enqueue status %d", resp.StatusCode)
	}
	return result.ID, err
}

type received struct {
	event, path, auth, host, contentType string
	body                                 []byte
}

func runDownlink(t *testing.T, mode string, confirmed bool, rounds int) ([]received, Summary, string, string) {
	t.Helper()
	var logs bytes.Buffer
	d, err := NewDownlink("api-key", mode, []string{testEUI}, &logs)
	require.NoError(t, err)
	queue := httptest.NewServer(d)
	defer queue.Close()
	d.Listening(queue.Listener.Addr().String())
	var mu sync.Mutex
	var got []received
	var id string
	var enqueueErr error
	ups := 0
	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		event := r.URL.Query().Get("event")
		got = append(got, received{event, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Host, r.Header.Get("Content-Type"), body})
		if event == "up" {
			ups++
			// Enqueue during uplink 1's POST: its pre-POST snapshot must exclude this item.
			if ups == 1 {
				id, enqueueErr = enqueue(queue.URL, csQueueItem{strings.ToLower(testEUI), 12, confirmed, "ANcD", "2030-01-01T00:00:00Z"})
			}
		}
		w.WriteHeader(202)
	}))
	defer integration.Close()
	c := cfg(ChirpStack)
	c.URL = integration.URL + "/api/integrations/acme/lora"
	c.DevEUIs = []string{testEUI}
	c.Token = "integration-secret"
	c.HostHeader = "tenant.example"
	c.Uplinks = rounds
	c.Interval = 50 * time.Millisecond
	c.Downlink = d
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sum, err := Run(ctx, c)
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	require.NoError(t, enqueueErr)
	require.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, id)
	require.Equal(t, 1, *sum.Queued)
	return got, sum, id, logs.String()
}

func decodeReport(t *testing.T, r received) integrationcodec.DownlinkEvent {
	t.Helper()
	d := &integrationcodec.Descriptor{Token: "lora", Type: integrationcodec.ChirpStackV4, Enabled: true, Identity: "DEV_EUI"}
	require.NoError(t, d.Prepare())
	res, err := integrationcodec.Decode(d, &integrationcodec.Request{DownlinkReports: true, Query: url.Values{"event": {r.event}}, ContentType: r.contentType, Body: r.body, ReceivedAt: time.Now()})
	require.NoError(t, err)
	require.Len(t, res.Downlinks, 1)
	return res.Downlinks[0]
}

// Catches M1 (same-uplink or early reporting), M2 (wrong queue id), M6
// (reversed reports), and stopping with pending items after the final round.
func TestDownlinkClassATimingAndCodec(t *testing.T) {
	for _, rounds := range []int{2, 1} {
		t.Run(fmt.Sprintf("uplinks=%d", rounds), func(t *testing.T) {
			got, sum, id, logs := runDownlink(t, "ack", true, rounds)
			var events []string
			for _, r := range got {
				events = append(events, r.event)
			}
			require.Equal(t, []string{"up", "up", "txack", "ack"}, events, "reports must follow the NEXT uplink, txack first")
			require.Equal(t, 2, sum.Sent, "a queue during the final configured uplink needs an extra real uplink")
			require.Equal(t, map[string]int{"202": 2}, sum.Statuses)
			require.Equal(t, map[string]map[string]int{"txack": {"202": 1}, "ack": {"202": 1}}, sum.Reported)
			for _, r := range got[2:] {
				require.Equal(t, "Bearer integration-secret", r.auth)
				require.Equal(t, "tenant.example", r.host)
				require.Equal(t, "/api/integrations/acme/lora?event="+r.event, r.path)
				ev := decodeReport(t, r)
				require.Equal(t, r.event, ev.Kind)
				require.Equal(t, testEUI, ev.Identifier)
				require.Equal(t, id, ev.QueueItemID, "reports must correlate with the enqueue response")
				require.Zero(t, ev.FCntDown)
				var body map[string]any
				require.NoError(t, json.Unmarshal(r.body, &body))
				require.Equal(t, id, body["queueItemId"])
				stamp, err := time.Parse(time.RFC3339Nano, body["time"].(string))
				require.NoError(t, err)
				require.Equal(t, time.UTC, stamp.Location())
				require.Equal(t, map[string]any{"tenantName": "sim", "applicationName": "sim", "deviceProfileName": "sim", "deviceName": "sim-" + strings.ToLower(testEUI), "devEui": testEUI}, body["deviceInfo"])
				if r.event == "ack" {
					require.True(t, ev.Acknowledged)
					require.Equal(t, true, body["acknowledged"])
				} else {
					require.Equal(t, gatewayID(0), body["gatewayId"])
					require.Equal(t, map[string]any{"frequency": float64(868100000), "power": float64(14)}, body["txInfo"])
				}
			}
			var second map[string]any
			require.NoError(t, json.Unmarshal(got[1].body, &second))
			require.Equal(t, float64(1), second["fCnt"], "extra rounds must keep the uplink counter")
			require.Contains(t, logs, `"event":"downlink_listening"`)
			require.Contains(t, logs, `"hex":"00d703"`)
			require.Contains(t, logs, `"event":"downlink_reported"`)
		})
	}
}

// Catches M3: nack accidentally reporting a positive acknowledgement.
func TestDownlinkNack(t *testing.T) {
	got, _, _, _ := runDownlink(t, "nack", true, 2)
	require.Len(t, got, 4)
	require.Equal(t, "txack", got[2].event)
	require.Equal(t, "ack", got[3].event)
	var body struct {
		Acknowledged *bool `json:"acknowledged"`
	}
	require.NoError(t, json.Unmarshal(got[3].body, &body))
	require.NotNil(t, body.Acknowledged)
	require.False(t, *body.Acknowledged)
	require.False(t, decodeReport(t, got[3]).Acknowledged)
}

// Catches M4: none accidentally posting reports or extending the run.
func TestDownlinkNone(t *testing.T) {
	got, sum, _, logs := runDownlink(t, "none", true, 2)
	require.Len(t, got, 2, "none must send only the configured uplinks")
	for _, r := range got {
		require.Equal(t, "up", r.event)
	}
	require.Equal(t, 2, sum.Sent)
	require.Empty(t, sum.Reported["ack"])
	require.NotContains(t, logs, "downlink_reported")
}

// Catches treating an unconfirmed item as confirmed (or failing to log acceptance).
func TestDownlinkUnconfirmed(t *testing.T) {
	got, sum, _, logs := runDownlink(t, "ack", false, 2)
	require.Len(t, got, 2)
	for _, r := range got {
		require.Equal(t, "up", r.event)
	}
	require.Empty(t, sum.Reported["txack"])
	require.Contains(t, logs, `"confirmed":false`)
	require.Contains(t, logs, `"event":"downlink_queued"`)
}

// Catches M5 (skipping auth), queuing refused items, and incorrect gRPC refusal codes.
func TestDownlinkAuthAndRefusals(t *testing.T) {
	var logs bytes.Buffer
	d, err := NewDownlink("api-key", "ack", []string{testEUI}, &logs)
	require.NoError(t, err)
	server := httptest.NewServer(d)
	defer server.Close()
	body := `{"queueItem":{"devEui":"` + testEUI + `","fPort":12,"confirmed":true,"data":"ANcD"}}`
	for _, tc := range []struct {
		name, key, path, body, method string
		status, code                  int
	}{
		{"absent", "", "/api/devices/" + testEUI + "/queue", body, "POST", 401, 16},
		{"wrong", "Bearer do-not-log-me", "/api/devices/" + testEUI + "/queue", body, "POST", 401, 16},
		{"unknown", "Bearer api-key", "/api/devices/0000000000000000/queue", body, "POST", 404, 5},
		{"bad-json", "Bearer api-key", "/api/devices/" + testEUI + "/queue", "{", "POST", 400, 3},
		{"mismatch", "Bearer api-key", "/api/devices/" + testEUI + "/queue", strings.ReplaceAll(body, testEUI, "0000000000000000"), "POST", 400, 3},
		{"port-zero", "Bearer api-key", "/api/devices/" + testEUI + "/queue", strings.ReplaceAll(body, `"fPort":12`, `"fPort":0`), "POST", 400, 3},
		{"port-high", "Bearer api-key", "/api/devices/" + testEUI + "/queue", strings.ReplaceAll(body, `"fPort":12`, `"fPort":224`), "POST", 400, 3},
		{"base64", "Bearer api-key", "/api/devices/" + testEUI + "/queue", strings.ReplaceAll(body, "ANcD", "_bad"), "POST", 400, 3},
		{"oversize", "Bearer api-key", "/api/devices/" + testEUI + "/queue", body + strings.Repeat(" ", 64<<10), "POST", 400, 3},
		{"path", "Bearer api-key", "/other", body, "POST", 404, 5},
		{"method", "Bearer api-key", "/api/devices/" + testEUI + "/queue", body, "GET", 405, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, server.URL+tc.path, strings.NewReader(tc.body))
			require.NoError(t, err)
			req.Header.Set("Authorization", tc.key)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, tc.status, resp.StatusCode)
			var refusal struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&refusal))
			require.Equal(t, tc.code, refusal.Code)
			require.NotEmpty(t, refusal.Message)
		})
	}
	require.Zero(t, d.count(), "refusals must not count as accepted items")
	id, err := enqueue(server.URL+"/prefix", csQueueItem{strings.ToLower(testEUI), 12, false, "", ""})
	require.NoError(t, err, "right auth, prefix and empty data must be accepted")
	require.NotEmpty(t, id)
	require.Equal(t, 1, d.count())
	var events []string
	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		events = append(events, r.URL.Query().Get("event"))
		w.WriteHeader(202)
	}))
	defer integration.Close()
	c := cfg(ChirpStack)
	c.DevEUIs = []string{testEUI}
	c.Downlink = d
	c.URL = integration.URL
	c.Uplinks = 1
	_, err = Run(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"up"}, events, "refusals must not queue reports")
	require.NotContains(t, logs.String(), "do-not-log-me")
}

// Catches accepting TTS downlinks, rejecting continuous mode, or changing legacy summary JSON.
func TestDownlinkValidateAndSummary(t *testing.T) {
	d, err := NewDownlink("api-key", "ack", []string{testEUI}, io.Discard)
	require.NoError(t, err)
	c := cfg(ChirpStack)
	c.Uplinks = 0
	require.EqualError(t, c.Validate(), "--uplinks must be at least 1")
	c.Downlink = d
	require.NoError(t, c.Validate())
	c.NS = TTS
	require.ErrorContains(t, c.Validate(), "requires --lorawan-ns chirpstack")
	_, err = NewDownlink("", "ack", nil, nil)
	require.ErrorContains(t, err, "DC_SIM_LORAWAN_API_KEY")
	_, err = NewDownlink("key", "other", nil, nil)
	require.ErrorContains(t, err, "ack, nack or none")
	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer integration.Close()
	c = cfg(ChirpStack)
	c.URL, c.Uplinks, c.RunID = integration.URL, 1, "r"
	sum, err := Run(context.Background(), c)
	require.NoError(t, err)
	body, err := json.Marshal(sum)
	require.NoError(t, err)
	require.JSONEq(t, `{"ns":"chirpstack","sent":1,"resent":0,"statuses":{"202":1},"errors":0,"runId":"r"}`, string(body))
	require.NotContains(t, string(body), "queued")
	require.NotContains(t, string(body), "reported")
}

// Catches continuous mode silently stopping at zero configured rounds or not honoring cancellation.
func TestDownlinkContinuousUntilCanceled(t *testing.T) {
	d, err := NewDownlink("api-key", "ack", []string{testEUI}, io.Discard)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var mu sync.Mutex
	ups := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ups++
		if ups == 2 {
			cancel()
		}
		mu.Unlock()
		w.WriteHeader(202)
	}))
	defer srv.Close()
	c := cfg(ChirpStack)
	c.Downlink, c.URL, c.Uplinks, c.Interval = d, srv.URL, 0, 50*time.Millisecond
	sum, err := Run(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 2, sum.Sent)
	require.Zero(t, sum.Errors, "interruption is a normal end")
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, ups)
}

// Catches losing report failures, contaminating uplink statuses with report statuses,
// or not recording the first report transport error.
func TestDownlinkReportFailures(t *testing.T) {
	for _, transportFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(transportFailure), func(t *testing.T) {
			d, err := NewDownlink("api-key", "ack", []string{testEUI}, io.Discard)
			require.NoError(t, err)
			queue := httptest.NewServer(d)
			defer queue.Close()
			_, err = enqueue(queue.URL, csQueueItem{testEUI, 12, true, "ANcD", ""})
			require.NoError(t, err)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("event") == "up" {
					w.WriteHeader(202)
					return
				}
				if transportFailure {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return
				}
				w.WriteHeader(401)
			}))
			defer srv.Close()
			c := cfg(ChirpStack)
			c.Downlink, c.URL, c.DevEUIs, c.Uplinks = d, srv.URL, []string{testEUI}, 1
			sum, err := Run(context.Background(), c)
			require.NoError(t, err)
			require.Equal(t, map[string]int{"202": 1}, sum.Statuses)
			status := "401"
			if transportFailure {
				status = "0"
				require.Equal(t, 2, sum.Errors)
				require.NotEmpty(t, sum.FirstError)
			} else {
				require.Zero(t, sum.Errors)
			}
			require.Equal(t, map[string]map[string]int{"txack": {status: 1}, "ack": {status: 1}}, sum.Reported)
		})
	}
}

// Catches a global (rather than per-device) downlink counter and incrementing once per report.
func TestDownlinkCounters(t *testing.T) {
	devices := []string{testEUI, "70B3D57ED0050005"}
	d, err := NewDownlink("api-key", "ack", devices, io.Discard)
	require.NoError(t, err)
	queue := httptest.NewServer(d)
	defer queue.Close()
	for _, eui := range devices {
		for i := 0; i < 2; i++ {
			_, err := enqueue(queue.URL, csQueueItem{eui, 12, true, "ANcD", ""})
			require.NoError(t, err)
		}
	}
	var mu sync.Mutex
	counters := map[string][]int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("event") != "up" {
			body, _ := io.ReadAll(r.Body)
			var msg struct {
				DeviceInfo struct {
					DevEUI string `json:"devEui"`
				} `json:"deviceInfo"`
				FCntDown int64 `json:"fCntDown"`
			}
			_ = json.Unmarshal(body, &msg)
			mu.Lock()
			counters[msg.DeviceInfo.DevEUI] = append(counters[msg.DeviceInfo.DevEUI], msg.FCntDown)
			mu.Unlock()
		}
		w.WriteHeader(202)
	}))
	defer srv.Close()
	c := cfg(ChirpStack)
	c.Downlink, c.URL, c.DevEUIs, c.Uplinks = d, srv.URL, devices, 1
	sum, err := Run(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, 4, *sum.Queued)
	mu.Lock()
	defer mu.Unlock()
	for _, eui := range devices {
		require.Equal(t, []int64{0, 0, 1, 1}, counters[eui])
	}
}

// Catches accepting downlinks after Run drains while its HTTP queue is still listening.
func TestDownlinkRefusesAfterRun(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		confirmed bool
	}{
		{"ack", true}, {"ack", false}, {"none", true},
	} {
		t.Run(fmt.Sprintf("%s/confirmed=%t", tc.mode, tc.confirmed), func(t *testing.T) {
			var logs bytes.Buffer
			d, err := NewDownlink("api-key", tc.mode, []string{testEUI}, &logs)
			require.NoError(t, err)
			queue := httptest.NewServer(d)
			defer queue.Close()
			var mu sync.Mutex
			var events []string
			integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				events = append(events, r.URL.Query().Get("event"))
				mu.Unlock()
				w.WriteHeader(http.StatusAccepted)
			}))
			defer integration.Close()
			c := cfg(ChirpStack)
			c.Downlink, c.URL, c.DevEUIs, c.Uplinks = d, integration.URL, []string{testEUI}, 1
			sum, err := Run(context.Background(), c)
			require.NoError(t, err)
			require.Equal(t, 0, *sum.Queued)
			body, err := json.Marshal(map[string]csQueueItem{"queueItem": {testEUI, 12, tc.confirmed, "ANcD", ""}})
			require.NoError(t, err)
			req, err := http.NewRequest(http.MethodPost, queue.URL+"/api/devices/"+testEUI+"/queue", bytes.NewReader(body))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer api-key")
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			refusal, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusConflict, resp.StatusCode)
			require.JSONEq(t, `{"code":9,"message":"the simulated network server has stopped taking downlinks"}`, string(refusal))
			require.Equal(t, 0, *sum.Queued)
			require.Zero(t, d.count())
			require.Empty(t, sum.Reported["txack"])
			require.Empty(t, sum.Reported["ack"])
			mu.Lock()
			require.Equal(t, []string{"up"}, events)
			mu.Unlock()
			require.Contains(t, logs.String(), `"event":"downlink_refused"`)
		})
	}
}

// Catches dropping an item accepted while the final configured uplink POST is blocked.
func TestDownlinkEnqueuedDuringFinalPost(t *testing.T) {
	d, err := NewDownlink("api-key", "ack", []string{testEUI}, io.Discard)
	require.NoError(t, err)
	queue := httptest.NewServer(d)
	defer queue.Close()
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var mu sync.Mutex
	var events []string
	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		events = append(events, r.URL.Query().Get("event"))
		first := len(events) == 1
		mu.Unlock()
		if first {
			close(started)
			<-release
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer integration.Close()
	defer unblock()
	c := cfg(ChirpStack)
	c.Downlink, c.URL, c.DevEUIs, c.Uplinks = d, integration.URL, []string{testEUI}, 1
	c.Interval = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type result struct {
		sum Summary
		err error
	}
	done := make(chan result, 1)
	go func() {
		sum, err := Run(ctx, c)
		done <- result{sum, err}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("final uplink did not start")
	}
	_, err = enqueue(queue.URL, csQueueItem{testEUI, 12, true, "ANcD", ""})
	require.NoError(t, err)
	unblock()
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.Equal(t, 2, got.sum.Sent)
		require.Equal(t, 1, *got.sum.Queued)
		require.Equal(t, map[string]map[string]int{"txack": {"202": 1}, "ack": {"202": 1}}, got.sum.Reported)
	case <-ctx.Done():
		t.Fatal("run did not drain the accepted item")
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"up", "up", "txack", "ack"}, events)
}
