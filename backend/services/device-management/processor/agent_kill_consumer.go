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

	"github.com/devicechain-io/dc-device-management/model"
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
	Reader    messaging.MessageReader
	Api       KillEventAPI
	Feed      *model.KillFeed
	dead      *deadletter.Sink
	ms        *core.Microservice
	lifecycle core.LifecycleManager
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
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
	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		messaging.RunConsumer(c.ctx, c.Reader, core.NewReadPacer(c.ms, "agent-kills"), func(msg messaging.Message) bool {
			c.handle(c.ctx, msg)
			return true
		})
		// A stopped read loop must never leave the quiet-stream probe running.
		c.cancel()
	}()
	go func() {
		defer c.wg.Done()
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
	}()
	return nil
}

// Backlog is a broker round trip, safe alongside ReadMessage. Only a caught-up
// durable may beat the feed. An exhausted kill keeps the shared feed latched
// stale even once the broker stops counting it as unacknowledged.
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
		// Before the cap, leave storage failures unacked for redelivery. At the
		// cap JetStream retires the pending entry even without an ack, so an idle
		// poll could beat the feed despite this unapplied kill. Latch the shared
		// feed stale BEFORE dead-lettering; only process restart clears the latch.
		if messaging.FinalDelivery(msg) {
			c.Feed.LatchStale()
			log.Error().Err(err).Str("tenant", tenant).Msg("Agent kill was not applied on its final delivery; kill feed latched stale until process restart")
			if c.dead != nil {
				payload := msg.Value[:min(len(msg.Value), 4096)]
				if e := c.dead.WriteFor(msgctx, msg, deadletter.Envelope{
					Reason:     deadletter.ReasonUnprocessable,
					Summary:    "Agent kill was not applied; repeat the kill after storage recovers, then restart device-management",
					Detail:     err.Error(),
					Reference:  ev.EventID,
					OccurredAt: c.Feed.Now(),
					Payload:    payload,
				}); e != nil {
					log.Error().Err(e).Str("tenant", tenant).Msg("Failed to dead-letter unapplied agent kill; kill feed remains latched stale")
					return
				}
			}
			_ = msg.Ack()
			return
		}
		log.Warn().Err(err).Str("tenant", tenant).Msg("Agent kill was not applied; leaving it unacknowledged")
		return
	}
	// No Beat here: only a caught-up poll can vouch for the feed, and a latched
	// feed stays stale. One clean kill must not mask another tenant's failed kill.
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
