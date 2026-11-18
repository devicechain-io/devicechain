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

// DeletionApi is the slice of the API the deletion consumer uses.
type DeletionApi interface {
	DeviceDeleted(ctx context.Context, deviceToken string, incarnation uint64, deletedTime time.Time) error
}

// DeletionConsumer voids assignments covered by device-management's retained deletion
// facts. Transient failures use the same hold, retry and dead-letter discipline as reports.
type DeletionConsumer struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Api          DeletionApi

	dead *deadletter.Sink

	// Hold keeps a deletion outside its delivery budget while the store is unavailable.
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
	msgctx, _, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Str("subject", msg.Subject).Msg("Entity-deleted fact dropped: the subject names no tenant.")
		msg.Ack()
		return
	}
	ev, err := entityevent.UnmarshalEntityDeletedEvent(msg.Value)
	if err != nil || ev.EntityType != entity.TypeDevice || ev.EntityToken == "" || ev.DeletedTime.IsZero() {
		log.Warn().Err(err).Str("subject", msg.Subject).Msg("Entity-deleted fact dropped: it does not decode to a device.")
		msg.Ack()
		return
	}
	ctx2 := rc.procCtx
	if ctx2 == nil {
		ctx2 = ctx
	}
	if err := rc.Hold.RunCtx(ctx2, msgctx, []messaging.Message{msg}, func(c context.Context) error {
		return rc.Api.DeviceDeleted(c, ev.EntityToken, uint64(ev.EntityId), ev.DeletedTime)
	}); err != nil {
		rc.retryOrDrop(msgctx, msg, ev.EntityToken, err)
		return
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
