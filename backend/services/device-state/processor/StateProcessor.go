// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-device-state/config"
	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// StateProcessor is a second, independent consumer of the resolved-events stream
// (fan-out alongside event-management's persistence). For every resolved event it
// updates the originating device's live state projection, and a background
// monitor flips devices to inactive after an inactivity timeout.
type StateProcessor struct {
	Microservice         *core.Microservice
	ResolvedEventsReader messaging.MessageReader
	Api                  model.DeviceStateApi

	lifecycle core.LifecycleManager
	quit      chan struct{}
}

// Create a new device-state processor.
func NewStateProcessor(ms *core.Microservice, reader messaging.MessageReader,
	callbacks core.LifecycleCallbacks, api model.DeviceStateApi) *StateProcessor {
	sp := &StateProcessor{
		Microservice:         ms,
		ResolvedEventsReader: reader,
		Api:                  api,
	}

	// Create lifecycle manager.
	spname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "state-proc")
	sp.lifecycle = core.NewLifecycleManager(spname, sp, callbacks)
	return sp
}

// Initialize component.
func (sp *StateProcessor) Initialize(ctx context.Context) error {
	return sp.lifecycle.Initialize(ctx)
}

// Lifecycle callback that runs initialization logic.
func (sp *StateProcessor) ExecuteInitialize(ctx context.Context) error {
	sp.quit = make(chan struct{})
	return nil
}

// Start component.
func (sp *StateProcessor) Start(ctx context.Context) error {
	return sp.lifecycle.Start(ctx)
}

// Execute primary processing loop. This is done in a goroutine since it runs
// indefinitely. For each resolved event it merges the originating device's state.
func (sp *StateProcessor) ProcessMessage(ctx context.Context) bool {
	msg, err := sp.ResolvedEventsReader.ReadMessage(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			log.Info().Msg("Detected EOF on resolved events stream")
			return true
		}
		sp.ResolvedEventsReader.HandleResponse(err)
		return false
	}

	// Derive the per-message tenant from the message subject and build a
	// tenant-scoped context. Without a parseable tenant the state can not be
	// updated safely (fail-closed) so the message is skipped.
	msgctx, _, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Msg(fmt.Sprintf("Skipping message with no parseable tenant in subject %q", msg.Subject))
		// Poison message: redelivery will not make the tenant parseable, so ack
		// to drop it rather than redeliver up to MaxDeliver times.
		msg.Ack()
		return false
	}

	// Attempt to unmarshal the resolved event. A projection has no failed-events
	// channel, so an unparseable message is logged and dropped.
	event, err := dmproto.UnmarshalResolvedEvent(msg.Value)
	if err != nil {
		log.Warn().Err(err).Msg(fmt.Sprintf("Skipping resolved event that could not be parsed from subject %q", msg.Subject))
		// Poison message: redelivery will not make it parseable, so ack to drop.
		msg.Ack()
		return false
	}

	// Update the originating device's live state projection.
	if _, err := sp.Api.MergeDeviceState(msgctx, event.SourceDeviceId, event.OccurredTime); err != nil {
		log.Error().Err(err).Msg(fmt.Sprintf("Unable to merge device state for device %d", event.SourceDeviceId))
		// Treat as transient (e.g. a DB blip). Retry via redelivery until the
		// finite MaxDeliver cap, then give up: the projection is reconstructable,
		// so dropping is preferable to looping forever.
		if msg.NumDelivered >= messaging.MaxDeliver {
			log.Error().Msg(fmt.Sprintf("Giving up on device state projection update for device %d after %d attempts", event.SourceDeviceId, msg.NumDelivered))
			msg.Ack()
		} else {
			msg.Nak()
		}
		return false
	}

	// Projection updated successfully: ack so the message is not redelivered.
	msg.Ack()
	return false
}

// Lifecycle callback that runs startup logic.
func (sp *StateProcessor) ExecuteStart(ctx context.Context) error {
	// Processing loop for inbound resolved events.
	go func() {
		for {
			eof := sp.ProcessMessage(ctx)
			if eof {
				break
			}
		}
	}()

	// Background inactivity monitor: periodically flip devices that have gone
	// quiet to inactive. The sweep runs under a system context so it spans all
	// tenants in a single pass (each row keeps its own tenant_id on save).
	go func() {
		ticker := time.NewTicker(config.InactivityRecheckInterval * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-sp.quit:
				return
			case <-ticker.C:
				count, err := sp.Api.SweepInactive(core.WithSystemContext(ctx), time.Now())
				if err != nil {
					log.Error().Err(err).Msg("Inactivity sweep failed")
				} else if count > 0 {
					log.Info().Msg(fmt.Sprintf("Inactivity monitor marked %d device(s) inactive", count))
				}
			}
		}
	}()
	return nil
}

// Stop component.
func (sp *StateProcessor) Stop(ctx context.Context) error {
	return sp.lifecycle.Stop(ctx)
}

// Lifecycle callback that runs shutdown logic.
func (sp *StateProcessor) ExecuteStop(context.Context) error {
	close(sp.quit)
	return nil
}

// Terminate component.
func (sp *StateProcessor) Terminate(ctx context.Context) error {
	return sp.lifecycle.Terminate(ctx)
}

// Lifecycle callback that runs termination logic.
func (sp *StateProcessor) ExecuteTerminate(context.Context) error {
	return nil
}
