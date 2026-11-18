// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package processor holds dashboard-management's one consumer: the removal of dashboard
// shares that name a deleted customer or group.
package processor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// ShareRemover is the slice of the Api this consumer writes through.
type ShareRemover interface {
	DeleteSharesOfEntity(ctx context.Context, entityType, token string, deletedAt time.Time) (int64, error)
}

// ShareReconciler consumes entity-deletion events (ADR-044) and removes the dashboard shares
// that named the deleted customer or group (ADR-081). A share naming a customer that no
// longer exists grants nothing today, but a customer later created under the same token would
// inherit every dashboard the old one was shared with — so the share goes with its customer,
// and a group's with its group.
//
// It is a durable, at-least-once consumer and cleanup is idempotent: deleting shares that
// are already gone is a no-op, so a redelivery, or the first-enablement replay of the
// retained stream, is harmless. Removal is bounded to shares made no later than the
// deletion. Customer and group tokens are retired when deleted.
// One inline read loop, as event-management's anchor reconciler: the work is one cheap statement.
type ShareReconciler struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Shares       ShareRemover
	Dead         *deadletter.Sink

	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup

	readPacer *core.ReadPacer

	lifecycle core.LifecycleManager
}

// pacer returns the read loop's error pacer, building it on first use: the struct is also
// assembled by literal in tests that never run the constructor.
func (r *ShareReconciler) pacer() *core.ReadPacer {
	if r.readPacer == nil {
		r.readPacer = core.NewReadPacer(r.Microservice, "entity-deleted")
	}
	return r.readPacer
}

// NewShareReconciler builds the reconciler over the entity-deleted reader.
func NewShareReconciler(ms *core.Microservice, reader messaging.MessageReader, shares ShareRemover,
	callbacks core.LifecycleCallbacks) *ShareReconciler {
	r := &ShareReconciler{Microservice: ms, Reader: reader, Shares: shares}
	r.lifecycle = core.NewLifecycleManager(fmt.Sprintf("%s-%s", ms.FunctionalArea, "share-reconciler"), r, callbacks)
	return r
}

func (r *ShareReconciler) Initialize(ctx context.Context) error { return r.lifecycle.Initialize(ctx) }

func (r *ShareReconciler) ExecuteInitialize(ctx context.Context) error {
	r.procCtx, r.procCancel = context.WithCancel(ctx)
	return nil
}

func (r *ShareReconciler) Start(ctx context.Context) error { return r.lifecycle.Start(ctx) }

func (r *ShareReconciler) ExecuteStart(context.Context) error {
	r.readerWG.Add(1)
	go func() {
		defer r.readerWG.Done()
		messaging.RunConsumer(r.procCtx, r.Reader, r.pacer(), func(msg messaging.Message) bool {
			r.handle(r.procCtx, msg)
			return true
		})
	}()
	return nil
}

func (r *ShareReconciler) Stop(ctx context.Context) error { return r.lifecycle.Stop(ctx) }

func (r *ShareReconciler) ExecuteStop(context.Context) error {
	if r.procCancel != nil {
		r.procCancel()
	}
	r.readerWG.Wait()
	return nil
}

func (r *ShareReconciler) Terminate(ctx context.Context) error { return r.lifecycle.Terminate(ctx) }

func (r *ShareReconciler) ExecuteTerminate(context.Context) error { return nil }

// handle reconciles one entity-deletion event: it stamps the tenant carried on the subject,
// removes the shares that named the entity, and acks. Poison (no tenant, an unparseable
// payload) is acked to drop it; a database error is left unacked for AckWait-paced
// redelivery until the delivery ceiling, then dead-lettered. An entity that no share can name — a
// device, an asset — is acked having removed nothing.
func (r *ShareReconciler) handle(ctx context.Context, msg messaging.Message) {
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
	removed, err := r.Shares.DeleteSharesOfEntity(tctx, string(event.EntityType), event.EntityToken, event.DeletedTime)
	if err != nil {
		switch messaging.Dispose(msg, err) {
		case messaging.TenantErased:
			log.Debug().Err(err).Msg("Dropping message for an erased tenant")
			_ = msg.Ack()
			return
		case messaging.Exhausted:
			log.Error().Err(err).Str("entity", event.EntityToken).Int("delivered", msg.NumDelivered).
				Msg("Giving up on entity-deleted event after max delivery attempts")
			payload := msg.Value
			if len(payload) > 64*1024 {
				payload = payload[:64*1024]
			}
			if derr := r.Dead.WriteFor(tctx, msg, deadletter.Envelope{
				Reason:     deadletter.ReasonExhausted,
				Summary:    "dashboard share cleanup exhausted delivery attempts",
				Detail:     err.Error(),
				Reference:  event.EntityToken,
				OccurredAt: time.Now().UTC(),
				Payload:    payload,
			}); derr != nil {
				log.Error().Err(derr).Msg("LOST entity-deleted fact: cleanup and dead-letter write both failed")
			}
			_ = msg.Ack()
			return
		}
		// Left UNACKED, not nak'd: AckWait paces the redelivery, where an immediate nak
		// would burn the delivery ceiling in milliseconds inside an outage.
		log.Warn().Err(err).Str("entity", event.EntityToken).Msg("Dashboard share cleanup failed; will retry on redelivery")
		return
	}
	if removed > 0 {
		log.Info().Str("tenant", tenant).Str("entityType", string(event.EntityType)).
			Str("entity", event.EntityToken).Int64("sharesRemoved", removed).
			Msg("Removed dashboard shares naming a deleted entity")
	}
	_ = msg.Ack()
}
