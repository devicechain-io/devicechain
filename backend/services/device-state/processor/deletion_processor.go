// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/rs/zerolog/log"
)

// DeletionApplier is the slice of the projection the deletion consumer writes.
type DeletionApplier interface {
	ApplyDeviceDeleted(ctx context.Context, deviceToken string, incarnation uint64, deletedTime time.Time) (bool, error)
}

// DeletionProcessor removes deleted devices from the tenant's live projection.
type DeletionProcessor struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Api          DeletionApplier

	// Hold keeps deletion facts outside their delivery budget during store outages.
	Hold *storehold.Holder

	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	readPacer  *core.ReadPacer

	lifecycle core.LifecycleManager
}

// NewDeletionProcessor builds the consumer over reader, writing through api.
func NewDeletionProcessor(ms *core.Microservice, reader messaging.MessageReader, api DeletionApplier,
	callbacks core.LifecycleCallbacks) *DeletionProcessor {
	op := &DeletionProcessor{Microservice: ms, Reader: reader, Api: api}
	op.lifecycle = core.NewLifecycleManager(fmt.Sprintf("%s-%s", ms.FunctionalArea, "deletion-proc"), op, callbacks)
	return op
}

func (op *DeletionProcessor) pacer() *core.ReadPacer {
	if op.readPacer == nil {
		op.readPacer = core.NewReadPacer(op.Microservice, "device deletion")
	}
	return op.readPacer
}

// Initialize component.
func (op *DeletionProcessor) Initialize(ctx context.Context) error {
	return op.lifecycle.Initialize(ctx)
}

// ExecuteInitialize derives the cancelable context the read loop runs under.
func (op *DeletionProcessor) ExecuteInitialize(ctx context.Context) error {
	op.procCtx, op.procCancel = context.WithCancel(ctx)
	return nil
}

// Start component.
func (op *DeletionProcessor) Start(ctx context.Context) error { return op.lifecycle.Start(ctx) }

// ExecuteStart runs the read loop.
func (op *DeletionProcessor) ExecuteStart(context.Context) error {
	op.readerWG.Add(1)
	go func() {
		defer op.readerWG.Done()
		messaging.RunConsumer(op.procCtx, op.Reader, op.pacer(), func(msg messaging.Message) bool {
			op.handle(op.procCtx, msg)
			return true
		})
	}()
	return nil
}

// Stop component.
func (op *DeletionProcessor) Stop(ctx context.Context) error { return op.lifecycle.Stop(ctx) }

// ExecuteStop cancels the read loop and waits for it, so no fact is being written when the
// database closes.
func (op *DeletionProcessor) ExecuteStop(context.Context) error {
	if op.procCancel != nil {
		op.procCancel()
	}
	op.readerWG.Wait()
	return nil
}

// Terminate component.
func (op *DeletionProcessor) Terminate(ctx context.Context) error {
	return op.lifecycle.Terminate(ctx)
}

// ExecuteTerminate has nothing to release.
func (op *DeletionProcessor) ExecuteTerminate(context.Context) error { return nil }

// handle applies one fact and settles its message. A fact no redelivery can fix — no tenant
// in its subject, a payload that will not decode — is acked and dropped, loudly. A write that
// failed takes the platform's disposition: settled for an erased tenant, left unacked for
// redelivery (AckWait paces it) until the last delivery, then dropped -- except that a write
// the DATABASE refused is held (Hold), not counted against that budget, because dropping a
// fact leaves old live data visible until the orphan sweep repairs it.
func (op *DeletionProcessor) handle(ctx context.Context, msg messaging.Message) {
	tctx, tenant, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Str("subject", msg.Subject).Msg("Dropping device entity-deleted fact with no parseable tenant")
		_ = msg.Ack()
		return
	}
	fact, err := entityevent.UnmarshalEntityDeletedEvent(msg.Value)
	if err != nil {
		log.Error().Err(err).Str("subject", msg.Subject).Msg("Dropping unparseable device entity-deleted fact")
		_ = msg.Ack()
		return
	}
	if fact.EntityType != entity.TypeDevice || fact.EntityToken == "" || fact.DeletedTime.IsZero() {
		_ = msg.Ack()
		return
	}
	var applied bool
	holdCtx := op.procCtx
	if holdCtx == nil {
		holdCtx = ctx
	}
	err = op.Hold.RunCtx(holdCtx, tctx, []messaging.Message{msg}, func(c context.Context) (e error) {
		applied, e = op.Api.ApplyDeviceDeleted(c, fact.EntityToken, uint64(fact.EntityId), fact.DeletedTime)
		return e
	})
	if err != nil {
		switch messaging.Dispose(msg, err) {
		case messaging.TenantErased:
			_ = msg.Ack()
		case messaging.Exhausted:
			log.Error().Err(err).Str("tenant", tenant).Str("device", fact.EntityToken).Int("delivered", msg.NumDelivered).
				Msg("Giving up on a device entity-deleted fact; the orphan sweep will repair its live view")
			_ = msg.Ack()
		default:
			// Left UNACKED, not nak'd: AckWait paces the redelivery, where an immediate nak
			// would burn the delivery ceiling in milliseconds inside an outage.
			log.Warn().Err(err).Str("tenant", tenant).Str("device", fact.EntityToken).
				Msg("Unable to remove a deleted device's live view; will retry on redelivery")
		}
		return
	}
	log.Debug().Str("tenant", tenant).Str("device", fact.EntityToken).
		Bool("applied", applied).Msg("Device entity-deleted fact handled")
	_ = msg.Ack()
}

var _ DeletionApplier = (*model.Api)(nil)
