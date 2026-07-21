// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package detmonitor

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

// A clean stream: raised/resolved edges tally per series, no violation, Intact.
func TestDetMonitorTalliesEdges(t *testing.T) {
	url := rawDetectionServer(t, func(conn *websocket.Conn, id string) {
		sendDetection(t, conn, id, "harness-edge-001", EdgeRaised)
		sendDetection(t, conn, id, "harness-edge-001", EdgeResolved)
		sendDetection(t, conn, id, "harness-edge-001", EdgeRaised)
		sendDetection(t, conn, id, "harness-safe-001", EdgeRaised) // (server-side "bug"): the run classifier judges this, not the watcher
		holdOpen(conn)
	})

	m := dialWatch(t, url, "harness-profile")
	eventually(t, 3*time.Second, func() bool { return m.Report().Observed >= 4 })
	require.NoError(t, m.Stop())

	assert.Equal(t, SeriesTally{Raised: 2, Resolved: 1}, m.Tally("harness-edge-001"))
	assert.Equal(t, SeriesTally{Raised: 1, Resolved: 0}, m.Tally("harness-safe-001"))
	assert.Zero(t, m.Tally("harness-edge-999"), "an unseen series tallies zero")
	assert.True(t, m.Intact(), "a clean, fully-parsed stream leaves the watcher intact")
	assert.Empty(t, m.Report().Violations)
}

// An edge value outside the closed {raised,resolved} set is malformed and must NOT
// be counted as either edge — miscounting it would corrupt the exact-K judgment.
func TestDetMonitorUnknownEdgeIsMalformed(t *testing.T) {
	url := rawDetectionServer(t, func(conn *websocket.Conn, id string) {
		sendDetection(t, conn, id, "harness-edge-001", "sideways")
		holdOpen(conn)
	})

	m := dialWatch(t, url, "harness-profile")
	eventually(t, 3*time.Second, func() bool { return hasKind(m, ViolMalformed) })
	require.NoError(t, m.Stop())

	assert.Zero(t, m.Tally("harness-edge-001"), "an unknown edge advances neither counter")
	assert.Zero(t, m.Report().Observed, "a malformed frame is not a valid observation")
	assert.False(t, m.Intact())
}

// A next frame that parses as JSON but is the wrong shape is malformed and must not
// count.
func TestDetMonitorBadPayloadIsMalformed(t *testing.T) {
	url := rawDetectionServer(t, func(conn *websocket.Conn, id string) {
		payload, _ := json.Marshal(map[string]interface{}{"data": map[string]interface{}{"detectionStream": 7}})
		_ = conn.WriteJSON(map[string]interface{}{"id": id, "type": "next", "payload": json.RawMessage(payload)})
		holdOpen(conn)
	})

	m := dialWatch(t, url, "harness-profile")
	eventually(t, 3*time.Second, func() bool { return hasKind(m, ViolMalformed) })
	require.NoError(t, m.Stop())
	assert.Zero(t, m.Report().Observed)
	assert.False(t, m.Intact())
}

// A dropped stream (not the watcher's own Stop) is a lost view — the watcher went
// blind and its silence proves nothing.
func TestDetMonitorDropIsLostView(t *testing.T) {
	url := rawDetectionServer(t, func(conn *websocket.Conn, id string) {
		sendDetection(t, conn, id, "harness-edge-001", EdgeRaised)
		// return → abrupt close, no `complete`
	})

	m := dialWatch(t, url, "harness-profile")
	eventually(t, 3*time.Second, func() bool { return hasKind(m, ViolLostView) })
	_ = m.Stop()
	assert.False(t, m.Intact(), "a lost view can never be intact")
}

// A mid-run server `complete` (Err()==nil, NOT the watcher's own Stop) is a lost
// view — this pins the nil-err branch a mutation (`err != nil && !errors.Is(...)`)
// would otherwise leave green while opening a false-intact.
func TestDetMonitorServerCompleteIsLostView(t *testing.T) {
	url := rawDetectionServer(t, func(conn *websocket.Conn, id string) {
		sendDetection(t, conn, id, "harness-edge-001", EdgeRaised)
		_ = conn.WriteJSON(map[string]interface{}{"id": id, "type": "complete"})
		holdOpen(conn)
	})

	m := dialWatch(t, url, "harness-profile")
	eventually(t, 3*time.Second, func() bool { return hasKind(m, ViolLostView) })
	require.NoError(t, m.Stop())
	assert.False(t, m.Intact())
}

// A watcher that never subscribed is not intact — Intact requires an actual view,
// so an empty run can never read as a clean detection verdict.
func TestDetMonitorUnwatchedNotIntact(t *testing.T) {
	m := New(nil)
	assert.False(t, m.Intact(), "a watcher that never subscribed has no testimony to trust")
}

// --- helpers -----------------------------------------------------------------

func dialWatch(t *testing.T, url, profileToken string) *Monitor {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	m, err := Dial(ctx, url, nil)
	require.NoError(t, err)
	require.NoError(t, m.Watch(ctx, profileToken))
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

// rawDetectionServer speaks the server half of graphql-transport-ws: it acks the
// connection_init, reads the single subscribe (the detectionStream), then hands
// control to script to emit detection frames on that subscription id.
func rawDetectionServer(t *testing.T, script func(conn *websocket.Conn, subID string)) string {
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
			ID string `json:"id"`
		}
		if err := conn.ReadJSON(&sub); err != nil {
			return
		}
		script(conn, sub.ID)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func sendDetection(t *testing.T, conn *websocket.Conn, subID, series, edge string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]interface{}{
		"data": map[string]interface{}{
			"detectionStream": map[string]interface{}{
				"ruleToken":    "harness-overtemp",
				"series":       series,
				"edge":         edge,
				"occurredTime": "2026-07-21T10:00:00Z",
			},
		},
	})
	require.NoError(t, conn.WriteJSON(map[string]interface{}{
		"id": subID, "type": "next", "payload": json.RawMessage(payload),
	}))
}

// holdOpen keeps the server side reading so the socket stays alive until the
// watcher's Stop closes it.
func holdOpen(conn *websocket.Conn) {
	for {
		var m map[string]interface{}
		if err := conn.ReadJSON(&m); err != nil {
			return
		}
	}
}
