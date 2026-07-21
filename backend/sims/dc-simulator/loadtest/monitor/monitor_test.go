// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- pure checker logic (fast, exhaustive) -----------------------------------

func TestDeviceCheckerInvariants(t *testing.T) {
	const tok = "devicepulse-00001"

	t.Run("equal stamps are not a regression (second-truncated ties)", func(t *testing.T) {
		d := &deviceChecker{token: tok}
		assert.Empty(t, d.check(ev(tok, "2026-07-21T10:00:00Z")))
		assert.Empty(t, d.check(ev(tok, "2026-07-21T10:00:00Z")), "an equal stamp is a legit tie under load")
		assert.Empty(t, d.check(ev(tok, "2026-07-21T10:00:01Z")))
	})

	t.Run("strict regression is a monotonicity violation", func(t *testing.T) {
		d := &deviceChecker{token: tok}
		d.check(ev(tok, "2026-07-21T10:00:05Z"))
		vs := d.check(ev(tok, "2026-07-21T10:00:04Z"))
		require.Len(t, vs, 1)
		assert.Equal(t, ViolMonotonicity, vs[0].Kind)
	})

	t.Run("wrong device is a membership violation", func(t *testing.T) {
		d := &deviceChecker{token: tok}
		vs := d.check(ev("devicepulse-00099", "2026-07-21T10:00:00Z"))
		require.Len(t, vs, 1)
		assert.Equal(t, ViolMembership, vs[0].Kind)
	})

	t.Run("empty/garbage occurredTime is malformed", func(t *testing.T) {
		d := &deviceChecker{token: tok}
		assert.Equal(t, ViolMalformed, d.check(ev(tok, ""))[0].Kind)
		assert.Equal(t, ViolMalformed, d.check(ev(tok, "not-a-time"))[0].Kind)
	})

	t.Run("a regression after a malformed event still compares to the last GOOD stamp", func(t *testing.T) {
		d := &deviceChecker{token: tok}
		d.check(ev(tok, "2026-07-21T10:00:05Z"))
		d.check(ev(tok, "")) // malformed: must not clobber lastOccurred
		vs := d.check(ev(tok, "2026-07-21T10:00:04Z"))
		require.Len(t, vs, 1)
		assert.Equal(t, ViolMonotonicity, vs[0].Kind, "malformed event must not reset the monotonicity baseline")
	})
}

func ev(token, occurred string) measurementEvent {
	return measurementEvent{DeviceToken: token, OccurredTime: occurred, Name: "temp"}
}

// --- the whole monitor, driven through the real graphqlws client -------------

func TestMonitorCleanStream(t *testing.T) {
	url := rawEventServer(t, func(conn *websocket.Conn, id, dt string) {
		sendMeasurement(t, conn, id, dt, "2026-07-21T10:00:00Z")
		sendMeasurement(t, conn, id, dt, "2026-07-21T10:00:00Z") // tie
		sendMeasurement(t, conn, id, dt, "2026-07-21T10:00:01Z")
		holdOpen(conn)
	})

	m := dialWatch(t, url, "devicepulse-00001")
	eventually(t, 3*time.Second, func() bool { return m.Report().Observed >= 3 })
	require.NoError(t, m.Stop())

	r := m.Report()
	assert.Equal(t, 3, r.Observed)
	assert.Equal(t, 3, r.ByDevice["devicepulse-00001"])
	assert.Empty(t, r.Violations)
	assert.True(t, r.Passed())
}

// Two devices on one client: frames must route by id to the right checker, and
// coverage must be attributed per device. A token/subscription binding bug would
// surface here as spurious membership violations.
func TestMonitorMultiDeviceRouting(t *testing.T) {
	const d1, d2 = "devicepulse-00001", "devicepulse-00002"
	up := websocket.Upgrader{Subprotocols: []string{"graphql-transport-ws"}, CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var init map[string]interface{}
		if conn.ReadJSON(&init) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]interface{}{"type": "connection_ack"})
		// Read both subscribes; map each id to the device token it asked for.
		byID := map[string]string{}
		for i := 0; i < 2; i++ {
			var sub struct {
				ID      string `json:"id"`
				Payload struct {
					Variables map[string]interface{} `json:"variables"`
				} `json:"payload"`
			}
			if conn.ReadJSON(&sub) != nil {
				return
			}
			dt, _ := sub.Payload.Variables["deviceToken"].(string)
			byID[sub.ID] = dt
		}
		// Emit two in-order events per subscription, interleaved.
		for _, ts := range []string{"2026-07-21T10:00:00Z", "2026-07-21T10:00:01Z"} {
			for id, dt := range byID {
				sendMeasurement(t, conn, id, dt, ts)
			}
		}
		holdOpen(conn)
	}))
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	m := dialWatch(t, url, d1, d2)
	eventually(t, 3*time.Second, func() bool { return m.Report().Observed >= 4 })
	require.NoError(t, m.Stop())

	r := m.Report()
	assert.Equal(t, 2, r.Cohort)
	assert.Equal(t, 4, r.Observed)
	assert.Equal(t, 2, r.ByDevice[d1])
	assert.Equal(t, 2, r.ByDevice[d2], "coverage attributed per device, routed by id")
	assert.Empty(t, r.Violations)
	assert.True(t, r.Passed())
}

func TestMonitorCatchesRegression(t *testing.T) {
	url := rawEventServer(t, func(conn *websocket.Conn, id, dt string) {
		sendMeasurement(t, conn, id, dt, "2026-07-21T10:00:05Z")
		sendMeasurement(t, conn, id, dt, "2026-07-21T10:00:04Z") // regression
		holdOpen(conn)
	})

	m := dialWatch(t, url, "devicepulse-00001")
	eventually(t, 3*time.Second, func() bool { return hasKind(m, ViolMonotonicity) })
	require.NoError(t, m.Stop())
	assert.False(t, m.Report().Passed())
}

func TestMonitorCatchesMembershipBreach(t *testing.T) {
	url := rawEventServer(t, func(conn *websocket.Conn, id, _ string) {
		// The subscription is for device 1, but the server leaks device 2's event.
		sendMeasurement(t, conn, id, "devicepulse-00002", "2026-07-21T10:00:00Z")
		holdOpen(conn)
	})

	m := dialWatch(t, url, "devicepulse-00001")
	eventually(t, 3*time.Second, func() bool { return hasKind(m, ViolMembership) })
	require.NoError(t, m.Stop())
	assert.False(t, m.Report().Passed())
}

// A dropped subscription (not the monitor's own Stop) is a LOST VIEW — the
// monitor went blind, and its silence must not read as "nothing bad happened".
func TestMonitorDropIsLostView(t *testing.T) {
	url := rawEventServer(t, func(conn *websocket.Conn, id, dt string) {
		sendMeasurement(t, conn, id, dt, "2026-07-21T10:00:00Z")
		// return → abrupt close, no `complete`
	})

	m := dialWatch(t, url, "devicepulse-00001")
	eventually(t, 3*time.Second, func() bool { return hasKind(m, ViolLostView) })
	_ = m.Stop()
	assert.False(t, m.Report().Passed(), "a lost view can never be a pass")
}

// A cohort device that delivered NOTHING is a blind device — even with a clean
// stop and no per-event violation, the monitor never watched it, so the run fails.
// This is the F1 hole: aggregate observed>0 alone would have hidden it.
func TestMonitorBlindDeviceFails(t *testing.T) {
	url := rawEventServer(t, func(conn *websocket.Conn, _, _ string) {
		holdOpen(conn) // ack + subscribe, but never a single event
	})

	m := dialWatch(t, url, "devicepulse-00001")
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, m.Stop())

	r := m.Report()
	assert.Zero(t, r.Observed)
	require.Len(t, r.Violations, 1)
	assert.Equal(t, ViolBlindDevice, r.Violations[0].Kind, "a zero-delivery cohort device must be flagged blind")
	assert.False(t, r.Passed())
}

// A mid-run server `complete` (Err()==nil, NOT the monitor's own Stop) is a lost
// view — the stream ended under the monitor and it went blind for the remainder.
// This pins the nil-err branch of the lost-view classification, which a mutation
// (`err != nil && !errors.Is(...)`) would otherwise leave green while opening a
// false-PASS.
func TestMonitorServerCompleteIsLostView(t *testing.T) {
	url := rawEventServer(t, func(conn *websocket.Conn, id, dt string) {
		sendMeasurement(t, conn, id, dt, "2026-07-21T10:00:00Z")
		_ = conn.WriteJSON(map[string]interface{}{"id": id, "type": "complete"})
		holdOpen(conn)
	})

	m := dialWatch(t, url, "devicepulse-00001")
	eventually(t, 3*time.Second, func() bool { return hasKind(m, ViolLostView) })
	require.NoError(t, m.Stop())
	assert.False(t, m.Report().Passed())
}

// A next frame that parses as JSON but whose measurementStream is the wrong shape
// is malformed and must NOT count as an observation — coverage is only advanced by
// a genuinely parsed event.
func TestMonitorBadPayloadNotCounted(t *testing.T) {
	url := rawEventServer(t, func(conn *websocket.Conn, id, _ string) {
		payload, _ := json.Marshal(map[string]interface{}{"data": map[string]interface{}{"measurementStream": 5}})
		_ = conn.WriteJSON(map[string]interface{}{"id": id, "type": "next", "payload": json.RawMessage(payload)})
		holdOpen(conn)
	})

	m := dialWatch(t, url, "devicepulse-00001")
	eventually(t, 3*time.Second, func() bool { return hasKind(m, ViolMalformed) })
	require.NoError(t, m.Stop())

	r := m.Report()
	assert.Zero(t, r.Observed, "a malformed frame is not a valid observation")
	assert.False(t, r.Passed())
}

// --- helpers -----------------------------------------------------------------

func dialWatch(t *testing.T, url string, tokens ...string) *Monitor {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	m, err := Dial(ctx, url, nil)
	require.NoError(t, err)
	require.NoError(t, m.Watch(ctx, tokens))
	return m
}

func hasKind(m *Monitor, kind string) bool {
	for _, v := range m.Report().Violations {
		if v.Kind == kind {
			return true
		}
	}
	return false
}

func eventually(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// rawEventServer speaks the server half of graphql-transport-ws: it acks the
// connection_init, reads the first subscribe (extracting its id + deviceToken
// variable), then hands control to script to emit frames for that subscription.
func rawEventServer(t *testing.T, script func(conn *websocket.Conn, subID, deviceToken string)) string {
	t.Helper()
	up := websocket.Upgrader{
		Subprotocols: []string{"graphql-transport-ws"},
		CheckOrigin:  func(*http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var init map[string]interface{}
		if err := conn.ReadJSON(&init); err != nil {
			return
		}
		_ = conn.WriteJSON(map[string]interface{}{"type": "connection_ack"})
		var sub struct {
			ID      string `json:"id"`
			Payload struct {
				Variables map[string]interface{} `json:"variables"`
			} `json:"payload"`
		}
		if err := conn.ReadJSON(&sub); err != nil {
			return
		}
		dt, _ := sub.Payload.Variables["deviceToken"].(string)
		script(conn, sub.ID, dt)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func sendMeasurement(t *testing.T, conn *websocket.Conn, subID, deviceToken, occurredTime string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]interface{}{
		"data": map[string]interface{}{
			"measurementStream": map[string]interface{}{
				"deviceToken":  deviceToken,
				"occurredTime": occurredTime,
				"name":         "temp",
				"value":        21.5,
			},
		},
	})
	require.NoError(t, conn.WriteJSON(map[string]interface{}{
		"id": subID, "type": "next", "payload": json.RawMessage(payload),
	}))
}

// holdOpen keeps the server side reading (so the socket stays alive and responds
// to the client's close) until the monitor's Stop closes the connection.
func holdOpen(conn *websocket.Conn) {
	for {
		var m map[string]interface{}
		if err := conn.ReadJSON(&m); err != nil {
			return
		}
	}
}
