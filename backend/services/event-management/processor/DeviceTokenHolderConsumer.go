// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync"

	dmproto "github.com/devicechain-io/dc-device-management/proto"
	emmodel "github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// DeviceTokenHolderConsumer reads device-management's device-roster facts, which it publishes
// when a device is created, re-typed, disabled or enabled, and records from each that a live
// device holds the fact's token since its ExpectedSince (model.DeviceTokenHolder). That is the
// prompt half of telling a re-created token from a deleted one: a read by a token deleted and
// created again is bounded to the new device, a read by a token only deleted is not.
//
// Every write is forward-only (HeldSince = GREATEST(stored, ExpectedSince)), so a duplicated,
// reordered or replayed fact is harmless, including the first-enablement replay of the retained
// stream. A fact device-management failed to publish is repaired by the anchor sweep, which asks
// device-management directly (AnchorReconciliationSweep.repairHolders).
//
// It settles a message the way EntityAnchorReconciler does: poison is acked and dropped, a
// transient database error is left unacked for AckWait-paced redelivery, and a delivery past the
// redelivery cap is acked with an error log (the sweep still covers the token).
type DeviceTokenHolderConsumer struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Api          emmodel.EventManagementApi

	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	readPacer  *core.ReadPacer

	lifecycle core.LifecycleManager
}

// NewDeviceTokenHolderConsumer builds the consumer over the device-roster reader.
func NewDeviceTokenHolderConsumer(ms *core.Microservice, reader messaging.MessageReader,
	api emmodel.EventManagementApi, callbacks core.LifecycleCallbacks) *DeviceTokenHolderConsumer {
	c := &DeviceTokenHolderConsumer{Microservice: ms, Reader: reader, Api: api}
	c.lifecycle = core.NewLifecycleManager(fmt.Sprintf("%s-%s", ms.FunctionalArea, "device-token-holders"), c, callbacks)
	return c
}

func (c *DeviceTokenHolderConsumer) pacer() *core.ReadPacer {
	if c.readPacer == nil {
		c.readPacer = core.NewReadPacer(c.Microservice, "device-roster")
	}
	return c.readPacer
}

func (c *DeviceTokenHolderConsumer) Initialize(ctx context.Context) error {
	return c.lifecycle.Initialize(ctx)
}

func (c *DeviceTokenHolderConsumer) ExecuteInitialize(ctx context.Context) error {
	c.procCtx, c.procCancel = context.WithCancel(ctx)
	return nil
}

func (c *DeviceTokenHolderConsumer) Start(ctx context.Context) error { return c.lifecycle.Start(ctx) }

func (c *DeviceTokenHolderConsumer) ExecuteStart(context.Context) error {
	c.readerWG.Add(1)
	go func() {
		defer c.readerWG.Done()
		messaging.RunConsumer(c.procCtx, c.Reader, c.pacer(), func(msg messaging.Message) bool {
			c.handle(c.procCtx, msg)
			return true
		})
	}()
	return nil
}

func (c *DeviceTokenHolderConsumer) Stop(ctx context.Context) error { return c.lifecycle.Stop(ctx) }

func (c *DeviceTokenHolderConsumer) ExecuteStop(context.Context) error {
	if c.procCancel != nil {
		c.procCancel()
	}
	c.readerWG.Wait()
	return nil
}

func (c *DeviceTokenHolderConsumer) Terminate(ctx context.Context) error {
	return c.lifecycle.Terminate(ctx)
}

func (c *DeviceTokenHolderConsumer) ExecuteTerminate(context.Context) error { return nil }

// handle records one roster fact's holder and acks it, or leaves it for redelivery.
func (c *DeviceTokenHolderConsumer) handle(ctx context.Context, msg messaging.Message) {
	tctx, _, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Str("subject", msg.Subject).Msg("Dropping device-roster fact with no parseable tenant")
		_ = msg.Ack()
		return
	}
	ev, err := dmproto.UnmarshalDeviceRosterEvent(msg.Value)
	if err != nil || core.ValidateToken(ev.DeviceToken) != nil || ev.ExpectedSince.IsZero() {
		// A fact with no instant says nothing about WHEN the token was held, and recording it
		// at the zero time would be a no-op anyway; the sweep covers the token.
		log.Warn().Err(err).Str("subject", msg.Subject).Msg("Dropping a device-roster fact that names no device and instant")
		_ = msg.Ack()
		return
	}
	if err := c.Api.RecordDeviceTokenHeld(tctx, ev.DeviceToken, ev.ExpectedSince); err != nil {
		switch messaging.Dispose(msg, err) {
		case messaging.TenantErased:
			log.Debug().Err(err).Msg("Dropping device-roster fact for an erased tenant")
			_ = msg.Ack()
			return
		case messaging.Exhausted:
			log.Error().Err(err).Str("device", ev.DeviceToken).Int("delivered", msg.NumDelivered).
				Msg("Giving up on a device-roster fact after max delivery attempts; the anchor sweep repairs its token")
			_ = msg.Ack()
			return
		}
		// Transient: leave it UNACKED so AckWait paces redelivery (see EntityAnchorReconciler).
		log.Warn().Err(err).Str("device", ev.DeviceToken).Msg("Recording a device-token holder failed; will retry on redelivery")
		return
	}
	_ = msg.Ack()
}
