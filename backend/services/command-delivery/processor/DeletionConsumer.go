// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/rs/zerolog/log"
)

// DeletionApi is the write needed to apply an entity-deleted fact.
type DeletionApi interface {
	CancelDeletedDeviceCommands(ctx context.Context, deviceToken string, deletedTime time.Time) (int64, error)
}

// DeletionConsumer applies retained deletion facts before acknowledging them.
type DeletionConsumer struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Api          DeletionApi

	dead *deadletter.Sink

	// Hold keeps the fact unacked while the store cannot take writes. Nil holds nothing.
	// Recording the floor and cancelling covered commands are idempotent.
	Hold *storehold.Holder

	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	readPacer  *core.ReadPacer

	lifecycle core.LifecycleManager
}

// NewDeletionConsumer creates the consumer over its dedicated reader.
func NewDeletionConsumer(ms *core.Microservice, reader messaging.MessageReader, callbacks core.LifecycleCallbacks,
	api DeletionApi, dead *deadletter.Sink) *DeletionConsumer {
	rc := &DeletionConsumer{Microservice: ms, Reader: reader, Api: api, dead: dead}
	name := fmt.Sprintf("%s-%s", ms.FunctionalArea, "entity-deleted-proc")
	rc.lifecycle = core.NewLifecycleManager(name, rc, callbacks)
	return rc
}

func (rc *DeletionConsumer) pacer() *core.ReadPacer {
	if rc.readPacer == nil {
		rc.readPacer = core.NewReadPacer(rc.Microservice, "entity-deleted")
	}
	return rc.readPacer
}

// Initialize component.
func (rc *DeletionConsumer) Initialize(ctx context.Context) error {
	return rc.lifecycle.Initialize(ctx)
}

// ExecuteInitialize builds the cancelable read context.
func (rc *DeletionConsumer) ExecuteInitialize(ctx context.Context) error {
	rc.procCtx, rc.procCancel = context.WithCancel(ctx)
	return nil
}

// Start component.
func (rc *DeletionConsumer) Start(ctx context.Context) error { return rc.lifecycle.Start(ctx) }

// ExecuteStart launches the read loop.
func (rc *DeletionConsumer) ExecuteStart(ctx context.Context) error {
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

// handle applies one deletion fact and acks it or leaves it unacked for redelivery.
func (rc *DeletionConsumer) handle(ctx context.Context, msg messaging.Message) {
	msgctx, tenant, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Str("subject", msg.Subject).Msg("Entity-deleted fact dropped: the subject names no tenant.")
		msg.Ack()
		return
	}
	ev, err := entityevent.UnmarshalEntityDeletedEvent(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("subject", msg.Subject).Msg("Entity-deleted fact dropped: invalid payload.")
		msg.Ack()
		return
	}
	if ev.EntityType != entity.TypeDevice {
		msg.Ack()
		return
	}
	if ev.EntityToken == "" || ev.DeletedTime.IsZero() {
		log.Warn().Str("subject", msg.Subject).Msg("Entity-deleted device fact dropped: missing token or deletion time.")
		msg.Ack()
		return
	}
	var cancelled int64
	err = rc.Hold.RunCtx(ctx, msgctx, []messaging.Message{msg}, func(c context.Context) (e error) {
		cancelled, e = rc.Api.CancelDeletedDeviceCommands(c, ev.EntityToken, ev.DeletedTime)
		return e
	})
	if err != nil {
		rc.retryOrDrop(msgctx, msg, ev.EntityToken, err)
		return
	}
	if cancelled > 0 {
		log.Info().Str("tenant", tenant).Str("device", ev.EntityToken).Int64("cancelled", cancelled).
			Msg("Cancelled commands covered by device deletion.")
	}
	msg.Ack()
}

// retryOrDrop leaves a transiently failed fact unacked for redelivery, or dead-letters and acks it
// once the redelivery cap is reached. A deleted tenant is settled where it stands (messaging.Dispose).
func (rc *DeletionConsumer) retryOrDrop(ctx context.Context, msg messaging.Message, token string, err error) {
	switch messaging.Dispose(msg, err) {
	case messaging.TenantErased:
		msg.Ack()
		return
	case messaging.Exhausted:
		log.Error().Err(err).Str("device", token).Int("attempts", msg.NumDelivered).
			Msg("Entity-deleted fact failed past the redelivery cap; dead-lettering.")
		if rc.dead != nil {
			payload := msg.Value
			if len(payload) > maxDeadLetterPayload {
				payload = payload[:maxDeadLetterPayload]
			}
			if derr := rc.dead.WriteFor(ctx, msg, deadletter.Envelope{
				Reason:     deadletter.ReasonExhausted,
				Summary:    "an entity-deleted fact could not be applied after every delivery attempt",
				Detail:     err.Error(),
				Reference:  token,
				OccurredAt: time.Now().UTC(),
				Payload:    payload,
			}); derr != nil {
				log.Error().Err(derr).Str("device", token).Msg("LOST entity-deleted fact: it could be neither applied nor dead-lettered.")
			}
		}
		msg.Ack()
		return
	}
	log.Warn().Err(err).Str("device", token).Msg("Entity-deleted fact failed; will retry on redelivery.")
}

// Stop component.
func (rc *DeletionConsumer) Stop(ctx context.Context) error { return rc.lifecycle.Stop(ctx) }

// ExecuteStop cancels the read loop and waits for it to exit before the reader is torn down.
func (rc *DeletionConsumer) ExecuteStop(context.Context) error {
	if rc.procCancel != nil {
		rc.procCancel()
	}
	rc.readerWG.Wait()
	return nil
}

// Terminate component.
func (rc *DeletionConsumer) Terminate(ctx context.Context) error { return rc.lifecycle.Terminate(ctx) }

// ExecuteTerminate is a no-op; the reader is owned by the NATS manager.
func (rc *DeletionConsumer) ExecuteTerminate(context.Context) error { return nil }
