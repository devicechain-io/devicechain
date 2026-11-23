// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/rs/zerolog/log"
)

type KillEventAPI interface {
	ApplyKillEvent(context.Context, actiongate.KillEvent) error
}

type AgentKillConsumer struct {
	Reader messaging.MessageReader
	// BindReader is optional for an already-built reader. Production binds here so
	// an old broker auth callout cannot make this release fail service startup.
	BindReader func() (messaging.MessageReader, error)
	Api        KillEventAPI
	Feed       *model.KillFeed
	dead       *deadletter.Sink
	ms         *core.Microservice
	lifecycle  core.LifecycleManager
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

func NewAgentKillConsumer(ms *core.Microservice, reader messaging.MessageReader, api KillEventAPI, feed *model.KillFeed, dead *deadletter.Sink) *AgentKillConsumer {
	c := &AgentKillConsumer{Reader: reader, Api: api, Feed: feed, dead: dead, ms: ms}
	c.lifecycle = core.NewLifecycleManager(ms.FunctionalArea+"-agent-kills", c, core.NewNoOpLifecycleCallbacks())
	return c
}

func (c *AgentKillConsumer) Initialize(ctx context.Context) error { return c.lifecycle.Initialize(ctx) }
func (c *AgentKillConsumer) Start(ctx context.Context) error      { return c.lifecycle.Start(ctx) }
func (c *AgentKillConsumer) Stop(ctx context.Context) error       { return c.lifecycle.Stop(ctx) }
func (c *AgentKillConsumer) Terminate(ctx context.Context) error  { return c.lifecycle.Terminate(ctx) }
func (c *AgentKillConsumer) ExecuteInitialize(ctx context.Context) error {
	c.ctx, c.cancel = context.WithCancel(ctx)
	return nil
}
func (c *AgentKillConsumer) ExecuteStart(context.Context) error {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		if !c.bindReader() {
			return
		}
		var poller sync.WaitGroup
		poller.Add(1)
		go func() {
			defer poller.Done()
			c.pollLoop()
		}()
		messaging.RunConsumer(c.ctx, c.Reader, core.NewReadPacer(c.ms, "agent-kills"), func(msg messaging.Message) bool {
			c.handle(c.ctx, msg)
			return true
		})
		// A stopped read loop must never leave the quiet-stream probe running.
		c.cancel()
		poller.Wait()
	}()
	return nil
}

func (c *AgentKillConsumer) bindReader() bool {
	backoff := time.Second
	for c.Reader == nil {
		if c.ctx.Err() != nil {
			return false
		}
		var err error
		if c.BindReader != nil {
			var reader messaging.MessageReader
			reader, err = c.BindReader()
			if err == nil {
				c.Reader = reader
			}
		}
		if err == nil && c.Reader != nil {
			return true
		}
		// No heartbeat before a successful bind and caught-up backlog probe.
		log.Warn().Err(err).Msg("Agent kill subscription unavailable; retrying with execution fenced")
		timer := time.NewTimer(backoff)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
		backoff = min(2*backoff, time.Minute)
	}
	return true
}

func (c *AgentKillConsumer) pollLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		c.poll(c.ctx)
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Backlog is a broker round trip, safe alongside ReadMessage. Only a caught-up
// durable proves an idle stream healthy: an unhandled or failed kill stops the beat while
// it is redelivered. It does NOT stop it forever: after the final delivery the broker moves
// the ack floor past it, which is why handle latches the feed then (KillFeed.Latch).
func (c *AgentKillConsumer) poll(ctx context.Context) {
	r, ok := c.Reader.(interface {
		Backlog(context.Context) (uint64, uint64, error)
	})
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	pending, unacked, err := r.Backlog(ctx)
	if err == nil && pending == 0 && unacked == 0 {
		c.Feed.Beat(c.Feed.Now())
	}
}

func (c *AgentKillConsumer) handle(ctx context.Context, msg messaging.Message) {
	msgctx, tenant, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	ev, err := actiongate.DecodeKillEvent(msg.Value)
	parts := strings.Split(msg.Subject, ".")
	if !ok || len(parts) != 3 || parts[2] != streams.AgentActuationKills || (err == nil && tenant != ev.Tenant) {
		err = fmt.Errorf("kill event subject/tenant mismatch")
	}
	if err != nil {
		log.Warn().Err(err).Str("subject", msg.Subject).Msg("Malformed agent kill event")
		if c.dead != nil {
			payload := msg.Value[:min(len(msg.Value), 4096)]
			if e := c.dead.WriteFor(msgctx, msg, deadletter.Envelope{Reason: deadletter.ReasonUnprocessable,
				Summary: "malformed agent kill", Detail: err.Error(), OccurredAt: c.Feed.Now(), Payload: payload}); e != nil {
				return
			}
		}
		_ = msg.Ack()
		return
	}
	if err := c.Api.ApplyKillEvent(msgctx, ev); err != nil {
		if errors.Is(err, rdb.ErrTenantPurged) {
			// Erasure already reclaimed this tenant, so there is no fence to close.
			// Keeping its kill unacked would stop the caught-up beat and fail every
			// other tenant's executions closed.
			log.Info().Err(err).Str("tenant", tenant).Msg("Acknowledging agent kill for a purged tenant")
			_ = msg.Ack()
			return
		}
		if !messaging.FinalDelivery(msg) {
			// Left unacknowledged, it is redelivered; until then the durable has an
			// unacknowledged message, so the poll cannot vouch for the feed.
			log.Warn().Err(err).Str("tenant", tenant).Msg("Agent kill was not applied; leaving it unacknowledged for redelivery")
			return
		}
		// 🔴 THE FINAL DELIVERY. The stream caps deliveries (MaxDeliver) and the broker then moves
		// the ack floor past this kill whether or not it is acknowledged, so "never acknowledge a
		// storage failure" would NOT keep it: the durable would read caught up and the feed fresh,
		// with this tenant's fence never closed. Instead: latch the feed stale for the life of the
		// process (execution and agent-command dispatch stay refused) and dead-letter the kill so
		// an operator repeats it.
		c.Feed.Latch()
		log.Error().Err(err).Str("tenant", tenant).Msg("Agent kill was not applied by its final delivery; agent execution and agent-command dispatch are fenced in this process until restart; repeat the kill")
		if c.dead != nil {
			payload := msg.Value[:min(len(msg.Value), 4096)]
			if e := c.dead.WriteFor(msgctx, msg, deadletter.Envelope{Reason: deadletter.ReasonUnprocessable,
				Summary: "agent kill not applied by its final delivery; repeat the kill", Detail: err.Error(), OccurredAt: c.Feed.Now(), Payload: payload}); e != nil {
				return
			}
		}
		_ = msg.Ack()
		return
	}
	// No Beat here. Freshness means "the durable has nothing pending or unacknowledged" (poll), so a
	// message that applied cleanly must not vouch for another tenant's kill that is still failing.
	_ = msg.Ack()
}

func (c *AgentKillConsumer) ExecuteStop(context.Context) error {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	return nil
}
func (c *AgentKillConsumer) ExecuteTerminate(context.Context) error { return nil }
