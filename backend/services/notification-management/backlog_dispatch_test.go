// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-notification-management/processor"
)

// slowCountingNotifier stands in for a channel that takes a while to answer — an SMTP relay,
// a webhook behind a slow gateway — and counts how many times each alarm was handed to it.
type slowCountingNotifier struct {
	delay time.Duration

	mu     sync.Mutex
	counts map[string]int
	total  int
}

func (n *slowCountingNotifier) Notify(_ context.Context, event *dmmodel.AlarmStateChangeEvent) error {
	time.Sleep(n.delay)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.counts[event.AlarmToken]++
	n.total++
	return nil
}

func (n *slowCountingNotifier) snapshot() (map[string]int, int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make(map[string]int, len(n.counts))
	for k, v := range n.counts {
		out[k] = v
	}
	return out, n.total
}

// A burst of alarms queued behind slow channels must page each alarm ONCE.
//
// 🔴 THE DEFECT IS THE READER FETCHING AHEAD OF ITS WORKERS. The broker starts a message's
// redelivery clock (AckWait) when it hands the message out, not when a worker picks it up.
// A reader that fetched a 64-message batch into a 100-deep hand-off channel in front of five
// workers held most of that burst in process for longer than the clock: every alarm past
// roughly (workers × AckWait ÷ send time) was redelivered while its first copy was still
// queued, and BOTH copies were dispatched — two pages for one alarm, reproduced here.
//
// Everything is the service's own: the reader comes from newAlarmEventsReader (the function
// main wires), the processor is the real NotificationProcessor with its real pool, and the
// broker is a real embedded JetStream, because the clock that expires is the broker's. The
// only substitutions are the AckWait (3s rather than 60s, through the manager's single seam,
// so the durable and the reader agree on it) and a Notifier that takes 400ms and counts.
//
// The assertion is on VALUES: every token was notified exactly once, and the total is the
// number published — not merely "no duplicate was seen", which an alarm never delivered at
// all would also satisfy.
func TestAlarmBacklogIsDispatchedOnce(t *testing.T) {
	testCtx, cancelTest := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancelTest()

	const alarms = 100
	host, port := startEmbeddedNats(t)

	area := fmt.Sprintf("notification-backlog-%d", time.Now().UnixNano())
	ms := &core.Microservice{InstanceId: area, FunctionalArea: "notification-management"}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}

	var reader messaging.MessageReader
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(m *messaging.NatsManager) error {
		r, err := newAlarmEventsReader(m)
		reader = r
		return err
	})
	nmgr.SetAckWaitForTesting(t, 3*time.Second)
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	require.NotNil(t, reader, "the manager's start did not build the alarm-events reader")

	notifier := &slowCountingNotifier{delay: 400 * time.Millisecond, counts: map[string]int{}}
	np := processor.NewNotificationProcessor(ms, reader, core.NewNoOpLifecycleCallbacks(), notifier, nil,
		processor.NewNotifyMetrics(ms))
	require.NoError(t, np.Initialize(context.Background()))
	require.NoError(t, np.Start(context.Background()))
	t.Cleanup(func() { _ = np.Stop(context.Background()) })

	// Published AFTER the durable exists: it is DeliverNew, so anything earlier is never read.
	writer, err := nmgr.NewWriter(streams.AlarmEvents)
	require.NoError(t, err)
	tenantCtx := core.WithTenant(context.Background(), "acme")
	for i := 0; i < alarms; i++ {
		body, err := dmproto.MarshalAlarmStateChangeEvent(&dmmodel.AlarmStateChangeEvent{
			EventType: dmmodel.AlarmEventRaised, AlarmToken: fmt.Sprintf("alarm-%03d", i),
			State: "ACTIVE", Severity: "CRITICAL", OccurredTime: time.Now(), RaisedTime: time.Now(),
		})
		require.NoError(t, err)
		require.NoError(t, writer.WriteMessages(tenantCtx, messaging.Message{Value: body}))
	}

	// Wait until every alarm has been paged at least once.
	for {
		counts, _ := notifier.snapshot()
		if len(counts) == alarms {
			break
		}
		select {
		case <-testCtx.Done():
			t.Fatalf("only %d of %d alarms were notified before the test deadline", len(counts), alarms)
		case <-time.After(50 * time.Millisecond):
		}
	}
	// Then outlast one more AckWait, so a redelivery of anything still held has time to be
	// dispatched and counted rather than escaping the assertion by arriving late.
	select {
	case <-testCtx.Done():
		t.Fatal("the test deadline passed while waiting out the settle window")
	case <-time.After(4 * time.Second):
	}

	counts, total := notifier.snapshot()
	var twice []string
	for token, n := range counts {
		if n != 1 {
			twice = append(twice, fmt.Sprintf("%s×%d", token, n))
		}
	}
	require.Empty(t, twice, "these alarms were paged more than once: the reader held them in process "+
		"past AckWait, the broker redelivered them, and both copies were dispatched")
	require.Equal(t, alarms, total, "total notifications")
}
