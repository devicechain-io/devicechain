// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/devicechain-io/dc-command-delivery/config"
	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// deliveryEnvelope is the JSON payload published to a device on the
// device-commands subject. The command is addressed by its connection token
// (DeviceToken) and carries its own token so the device can correlate a
// response back to the persisted command.
type deliveryEnvelope struct {
	Token       string           `json:"token"`
	DeviceToken string           `json:"deviceToken"`
	Name        string           `json:"name"`
	Payload     *json.RawMessage `json:"payload,omitempty"`
}

// responseEnvelope is the JSON payload a device publishes on the
// command-responses subject to report the outcome of a command.
type responseEnvelope struct {
	CommandToken string  `json:"commandToken"`
	Success      bool    `json:"success"`
	Payload      *string `json:"payload,omitempty"`
	Error        *string `json:"error,omitempty"`
}

// CommandDeliveryProcessor owns the command delivery lifecycle: it delivers
// queued commands to devices, consumes device responses, and runs a background
// expiry + redelivery sweep (ADR-012 #4 / ThingsBoard §2.6).
type CommandDeliveryProcessor struct {
	Microservice           *core.Microservice
	CommandResponsesReader messaging.MessageReader
	DeviceCommandsWriter   messaging.MessageWriter
	Api                    model.CommandDeliveryApi

	// RED metrics for the response-consumer path (E13).
	metrics *core.ProcessorMetrics

	lifecycle core.LifecycleManager
	quit      chan struct{}
}

// NewCommandDeliveryProcessor creates a new command delivery processor.
func NewCommandDeliveryProcessor(ms *core.Microservice, responses messaging.MessageReader,
	commands messaging.MessageWriter, callbacks core.LifecycleCallbacks,
	api model.CommandDeliveryApi) *CommandDeliveryProcessor {
	cproc := &CommandDeliveryProcessor{
		Microservice:           ms,
		CommandResponsesReader: responses,
		DeviceCommandsWriter:   commands,
		Api:                    api,
		metrics:                ms.NewProcessorMetrics("response"),
	}

	// Create lifecycle manager.
	ipname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "command-delivery-proc")
	cproc.lifecycle = core.NewLifecycleManager(ipname, cproc, callbacks)
	return cproc
}

// deliverPendingCommands fetches a bounded batch of still-QUEUED commands across
// tenants and delivers each one, marking it SENT on a successful publish.
// Per-command errors are logged and skipped so one bad command does not abort the
// batch.
//
// Callers MUST hold the sweep lock (see sweepLocked). Publishing a command is a
// physical actuation, so running this concurrently on two pods sends the device the
// command twice.
func (cproc *CommandDeliveryProcessor) deliverPendingCommands(ctx context.Context) {
	pending, err := cproc.Api.PendingCommands(core.WithSystemContext(ctx))
	if err != nil {
		log.Error().Err(err).Msg("unable to load pending commands for delivery")
		return
	}
	for _, cmd := range pending {
		if err := cproc.deliverCommand(ctx, cmd); err != nil {
			log.Error().Err(err).Uint("command", cmd.ID).Str("device", cmd.DeviceToken).
				Msg("unable to deliver command")
		}
	}
	// A saturated batch means more is queued than one pass drains. Say so: the next
	// sweep picks up the remainder, but a persistently full batch is a backlog the
	// operator needs to see rather than infer from delivery latency.
	if len(pending) == model.PendingCommandBatch {
		log.Warn().Int("batch", len(pending)).
			Msg("Command redelivery batch was full; more commands remain queued for the next sweep.")
	}
}

// deliverCommand publishes a single command to its device's tenant subject and
// transitions it QUEUED -> SENT.
func (cproc *CommandDeliveryProcessor) deliverCommand(ctx context.Context, cmd *model.Command) error {
	envelope := deliveryEnvelope{
		Token:       cmd.Token,
		DeviceToken: cmd.DeviceToken,
		Name:        cmd.Name,
	}
	if cmd.Payload != nil {
		raw := json.RawMessage(*cmd.Payload)
		envelope.Payload = &raw
	}
	value, err := json.Marshal(envelope)
	if err != nil {
		return err
	}

	// Publish to the command's tenant subject and mark it SENT under the same
	// tenant context.
	tenantCtx := core.WithTenant(ctx, cmd.TenantId)
	msg := messaging.Message{
		Key:   []byte(cmd.Token),
		Value: value,
	}
	if err := cproc.DeviceCommandsWriter.WriteMessages(tenantCtx, msg); err != nil {
		cproc.DeviceCommandsWriter.HandleResponse(err)
		return err
	}
	cproc.DeviceCommandsWriter.HandleResponse(nil)

	if _, err := cproc.Api.MarkSent(tenantCtx, cmd.ID); err != nil {
		return err
	}
	return nil
}

// ProcessMessage reads a single device response and matches it to its command.
// Undecodable messages (or messages with no parseable tenant) are logged and
// skipped.
func (cproc *CommandDeliveryProcessor) ProcessMessage(ctx context.Context) bool {
	msg, err := cproc.CommandResponsesReader.ReadMessage(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			log.Info().Msg("Detected EOF on command responses stream")
			return true
		}
		cproc.CommandResponsesReader.HandleResponse(err)
		return false
	}

	// RED metrics for this response (E13): start timing now that we hold a
	// message, and record its disposition exactly once on whichever return
	// path it leaves by.
	done := cproc.metrics.Start()

	// Derive the per-message tenant from the subject (fail-closed). A response
	// we cannot route to a tenant is poison: ack it so it does not redeliver.
	tenantCtx, _, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).Msg(fmt.Sprintf("Skipping command response with no parseable tenant in subject %q", msg.Subject))
		_ = msg.Ack()
		done(core.ResultInvalid)
		return false
	}

	// An undecodable payload is poison: ack it so it does not redeliver.
	var response responseEnvelope
	if err := json.Unmarshal(msg.Value, &response); err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).Msg("Skipping undecodable command response")
		_ = msg.Ack()
		done(core.ResultInvalid)
		return false
	}

	if _, err := cproc.Api.MarkResponse(tenantCtx, response.CommandToken,
		response.Success, response.Payload, response.Error); err != nil {
		// Treat a failed persist as transient. Nak to retry until the
		// redelivery cap, then ack to give up (the device can resend and the
		// command sweep handles redelivery of the command itself).
		if msg.NumDelivered >= messaging.MaxDeliver {
			log.Error().Err(err).Str("command", response.CommandToken).Str("correlation", msg.CorrelationID()).Int("attempts", msg.NumDelivered).
				Msg("dropping command response after maximum delivery attempts")
			_ = msg.Ack()
			done(core.ResultFailed)
		} else {
			log.Error().Err(err).Str("command", response.CommandToken).Str("correlation", msg.CorrelationID()).Msg("unable to record command response")
			_ = msg.Nak()
			done(core.ResultRetry)
		}
		return false
	}

	// Response persisted successfully; ack so it is not redelivered.
	_ = msg.Ack()
	done(core.ResultOK)
	return false
}

// sweepLocked runs one expiry + redelivery sweep under a try-lock, so exactly one
// replica sweeps at a time.
//
// Without this every replica ran its own sweep over the same global QUEUED set and
// published every pending command, so an instance running N replicas delivered each
// command N times. That is not a wasted-work problem — a command is an actuation, and
// a device told twice to dispense, unlock, or reboot does it twice. It was also
// reachable by following our own guidance: the deployment docs recommend replicas:2
// for zero-downtime rollouts.
//
// The lock is a TRY, not a wait. A blocking acquire would merely queue the replicas
// and let each run the sweep in turn — the same duplicate deliveries, spread over
// time. A replica that cannot take the lock has nothing useful to do: its peer is
// already sweeping the same global set, and the ticker brings it back in 30 seconds.
//
// The lock covers expiry too. ExpireStale is a conditional UPDATE and safe to race,
// but holding one lock for the whole sweep keeps the invariant simple — one sweeper —
// rather than requiring a reader to re-derive which halves are safe to run twice.
func (cproc *CommandDeliveryProcessor) sweepLocked(ctx context.Context) {
	ran, err := cproc.Api.TrySweepLock(ctx, func() error {
		if _, err := cproc.Api.ExpireStale(core.WithSystemContext(ctx), time.Now()); err != nil {
			log.Error().Err(err).Msg("command expiry sweep failed")
		}
		cproc.deliverPendingCommands(ctx)
		return nil
	})
	if err != nil {
		log.Error().Err(err).Msg("could not acquire the command sweep lock")
		return
	}
	if !ran {
		log.Debug().Msg("Another replica holds the command sweep lock; skipping this pass.")
	}
}

// Initialize the component.
func (cproc *CommandDeliveryProcessor) Initialize(ctx context.Context) error {
	return cproc.lifecycle.Initialize(ctx)
}

// ExecuteInitialize runs initialization logic.
func (cproc *CommandDeliveryProcessor) ExecuteInitialize(ctx context.Context) error {
	cproc.quit = make(chan struct{})
	return nil
}

// Start the component.
func (cproc *CommandDeliveryProcessor) Start(ctx context.Context) error {
	return cproc.lifecycle.Start(ctx)
}

// ExecuteStart runs startup logic: an initial delivery pass, the response
// consumer loop, and the expiry + redelivery ticker.
func (cproc *CommandDeliveryProcessor) ExecuteStart(ctx context.Context) error {
	// Deliver any commands that were queued while the service was down
	// (deliver-on-reconnect semantics). Locked like the ticker's sweep: a rolling
	// restart starts several pods at once, which is precisely when an unguarded
	// startup pass would publish every queued command once per new pod.
	go cproc.sweepLocked(ctx)

	// Processing loop for inbound device responses.
	go func() {
		for {
			eof := cproc.ProcessMessage(ctx)
			if eof {
				break
			}
		}
	}()

	// Background expiry + redelivery ticker.
	go func() {
		ticker := time.NewTicker(config.RedeliveryInterval * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-cproc.quit:
				return
			case <-ticker.C:
				cproc.sweepLocked(ctx)
			}
		}
	}()
	return nil
}

// Stop the component.
func (cproc *CommandDeliveryProcessor) Stop(ctx context.Context) error {
	return cproc.lifecycle.Stop(ctx)
}

// ExecuteStop runs shutdown logic.
func (cproc *CommandDeliveryProcessor) ExecuteStop(context.Context) error {
	close(cproc.quit)
	return nil
}

// Terminate the component.
func (cproc *CommandDeliveryProcessor) Terminate(ctx context.Context) error {
	return cproc.lifecycle.Terminate(ctx)
}

// ExecuteTerminate runs termination logic.
func (cproc *CommandDeliveryProcessor) ExecuteTerminate(context.Context) error {
	return nil
}
