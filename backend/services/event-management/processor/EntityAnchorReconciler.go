// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	emmodel "github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
	"sync"
)

// EntityAnchorReconciler consumes entity-deletion events (ADR-044) and removes the
// event_anchors rows that referenced the deleted entity, closing the cross-service
// dangling-reference gap that ADR-013's app-layer RI left open across the seam. It
// is a durable, at-least-once consumer: cleanup is idempotent (deleting
// already-absent rows is a no-op), so a redelivery — including the first-enablement
// replay of the retained stream — is harmless. It runs a single inline read loop:
// watermark recording and cleanup are inline database operations; no worker pool is needed.
type EntityAnchorReconciler struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Api          emmodel.EventManagementApi

	// Shutdown coordination (A5): procCancel stops the read loop; readerWG lets
	// ExecuteStop wait for it to unwind before returning.
	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup

	// readPacer spaces out the retries after a non-EOF read error and ends the loop once
	// the errors stop clearing. Built on first use by pacer(), because this struct is also
	// assembled by literal in tests that never run the constructor.
	readPacer *core.ReadPacer

	lifecycle core.LifecycleManager
}

// pacer returns the read loop's error pacer, building it on first use. It is touched only
// by the single read goroutine, which is the pacer's own contract.
func (r *EntityAnchorReconciler) pacer() *core.ReadPacer {
	if r.readPacer == nil {
		r.readPacer = core.NewReadPacer(r.Microservice, "entity-deleted")
	}
	return r.readPacer
}

// NewEntityAnchorReconciler builds the reconciler over the entity-deleted reader.
func NewEntityAnchorReconciler(ms *core.Microservice, reader messaging.MessageReader,
	api emmodel.EventManagementApi, callbacks core.LifecycleCallbacks) *EntityAnchorReconciler {
	r := &EntityAnchorReconciler{
		Microservice: ms,
		Reader:       reader,
		Api:          api,
	}
	name := fmt.Sprintf("%s-%s", ms.FunctionalArea, "entity-anchor-reconciler")
	r.lifecycle = core.NewLifecycleManager(name, r, callbacks)
	return r
}

func (r *EntityAnchorReconciler) Initialize(ctx context.Context) error {
	return r.lifecycle.Initialize(ctx)
}

func (r *EntityAnchorReconciler) ExecuteInitialize(ctx context.Context) error {
	r.procCtx, r.procCancel = context.WithCancel(ctx)
	return nil
}

func (r *EntityAnchorReconciler) Start(ctx context.Context) error {
	return r.lifecycle.Start(ctx)
}

func (r *EntityAnchorReconciler) ExecuteStart(context.Context) error {
	r.readerWG.Add(1)
	go func() {
		defer r.readerWG.Done()
		r.readLoop(r.procCtx)
	}()
	return nil
}

func (r *EntityAnchorReconciler) Stop(ctx context.Context) error {
	return r.lifecycle.Stop(ctx)
}

func (r *EntityAnchorReconciler) ExecuteStop(context.Context) error {
	if r.procCancel != nil {
		r.procCancel()
	}
	r.readerWG.Wait()
	return nil
}

func (r *EntityAnchorReconciler) Terminate(ctx context.Context) error {
	return r.lifecycle.Terminate(ctx)
}

func (r *EntityAnchorReconciler) ExecuteTerminate(context.Context) error {
	return nil
}

// readLoop drains the entity-deleted stream until the context is cancelled, the reader
// reports EOF, or a run of non-EOF read errors outlasts the pacer's budget.
//
// 🔑 THAT LAST ONE ENDS THE WHOLE SERVICE, NOT JUST THIS RECONCILER, AND THAT IS THE POINT.
// This loop records deletion watermarks and performs primary cleanup. If it stops,
// new deletion facts have no watermark for the periodic resweep to recover, while
// the service still looks healthy. The errors that get that far are broker-level, so they are the same ones
// the persistence loop in this process is facing — and a restart, which re-dials the broker
// and re-creates the durable, is the remedy for most of them. Both loops leave their
// messages unacked, so a restart costs redelivery, not data.
func (r *EntityAnchorReconciler) readLoop(ctx context.Context) {
	messaging.RunConsumer(ctx, r.Reader, r.pacer(), func(msg messaging.Message) bool {
		r.handle(ctx, msg)
		return true
	})
}

// handle reconciles a single entity-deletion event: it stamps the tenant carried on
// the subject, deletes the referencing anchors, and acks. Poison (no tenant /
// unparseable payload) is acked to drop it; a transient DB error is left unacked for
// AckWait-paced redelivery until the poison ceiling, then dropped.
func (r *EntityAnchorReconciler) handle(ctx context.Context, msg messaging.Message) {
	tctx, tenant, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Str("subject", msg.Subject).Msg("Dropping entity-deleted event with no parseable tenant")
		_ = msg.Ack()
		return
	}
	event, err := entityevent.UnmarshalEntityDeletedEvent(msg.Value)
	if err != nil {
		log.Error().Err(err).Str("subject", msg.Subject).Msg("Dropping unparseable entity-deleted event")
		_ = msg.Ack()
		return
	}
	// Bound cleanup by the platform's receipt time, strictly before deletion
	// (ADR-044 decision-4 amendment). A late deletion fact must preserve a reused
	// token's buffered telemetry even when its device timestamp predates deletion.
	removed, err := r.Api.DeleteAnchorsForEntity(tctx, string(event.EntityType), event.EntityToken, event.DeletedTime)
	if err != nil {
		switch messaging.Dispose(msg, err) {
		case messaging.TenantErased:
			log.Debug().Err(err).Msg("Dropping message for an erased tenant")
			_ = msg.Ack()
			return
		case messaging.Exhausted:
			// If the watermark committed before cleanup failed, its armed periodic
			// resweep covers this exhausted delivery (including a crash after commit).
			log.Error().Err(err).Str("entity", event.EntityToken).Int("delivered", msg.NumDelivered).
				Msg("Giving up on entity-deleted event after max delivery attempts")
			_ = msg.Ack()
			return
		}
		// Transient: leave it UNACKED (do not nak) so AckWait paces redelivery — an
		// immediate nak would burn MaxDeliver in ~1.4ms inside an outage. Reference
		// disposition: event-sources' settler (ADR-030).
		log.Warn().Err(err).Str("entity", event.EntityToken).Msg("Anchor cleanup failed; will retry on redelivery")
		return
	}
	log.Debug().Str("tenant", tenant).Str("entityType", string(event.EntityType)).
		Str("entity", event.EntityToken).Int64("anchorsRemoved", removed).
		Msg("Reconciled event anchors for deleted entity")
	_ = msg.Ack()
}
