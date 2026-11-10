// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

type killAck struct {
	calls int
	err   error
}

func (a *killAck) Ack() error { a.calls++; return a.err }

type killAPI struct {
	calls  int
	tenant string
	err    error
}

func (a *killAPI) ApplyKillEvent(ctx context.Context, ev actiongate.KillEvent) error {
	a.calls++
	a.tenant, _ = core.TenantFromContext(ctx)
	return a.err
}

func TestAgentKillConsumerValidatesAndSettles(t *testing.T) {
	for _, mode := range []string{"valid", "malformed", "mismatch", "subject", "store-failure", "ack-failure"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
			feed := model.NewKillFeed(0, func() time.Time { return now })
			api := &killAPI{}
			ack := &killAck{}
			subject := messaging.ScopedSubject("test", "acme", streams.AgentActuationKills)
			body, err := actiongate.EncodeKillEvent(actiongate.KillEvent{EventID: "one", Tenant: "acme", PolicyVersion: 4, Cause: actiongate.KillCauseTenantDisabled, At: now})
			require.NoError(t, err)
			switch mode {
			case "malformed":
				body = []byte(`{"tenant":"acme"}`)
			case "mismatch":
				subject = messaging.ScopedSubject("test", "other", streams.AgentActuationKills)
			case "subject":
				subject = "test.acme.other"
			case "store-failure":
				api.err = errors.New("database away")
			case "ack-failure":
				ack.err = errors.New("ack failed")
			}
			consumer := NewAgentKillConsumer(&core.Microservice{FunctionalArea: "device-management"}, nil, api, feed, nil)
			consumer.handle(context.Background(), messaging.NewConsumedMessage(subject, body, 1, nil, ack))
			switch mode {
			case "malformed", "mismatch", "subject":
				require.Zero(t, api.calls)
				require.Equal(t, 1, ack.calls)
			case "store-failure":
				require.Equal(t, 1, api.calls)
				require.Zero(t, ack.calls)
				require.False(t, feed.Fresh(now))
			case "ack-failure":
				require.Equal(t, 1, api.calls)
				require.Equal(t, 1, ack.calls)
				require.False(t, feed.Fresh(now))
			default:
				require.Equal(t, 1, api.calls)
				require.Equal(t, "acme", api.tenant)
				require.Equal(t, 1, ack.calls)
				// Handling a message never vouches for the feed: only an idle poll does, so one tenant's
				// applied kill cannot mask another tenant's kill that is still failing.
				require.False(t, feed.Fresh(now))
			}
		})
	}
}

type killPollReader struct {
	messaging.MessageReader
	pending, unacked uint64
	err              error
}

func (r *killPollReader) Backlog(context.Context) (uint64, uint64, error) {
	return r.pending, r.unacked, r.err
}

func TestAgentKillConsumerIdlePollFreshness(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	feed := model.NewKillFeed(time.Second, func() time.Time { return now })
	r := &killPollReader{}
	c := &AgentKillConsumer{Reader: r, Feed: feed}
	c.poll(context.Background())
	require.True(t, feed.Fresh(now))
	for _, mode := range []string{"pending", "unacked", "failure"} {
		now = now.Add(2 * time.Second)
		r.pending, r.unacked, r.err = 0, 0, nil
		switch mode {
		case "pending":
			r.pending = 1
		case "unacked":
			r.unacked = 1
		case "failure":
			r.err = errors.New("broker away")
		}
		c.poll(context.Background())
		require.False(t, feed.Fresh(now), mode)
	}
	r.pending, r.unacked, r.err = 0, 0, nil
	c.poll(context.Background())
	require.True(t, feed.Fresh(now))
}

type dedupAlarmWriter struct {
	alarmEventRecorder
	messages []messaging.Message
	err      error
}

func (w *dedupAlarmWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.messages = append(w.messages, msgs...)
	return w.err
}

func TestAlarmEventDedupReturnsBrokerAckError(t *testing.T) {
	w := &dedupAlarmWriter{err: errors.New("broker ack lost")}
	p := NewAlarmEventWriter(w, nil, AlarmEventMetrics{})
	event := &model.AlarmStateChangeEvent{AlarmToken: "alarm", EventType: model.AlarmEventAcknowledged}
	err := p.PublishAlarmEventDedup(context.Background(), event, "0123456789abcdef0123456789abcdef")
	require.ErrorIs(t, err, w.err)
	w.err = nil
	require.NoError(t, p.PublishAlarmEventDedup(context.Background(), event, "0123456789abcdef0123456789abcdef"))
	require.Len(t, w.messages, 2)
	require.Equal(t, "0123456789abcdef0123456789abcdef", w.messages[0].DedupID)
	require.Equal(t, w.messages[0].DedupID, w.messages[1].DedupID)
}
