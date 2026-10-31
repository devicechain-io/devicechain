// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync"
	"time"

	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/rs/zerolog/log"
)

// RosterApi is the slice of the update-management API the roster consumer uses.
type RosterApi interface {
	DeviceRosterChanged(ctx context.Context, deviceToken string, enabled bool) error
}

// RosterConsumer reads device-management's device-roster facts, which it emits when a device is
// created, re-typed, disabled or enabled. update-management uses them for one thing: to learn at
// once that a device was disabled or enabled (it drops its cached answer) and, on an enable, to
// re-offer the device its current assignment (model.Api.DeviceRosterChanged).
//
// The fact is a prompt, never the answer: nothing it says is trusted over a fresh lookup, which
// is why a duplicated, reordered or lost fact is harmless (the cache TTL and the device's own
// sync are the backstops). It is acked once handled; a transient failure is left for redelivery
// and lettered at the redelivery cap like every consumer here.
type RosterConsumer struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Api          RosterApi

	dead *deadletter.Sink

	// Hold keeps a roster fact unacked, outside its delivery budget, while the relational store
	// cannot take writes (core/storehold). Re-running DeviceRosterChanged is safe: it re-evaluates
	// the device's assignments against the fact's enabled flag. Nil holds nothing.
	Hold *storehold.Holder

	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	readPacer  *core.ReadPacer

	lifecycle core.LifecycleManager
}

// NewRosterConsumer creates the consumer over its dedicated reader.
func NewRosterConsumer(ms *core.Microservice, reader messaging.MessageReader, callbacks core.LifecycleCallbacks,
	api RosterApi, dead *deadletter.Sink) *RosterConsumer {
	rc := &RosterConsumer{Microservice: ms, Reader: reader, Api: api, dead: dead}
	name := fmt.Sprintf("%s-%s", ms.FunctionalArea, "device-roster-proc")
	rc.lifecycle = core.NewLifecycleManager(name, rc, callbacks)
	return rc
}

func (rc *RosterConsumer) pacer() *core.ReadPacer {
	if rc.readPacer == nil {
		rc.readPacer = core.NewReadPacer(rc.Microservice, "device-roster")
	}
	return rc.readPacer
}

// Initialize component.
func (rc *RosterConsumer) Initialize(ctx context.Context) error { return rc.lifecycle.Initialize(ctx) }

// ExecuteInitialize builds the cancelable read context.
func (rc *RosterConsumer) ExecuteInitialize(ctx context.Context) error {
	rc.procCtx, rc.procCancel = context.WithCancel(ctx)
	return nil
}

// Start component.
func (rc *RosterConsumer) Start(ctx context.Context) error { return rc.lifecycle.Start(ctx) }

// ExecuteStart launches the read loop.
func (rc *RosterConsumer) ExecuteStart(ctx context.Context) error {
	rc.readerWG.Add(1)
	go func() {
		defer rc.readerWG.Done()
		messaging.RunConsumer(rc.procCtx, rc.Reader, rc.pacer(), func(msg messaging.Message) bool {
			rc.handle(rc.procCtx, msg)
			return true
		})
	}()
	return nil
}

// handle applies one roster fact and acks it or leaves it unacked for redelivery.
func (rc *RosterConsumer) handle(ctx context.Context, msg messaging.Message) {
	msgctx, _, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Str("subject", msg.Subject).Msg("Device-roster fact dropped: the subject names no tenant.")
		msg.Ack()
		return
	}
	ev, err := dmproto.UnmarshalDeviceRosterEvent(msg.Value)
	if err != nil || core.ValidateToken(ev.DeviceToken) != nil {
		log.Warn().Err(err).Str("subject", msg.Subject).Msg("Device-roster fact dropped: it does not decode to a device.")
		msg.Ack()
		return
	}
	ctx2 := rc.procCtx
	if ctx2 == nil {
		ctx2 = ctx
	}
	if err := rc.Hold.Run(ctx2, []messaging.Message{msg}, func() error {
		return rc.Api.DeviceRosterChanged(msgctx, ev.DeviceToken, ev.Enabled)
	}); err != nil {
		rc.retryOrDrop(msgctx, msg, ev.DeviceToken, err)
		return
	}
	msg.Ack()
}

// retryOrDrop leaves a transiently failed fact unacked for redelivery, or dead-letters and acks it
// once the redelivery cap is reached. A deleted tenant is settled where it stands (messaging.Dispose).
func (rc *RosterConsumer) retryOrDrop(ctx context.Context, msg messaging.Message, token string, err error) {
	switch messaging.Dispose(msg, err) {
	case messaging.TenantErased:
		msg.Ack()
		return
	case messaging.Exhausted:
		log.Error().Err(err).Str("device", token).Int("attempts", msg.NumDelivered).
			Msg("Device-roster fact failed past the redelivery cap; dead-lettering.")
		if rc.dead != nil {
			payload := msg.Value
			if len(payload) > maxDeadLetterPayload {
				payload = payload[:maxDeadLetterPayload]
			}
			if derr := rc.dead.WriteFor(ctx, msg, deadletter.Envelope{
				Reason:     deadletter.ReasonExhausted,
				Summary:    "a device-roster fact could not be applied after every delivery attempt",
				Detail:     err.Error(),
				Reference:  token,
				OccurredAt: time.Now().UTC(),
				Payload:    payload,
			}); derr != nil {
				log.Error().Err(derr).Str("device", token).Msg("LOST device-roster fact: it could be neither applied nor dead-lettered.")
			}
		}
		msg.Ack()
		return
	}
	log.Warn().Err(err).Str("device", token).Msg("Device-roster fact failed; will retry on redelivery.")
}

// Stop component.
func (rc *RosterConsumer) Stop(ctx context.Context) error { return rc.lifecycle.Stop(ctx) }

// ExecuteStop cancels the read loop and waits for it to exit before the reader is torn down.
func (rc *RosterConsumer) ExecuteStop(context.Context) error {
	if rc.procCancel != nil {
		rc.procCancel()
	}
	rc.readerWG.Wait()
	return nil
}

// Terminate component.
func (rc *RosterConsumer) Terminate(ctx context.Context) error { return rc.lifecycle.Terminate(ctx) }

// ExecuteTerminate is a no-op; the reader is owned by the NATS manager.
func (rc *RosterConsumer) ExecuteTerminate(context.Context) error { return nil }
