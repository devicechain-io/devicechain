// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
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
// durable proves an idle stream healthy: unhandled or failed kills stop the beat.
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
		// A storage failure is never acknowledged, even after the redelivery cap.
		log.Warn().Err(err).Str("tenant", tenant).Msg("Agent kill was not applied; leaving it unacknowledged")
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
