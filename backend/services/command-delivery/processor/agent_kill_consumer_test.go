// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
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
			body, err := actiongate.EncodeKillEvent(actiongate.KillEvent{EventID: "one", Tenant: "acme", ActuationGeneration: 4, Cause: actiongate.KillCauseTenantDisabled, At: now})
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

// An erased tenant cannot close a fence; ACK its kill so it cannot stall other tenants' feed.
// Ordinary storage failures remain unacknowledged, including wrapped errors.
func TestKillForAPurgedTenantIsAcknowledged(t *testing.T) {
	for _, purged := range []bool{true, false} {
		t.Run(map[bool]string{true: "purged", false: "storage failure"}[purged], func(t *testing.T) {
			now := time.Now()
			storageErr := fmt.Errorf("apply kill: %w", errors.New("database unavailable"))
			if purged {
				storageErr = fmt.Errorf("apply kill: %w", rdb.ErrTenantPurged)
			}
			api := &killAPI{err: storageErr}
			ack := &killAck{}
			feed := model.NewKillFeed(0, func() time.Time { return now })
			body, err := actiongate.EncodeKillEvent(actiongate.KillEvent{
				EventID: "purged", Tenant: "acme", Cause: actiongate.KillCauseOperatorSuspended, At: now,
			})
			require.NoError(t, err)
			c := &AgentKillConsumer{Api: api, Feed: feed}
			c.handle(context.Background(), messaging.NewConsumedMessage(
				messaging.ScopedSubject("test", "acme", streams.AgentActuationKills), body, 1, nil, ack))
			require.Equal(t, 1, api.calls)
			if purged {
				require.Equal(t, 1, ack.calls)
			} else {
				require.Zero(t, ack.calls)
			}
			require.False(t, feed.Fresh(now), "only a caught-up poll may beat the feed")
		})
	}
}

type retryKillReader struct{}

func (retryKillReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	<-ctx.Done()
	return messaging.Message{}, ctx.Err()
}
func (retryKillReader) HandleResponse(error)                            {}
func (retryKillReader) Backlog(context.Context) (uint64, uint64, error) { return 0, 0, nil }

func TestAgentKillBindFailureDoesNotFailStartupAndRecovers(t *testing.T) {
	feed := model.NewKillFeed(0, nil)
	c := NewAgentKillConsumer(&core.Microservice{FunctionalArea: "command-delivery"}, nil, &killAPI{}, feed, nil)
	var attempts atomic.Int32
	var allow atomic.Bool
	c.BindReader = func() (messaging.MessageReader, error) {
		attempts.Add(1)
		if !allow.Load() {
			return nil, errors.New("old auth callout refused subscription")
		}
		return retryKillReader{}, nil
	}
	require.NoError(t, c.Initialize(context.Background()))
	require.NoError(t, c.Start(context.Background()), "a bind refusal must leave the service up")
	t.Cleanup(func() { require.NoError(t, c.Stop(context.Background())) })
	require.Eventually(t, func() bool { return attempts.Load() > 0 }, time.Second, 10*time.Millisecond)
	require.False(t, feed.Fresh(feed.Now()), "execution stays FENCE_UNAVAILABLE until bind and caught-up probe")
	allow.Store(true)
	require.Eventually(t, func() bool { return feed.Fresh(feed.Now()) }, 4*time.Second, 10*time.Millisecond)
	require.GreaterOrEqual(t, attempts.Load(), int32(2))
}

type recordingDeadWriter struct{ msgs []messaging.Message }

func (w *recordingDeadWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.msgs = append(w.msgs, msgs...)
	return nil
}

// 🔴 A KILL THAT FAILS PAST THE DELIVERY CAP. The broker moves the ack floor past a message after
// MaxDeliver deliveries whether or not it was acknowledged, so the durable then reads caught up.
// Before that last delivery the kill stays unacknowledged; AT it, the consumer latches the feed
// stale for the process and dead-letters the kill, and from then on no agent command of any
// tenant is claimed for dispatch, even after an idle poll beats. Ordinary commands still go.
func TestAgentKillPastTheDeliveryCapFencesAgentDispatch(t *testing.T) {
	api := realApi(t)
	require.NoError(t, api.RDB.Database.AutoMigrate(&model.AgentActuationFence{}))
	ctx := core.WithTenant(context.Background(), "acme")
	feed := model.NewKillFeed(0, nil)
	feed.Beat(time.Now())
	api.KillFeed = feed
	require.NoError(t, api.RDB.DB(ctx).Create(&model.AgentActuationFence{Enabled: true, Epoch: 1, UpdatedAt: time.Now().UTC()}).Error)
	mk := func(token string, agent bool) *model.Command {
		c := &model.Command{TokenReference: rdb.TokenReference{Token: token}, DeviceToken: "dev-1", Name: "reboot", Status: model.CommandQueued.String(), QueuedTime: time.Now().UTC()}
		if agent {
			c.ProposalToken = sql.NullString{String: "proposal-" + token, Valid: true}
			c.CauseKind = sql.NullString{String: model.CauseKindProposal, Valid: true}
		}
		require.NoError(t, api.RDB.DB(ctx).Create(c).Error)
		return c
	}
	agentCmd, ordinary := mk("agent-cmd", true), mk("ordinary-cmd", false)

	dead := &recordingDeadWriter{}
	failing := &killAPI{err: errors.New("database away")}
	consumer := NewAgentKillConsumer(&core.Microservice{FunctionalArea: "command-delivery"}, nil, failing, feed, testDeadSink(dead))
	subject := messaging.ScopedSubject("test", "acme", streams.AgentActuationKills)
	body, err := actiongate.EncodeKillEvent(actiongate.KillEvent{EventID: "k", Tenant: "acme", ActuationGeneration: 4, Cause: actiongate.KillCauseTenantDisabled, At: time.Now().UTC()})
	require.NoError(t, err)

	early := &killAck{}
	consumer.handle(context.Background(), messaging.NewConsumedMessage(subject, body, messaging.MaxDeliver-1, nil, early))
	require.Zero(t, early.calls, "before the cap the kill stays unacknowledged for redelivery")
	require.False(t, feed.Latched())
	require.Empty(t, dead.msgs)

	final := &killAck{}
	consumer.handle(context.Background(), messaging.NewConsumedMessage(subject, body, messaging.MaxDeliver, nil, final).
		WithOrigin(messaging.Origin{Suffix: streams.AgentActuationKills, Stream: "s", Consumer: "c", Seq: 7}))
	require.True(t, feed.Latched(), "the final failed delivery latches the feed")
	require.Len(t, dead.msgs, 1, "the lost kill is dead-lettered for an operator to repeat")
	require.Equal(t, 1, final.calls)

	feed.Beat(time.Now()) // the broker now reads caught up; an idle poll must not vouch again
	require.False(t, feed.Fresh(time.Now()))
	_, won, err := api.MarkSent(ctx, agentCmd.ID)
	require.NoError(t, err)
	require.False(t, won, "no agent command dispatches after a lost kill")
	_, won, err = api.MarkSent(ctx, ordinary.ID)
	require.NoError(t, err)
	require.True(t, won, "ordinary commands are unaffected")
}
