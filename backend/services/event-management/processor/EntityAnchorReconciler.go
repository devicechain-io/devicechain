// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/devicechain-io/dc-device-management/proto"
	emmodel "github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// EntityAnchorReconciler consumes entity-deletion events (ADR-044) and removes the
// event_anchors rows that referenced the deleted entity, closing the cross-service
// dangling-reference gap that ADR-013's app-layer RI left open across the seam. It
// is a durable, at-least-once consumer: cleanup is idempotent (deleting
// already-absent rows is a no-op), so a redelivery — including the first-enablement
// replay of the retained stream — is harmless. It runs a single inline read loop:
// the cleanup is one cheap statement, so no worker pool is warranted.
type EntityAnchorReconciler struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Api          emmodel.EventManagementApi

	// Shutdown coordination (A5): procCancel stops the read loop; readerWG lets
	// ExecuteStop wait for it to unwind before returning.
	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup

	lifecycle core.LifecycleManager
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
		for {
			if r.processOne(r.procCtx) {
				break
			}
		}
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

// processOne reads and handles one entity-deletion message. Returns true on EOF
// (shutdown) so the loop exits.
func (r *EntityAnchorReconciler) processOne(ctx context.Context) bool {
	msg, err := r.Reader.ReadMessage(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			log.Info().Msg("Detected EOF on entity-deleted stream")
			return true
		}
		r.Reader.HandleResponse(err)
		return false
	}
	r.handle(ctx, msg)
	return false
}

// handle reconciles a single entity-deletion event: it stamps the tenant carried on
// the subject, deletes the referencing anchors, and acks. Poison (no tenant /
// unparseable payload) is acked to drop it; a transient DB error is naked for
// redelivery until the poison ceiling, then dropped.
func (r *EntityAnchorReconciler) handle(ctx context.Context, msg messaging.Message) {
	tctx, tenant, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Str("subject", msg.Subject).Msg("Dropping entity-deleted event with no parseable tenant")
		_ = msg.Ack()
		return
	}
	event, err := proto.UnmarshalEntityDeletedEvent(msg.Value)
	if err != nil {
		log.Error().Err(err).Str("subject", msg.Subject).Msg("Dropping unparseable entity-deleted event")
		_ = msg.Ack()
		return
	}
	// Bound the cleanup to events older than the deletion so a replayed deletion
	// event can't wipe a reused token's newer anchors (ADR-044 decision-4 amendment).
	removed, err := r.Api.DeleteAnchorsForEntity(tctx, string(event.EntityType), event.EntityToken, event.DeletedTime)
	if err != nil {
		if msg.NumDelivered >= messaging.MaxDeliver {
			log.Error().Err(err).Str("entity", event.EntityToken).Int("delivered", msg.NumDelivered).
				Msg("Giving up on entity-deleted event after max delivery attempts")
			_ = msg.Ack()
			return
		}
		log.Warn().Err(err).Str("entity", event.EntityToken).Msg("Anchor cleanup failed; will retry")
		_ = msg.Nak()
		return
	}
	log.Debug().Str("tenant", tenant).Str("entityType", string(event.EntityType)).
		Str("entity", event.EntityToken).Int64("anchorsRemoved", removed).
		Msg("Reconciled event anchors for deleted entity")
	_ = msg.Ack()
}
