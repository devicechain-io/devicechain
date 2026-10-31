// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync"
	"time"

	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/rs/zerolog/log"
)

// OwnershipApplier is the slice of the projection the ownership consumer writes.
type OwnershipApplier interface {
	ApplyDeviceOwnership(ctx context.Context, deviceToken string, anchors []string, changedAt time.Time) (bool, error)
}

// OwnershipProcessor consumes device-management's device ownership-changed facts (ADR-081)
// and restamps the live view of each device they name, so a device that is sold — and then
// reports nothing — leaves its old owner's view at once rather than at its next event. How
// the fact and the device's events are ordered against each other is model/ownership.go's.
//
// It runs under the tenant of the fact's subject and NOTHING of any caller's: the fact is
// device-management's statement of state, and this is the projection's own write.
//
// One message is handled at a time. Facts are rare (an operator assigning or claiming a
// device) and each is one single-row UPDATE, so a pool would add ordering questions to buy
// throughput nobody needs.
type OwnershipProcessor struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Api          OwnershipApplier

	// Hold keeps a fact unacked, outside its delivery budget, while the projection's database
	// cannot take writes (core/storehold). Unlike the state projection, a dropped fact is NOT
	// repaired by the next event: a device that was sold and then reports nothing stays in its
	// old owner's scoped view -- a scope leak -- until it next sends something. Facts are rare,
	// and ApplyDeviceOwnership is idempotent (it is ordered by the fact's change time). Nil
	// holds nothing.
	Hold *storehold.Holder

	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	readPacer  *core.ReadPacer

	lifecycle core.LifecycleManager
}

// NewOwnershipProcessor builds the consumer over reader, writing through api.
func NewOwnershipProcessor(ms *core.Microservice, reader messaging.MessageReader, api OwnershipApplier,
	callbacks core.LifecycleCallbacks) *OwnershipProcessor {
	op := &OwnershipProcessor{Microservice: ms, Reader: reader, Api: api}
	op.lifecycle = core.NewLifecycleManager(fmt.Sprintf("%s-%s", ms.FunctionalArea, "ownership-proc"), op, callbacks)
	return op
}

func (op *OwnershipProcessor) pacer() *core.ReadPacer {
	if op.readPacer == nil {
		op.readPacer = core.NewReadPacer(op.Microservice, "device ownership")
	}
	return op.readPacer
}

// Initialize component.
func (op *OwnershipProcessor) Initialize(ctx context.Context) error {
	return op.lifecycle.Initialize(ctx)
}

// ExecuteInitialize derives the cancelable context the read loop runs under.
func (op *OwnershipProcessor) ExecuteInitialize(ctx context.Context) error {
	op.procCtx, op.procCancel = context.WithCancel(ctx)
	return nil
}

// Start component.
func (op *OwnershipProcessor) Start(ctx context.Context) error { return op.lifecycle.Start(ctx) }

// ExecuteStart runs the read loop.
func (op *OwnershipProcessor) ExecuteStart(context.Context) error {
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
func (op *OwnershipProcessor) Stop(ctx context.Context) error { return op.lifecycle.Stop(ctx) }

// ExecuteStop cancels the read loop and waits for it, so no fact is being written when the
// database closes.
func (op *OwnershipProcessor) ExecuteStop(context.Context) error {
	if op.procCancel != nil {
		op.procCancel()
	}
	op.readerWG.Wait()
	return nil
}

// Terminate component.
func (op *OwnershipProcessor) Terminate(ctx context.Context) error {
	return op.lifecycle.Terminate(ctx)
}

// ExecuteTerminate has nothing to release.
func (op *OwnershipProcessor) ExecuteTerminate(context.Context) error { return nil }

// handle applies one fact and settles its message. A fact no redelivery can fix — no tenant
// in its subject, a payload that will not decode — is acked and dropped, loudly. A write that
// failed takes the platform's disposition: settled for an erased tenant, left unacked for
// redelivery (AckWait paces it) until the last delivery, then dropped -- except that a write
// the DATABASE refused is held (Hold), not counted against that budget, because dropping a
// fact does not repair itself: the device's next event restamps it, but a sold device that
// reports nothing keeps showing in its old owner's view until then.
func (op *OwnershipProcessor) handle(ctx context.Context, msg messaging.Message) {
	tctx, tenant, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Str("subject", msg.Subject).Msg("Dropping device ownership-changed fact with no parseable tenant")
		_ = msg.Ack()
		return
	}
	fact, err := dmproto.UnmarshalDeviceOwnershipChangedEvent(msg.Value)
	if err != nil {
		log.Error().Err(err).Str("subject", msg.Subject).Msg("Dropping unparseable device ownership-changed fact")
		_ = msg.Ack()
		return
	}
	var applied bool
	holdCtx := op.procCtx
	if holdCtx == nil {
		holdCtx = ctx
	}
	err = op.Hold.RunCtx(holdCtx, tctx, []messaging.Message{msg}, func(c context.Context) (e error) {
		applied, e = op.Api.ApplyDeviceOwnership(c, fact.DeviceToken, fact.Anchors, fact.ChangedTime)
		return e
	})
	if err != nil {
		switch messaging.Dispose(msg, err) {
		case messaging.TenantErased:
			_ = msg.Ack()
		case messaging.Exhausted:
			log.Error().Err(err).Str("tenant", tenant).Str("device", fact.DeviceToken).Int("delivered", msg.NumDelivered).
				Msg("Giving up on a device ownership-changed fact; the device's next event restamps its live view")
			_ = msg.Ack()
		default:
			// Left UNACKED, not nak'd: AckWait paces the redelivery, where an immediate nak
			// would burn the delivery ceiling in milliseconds inside an outage.
			log.Warn().Err(err).Str("tenant", tenant).Str("device", fact.DeviceToken).
				Msg("Unable to restamp a device's live view with its owners; will retry on redelivery")
		}
		return
	}
	log.Debug().Str("tenant", tenant).Str("device", fact.DeviceToken).Strs("anchors", fact.Anchors).
		Bool("applied", applied).Msg("Device ownership-changed fact handled")
	_ = msg.Ack()
}

var _ OwnershipApplier = (*model.Api)(nil)
