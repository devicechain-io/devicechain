// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

// laneApi records every applied edge, and how many were being applied at once, per alarm and overall.
type laneApi struct {
	*dmtest.MockApi
	mu       sync.Mutex
	applied  map[string][]string // tenant/device/alarmKey -> sequence tags in apply order
	inflight map[string]int
	overlap  bool // two edges of ONE alarm were applied at the same time
	cur, max int
	delay    time.Duration
}

func (a *laneApi) DevicesByToken(_ context.Context, tokens []string) ([]*model.Device, error) {
	d := &model.Device{}
	n, _ := strconv.Atoi(strings.TrimPrefix(tokens[0], "dev-"))
	d.ID = uint(n + 1)
	return []*model.Device{d}, nil
}

func (a *laneApi) ApplyAlarmContributorEdge(ctx context.Context, deviceId uint, alarmKey, _, _, _, severity string, _ *float64, _ time.Time) error {
	tenant, _ := core.TenantFromContext(ctx)
	id := fmt.Sprintf("%s/%d/%s", tenant, deviceId, alarmKey)
	a.mu.Lock()
	a.inflight[id]++
	if a.inflight[id] > 1 {
		a.overlap = true
	}
	a.cur++
	if a.cur > a.max {
		a.max = a.cur
	}
	a.mu.Unlock()

	time.Sleep(a.delay)

	a.mu.Lock()
	a.applied[id] = append(a.applied[id], severity)
	a.inflight[id]--
	a.cur--
	a.mu.Unlock()
	return nil
}

type lanesReader struct {
	msgs []messaging.Message
	next int
}

func (r *lanesReader) ReadMessage(context.Context) (messaging.Message, error) {
	if r.next >= len(r.msgs) {
		return messaging.Message{}, io.EOF
	}
	m := r.msgs[r.next]
	r.next++
	return m, nil
}
func (r *lanesReader) HandleResponse(error) {}

type laneAck struct{ n *atomic.Int64 }

func (a laneAck) Ack() error { a.n.Add(1); return nil }

// TestRaiseAlarmLanesKeepEachAlarmsOrder: with eight lanes the edges of different alarms overlap, while
// the edges of ONE alarm (same tenant, device and key) are applied one at a time, in the order they
// were read. The severity carries the sequence number so the order is observable, and the same
// device and key under another tenant is a different alarm.
func TestRaiseAlarmLanesKeepEachAlarmsOrder(t *testing.T) {
	const devices, keys, perAlarm = 12, 3, 5
	sevs := []string{"critical", "major", "minor", "warning", "indeterminate"}
	var acked atomic.Int64
	var msgs []messaging.Message
	want := map[string][]string{} // tenant/device/key -> severities in read order
	// Round-robin across alarms, so one alarm's edges are never adjacent in the stream.
	for seq := 0; seq < perAlarm; seq++ {
		for d := 0; d < devices; d++ {
			for k := 0; k < keys; k++ {
				for _, tenant := range []string{"acme", "globex"} {
					req := model.RaiseAlarmRequest{DeviceToken: fmt.Sprintf("dev-%d", d), AlarmKey: fmt.Sprintf("k%d", k),
						RuleID: "r", Edge: model.AlarmEdgeRaised, Severity: sevs[seq], OccurredTime: time.Now()}
					b, err := model.MarshalRaiseAlarmRequest(&req)
					require.NoError(t, err)
					msgs = append(msgs, messaging.NewConsumedMessage("dc."+tenant+".raise-alarm", b, 1, nil, laneAck{&acked}))
					id := fmt.Sprintf("%s/%d/k%d", tenant, d+1, k)
					want[id] = append(want[id], strings.ToUpper(sevs[seq]))
				}
			}
		}
	}
	api := &laneApi{applied: map[string][]string{}, inflight: map[string]int{}, delay: 2 * time.Millisecond}
	rc := &RaiseAlarmConsumer{Api: api, Reader: &lanesReader{msgs: msgs}, Workers: 8}
	rc.procCtx = context.Background()
	rc.readLoop(context.Background())

	require.EqualValues(t, len(msgs), acked.Load(), "every edge is acked exactly once")
	require.False(t, api.overlap, "two edges of one alarm were applied at the same time")
	require.Greater(t, api.max, 1, "edges of different alarms never overlapped: the lanes did nothing")
	require.Len(t, api.applied, len(want), "tenant, device and key together name an alarm")
	for id, seqs := range want {
		require.Equal(t, seqs, api.applied[id], "alarm %s was applied out of read order", id)
	}
}

// TestRaiseAlarmLaneIsStablePerAlarm: the lane depends on (subject, device, key) and nothing else.
func TestRaiseAlarmLaneIsStablePerAlarm(t *testing.T) {
	mk := func(subject, dev, key, sev string) messaging.Message {
		b, err := model.MarshalRaiseAlarmRequest(&model.RaiseAlarmRequest{DeviceToken: dev, AlarmKey: key,
			RuleID: "r", Edge: model.AlarmEdgeRaised, Severity: sev, OccurredTime: time.Now()})
		require.NoError(t, err)
		return messaging.NewConsumedMessage(subject, b, 1, nil, nil)
	}
	a := raiseLaneFor(mk("dc.acme.raise-alarm", "d1", "k", "major"), 8)
	require.Equal(t, a, raiseLaneFor(mk("dc.acme.raise-alarm", "d1", "k", "critical"), 8),
		"severity, rule and time must not move an alarm between lanes")
	spread := map[int]bool{}
	for i := 0; i < 64; i++ {
		spread[raiseLaneFor(mk("dc.acme.raise-alarm", fmt.Sprintf("d%d", i), "k", "major"), 8)] = true
	}
	require.Greater(t, len(spread), 4, "alarms should spread over the lanes")
	require.Equal(t, 0, raiseLaneFor(messaging.Message{Value: []byte("not json")}, 8), "poison goes to lane 0 to be dropped")
}

// TestRaiseAlarmLanesStopLeavesQueuedEdgesUnacked: cancelling the loop returns only once the lanes
// have drained, applies nothing afterwards, and leaves what it did not apply unacked.
func TestRaiseAlarmLanesStopLeavesQueuedEdgesUnacked(t *testing.T) {
	var acked atomic.Int64
	var msgs []messaging.Message
	for i := 0; i < 200; i++ {
		b, err := model.MarshalRaiseAlarmRequest(&model.RaiseAlarmRequest{DeviceToken: fmt.Sprintf("dev-%d", i), AlarmKey: "k",
			RuleID: "r", Edge: model.AlarmEdgeRaised, Severity: "major", OccurredTime: time.Now()})
		require.NoError(t, err)
		msgs = append(msgs, messaging.NewConsumedMessage("dc.acme.raise-alarm", b, 1, nil, laneAck{&acked}))
	}
	api := &laneApi{applied: map[string][]string{}, inflight: map[string]int{}, delay: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	rc := &RaiseAlarmConsumer{Api: api, Reader: &lanesReader{msgs: msgs}, Workers: 4}
	rc.procCtx = ctx
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	rc.readLoop(ctx)

	api.mu.Lock()
	applied := 0
	for _, v := range api.applied {
		applied += len(v)
	}
	api.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	api.mu.Lock()
	after := 0
	for _, v := range api.applied {
		after += len(v)
	}
	api.mu.Unlock()
	require.Equal(t, applied, after, "an edge was applied after the loop returned")
	require.Less(t, applied, len(msgs), "the loop should have stopped early")
	require.LessOrEqual(t, acked.Load(), int64(applied), "an edge that was not applied must not be acked")
}
