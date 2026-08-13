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
	"github.com/prometheus/client_golang/prometheus"
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
// expiry + redelivery sweep (ADR-012 #4).
type CommandDeliveryProcessor struct {
	Microservice           *core.Microservice
	CommandResponsesReader messaging.MessageReader
	DeviceCommandsWriter   messaging.MessageWriter
	Api                    model.CommandDeliveryApi

	// RED metrics for the response-consumer path (E13).
	metrics *core.ProcessorMetrics

	// TenantDeleted reports whether a tenant has been through the ADR-077 delete door.
	// A closure rather than the governance resolver type so this package needs no live
	// user-management to test. MAY BE NIL — read it through tenantDeleted, never
	// directly: this struct's fields are exported and it is built by literal in places
	// the constructor never runs.
	TenantDeleted func(tenant string) bool

	// ClaimsLost counts dispatches abandoned because another dispatcher claimed the
	// command first, and ClaimsStranded counts commands left reading SENT because the
	// publish failed AND the release failed too.
	//
	// 🔑 BOTH EXIST BECAUSE THEY USED TO BE SILENT. A lost claim was previously a
	// zero-row update nobody looked at; a stranded row has no representation at all
	// except a TIMEOUT that blames the device. Nil is tolerated (skipped) for the same
	// reason TenantDeleted is: this struct is assembled by literal in tests.
	ClaimsLost     prometheus.Counter
	ClaimsStranded prometheus.Counter

	lifecycle core.LifecycleManager
	quit      chan struct{}
}

// NewCommandDeliveryProcessor creates a new command delivery processor.
//
// tenantDeleted gates delivery on the ADR-077 tenant lifecycle. Nil disables the gate
// (every tenant reads live), matching the resolver's own fail-open, so an unwired gate
// behaves like an unreachable authority rather than stalling every tenant's commands.
func NewCommandDeliveryProcessor(ms *core.Microservice, responses messaging.MessageReader,
	commands messaging.MessageWriter, callbacks core.LifecycleCallbacks,
	api model.CommandDeliveryApi, tenantDeleted func(string) bool) *CommandDeliveryProcessor {
	cproc := &CommandDeliveryProcessor{
		Microservice:           ms,
		CommandResponsesReader: responses,
		DeviceCommandsWriter:   commands,
		Api:                    api,
		metrics:                ms.NewProcessorMetrics("response"),
		TenantDeleted:          tenantDeleted,
		ClaimsLost: ms.NewCounter("command_delivery_claims_lost_total",
			"Dispatches abandoned because another dispatcher claimed the command first", nil),
		ClaimsStranded: ms.NewCounter("command_delivery_claims_stranded_total",
			"Commands left reading SENT because their publish failed and the release failed too; "+
				"each will expire as TIMEOUT, wrongly blaming the device", nil),
	}

	// Create lifecycle manager.
	ipname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "command-delivery-proc")
	cproc.lifecycle = core.NewLifecycleManager(ipname, cproc, callbacks)
	return cproc
}

// deliverPendingCommands fetches the still-QUEUED commands across tenants and
// delivers each one, marking it SENT on a successful publish.
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
		// 🔴 The ADR-077 lifecycle gate, and the reason it is HERE rather than inside
		// deliverCommand: publishing is a PHYSICAL ACTUATION and it happens BEFORE
		// MarkSent. A command queued before an operator deleted the tenant would
		// otherwise fire a valve or a relay on an offboarded customer's hardware — and
		// once the tenant's rows are swept, the actuation has already happened by the time
		// MarkSent fails to find its row, so the device acts and the platform's only
		// record of it is an error log on a command it can no longer describe.
		//
		// The row is LEFT QUEUED, not failed. It is about to be deleted with the rest of
		// the tenant, so transitioning it would be writing into data being erased in
		// order to describe the erasure; and if the purge is ever abandoned, a queued
		// command is the state an operator can reason about. `continue` also skips the
		// error log the delivery path would emit — this is a refusal, not a failure, and
		// logging one line per command per sweep tick forever is how a correct refusal
		// gets mistaken for an outage.
		if cproc.tenantDeleted(cmd.TenantId) {
			continue
		}
		if err := cproc.deliverCommand(ctx, cmd); err != nil {
			log.Error().Err(err).Uint("command", cmd.ID).Str("device", cmd.DeviceToken).
				Msg("unable to deliver command")
		}
	}
}

// incr adds to an optional counter, skipping a nil one so a literal-built processor
// needs no metrics registry.
func incr(c prometheus.Counter, n float64) {
	if c != nil {
		c.Add(n)
	}
}

// tenantDeleted answers the ADR-077 gate, tolerating an unwired closure.
//
// A method rather than a normalization in the constructor because this struct's fields
// are exported and it is assembled by literal — today only by this package's own test
// fixture, but that is enough: a constructor-only guarantee is one a literal can silently
// drop, and what it drops here is a nil call on the delivery sweep. A gate that crashes
// the sweep when unwired is strictly worse than one that is off, and "off" is the same
// fail-open the resolver behind it takes when it cannot reach user-management.
//
// 🔴 The cost of the fixture skipping the constructor is real and was paid: with the
// plumbing untested, deleting `TenantDeleted: tenantDeleted` from the constructor
// disabled the gate in every shipped binary and left the whole suite green.
// TestConstructorWiresTheLifecycleGate is what closes that, and it is the reason this
// accessor is not a licence to keep testing around the constructor.
func (cproc *CommandDeliveryProcessor) tenantDeleted(tenant string) bool {
	return cproc.TenantDeleted != nil && cproc.TenantDeleted(tenant)
}

// deliverCommand claims a single command, then publishes it to its device's subject.
//
// 🔴 CLAIM BEFORE PUBLISH, NOT AFTER, AND THE ORDER IS THE WHOLE POINT. Publishing first
// left a window between the publish and the mark in which another dispatcher — the LwM2M
// wake drain, or the release path — could claim the same row and actuate the device a
// second time. A command is a physical movement of real hardware, so a duplicate is not a
// bookkeeping wrinkle.
//
// Claiming first inverts the risk: the failure mode becomes a row claimed but not
// published, which ReleaseClaim returns to QUEUED for the next tick. A command delivered
// late is recoverable; a command delivered twice is not.
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
	// Published to the TARGET DEVICE's subject, not the tenant's. Before this, every
	// command went to one tenant-wide subject that every device in the tenant was
	// granted to subscribe to, so isolation between devices rested entirely on each
	// device choosing to filter on the envelope's deviceToken. A device that simply
	// did not filter — or a compromised one — read every command in the tenant,
	// payloads included. The subject now carries the device, and the broker grant is
	// narrowed to match, so the isolation is enforced rather than requested.
	// Claim first. A lost claim means another dispatcher got there — benign, and now
	// COUNTED rather than silent: while nothing wrote HELD this could not happen at all,
	// so a standing rate here is the signal that two dispatch paths are overlapping.
	claimed, err := cproc.Api.MarkSent(tenantCtx, cmd.ID)
	if err != nil {
		return err
	}
	if !claimed {
		incr(cproc.ClaimsLost, 1)
		log.Debug().Str("command", cmd.Token).Str("device", cmd.DeviceToken).
			Msg("Another dispatcher claimed this command first; not publishing it again.")
		return nil
	}

	if err := cproc.DeviceCommandsWriter.WriteToDevice(tenantCtx, cmd.DeviceToken, msg); err != nil {
		cproc.DeviceCommandsWriter.HandleResponse(err)
		// The claim is now a lie unless it is undone: the row reads SENT with a
		// sent_time for a command that never went out, and nothing would ever pick it
		// up again. Release failures are logged rather than returned — the publish error
		// is the one worth propagating, and a swallowed release is exactly the kind of
		// silence that produced this whole class of defect, so it gets a counter too.
		if _, rerr := cproc.Api.ReleaseClaim(tenantCtx, cmd.ID); rerr != nil {
			incr(cproc.ClaimsStranded, 1)
			log.Error().Err(rerr).Str("command", cmd.Token).
				Msg("Could not release a command whose publish failed; it will read SENT until its TTL " +
					"expires it as TIMEOUT, which wrongly blames the device.")
		}
		return err
	}
	cproc.DeviceCommandsWriter.HandleResponse(nil)
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
		// Treat a failed persist as transient. Leave it unacked to retry until
		// the redelivery cap, then ack to give up (the device can resend and the
		// command sweep handles redelivery of the command itself).
		if msg.NumDelivered >= messaging.MaxDeliver {
			log.Error().Err(err).Str("command", response.CommandToken).Str("correlation", msg.CorrelationID()).Int("attempts", msg.NumDelivered).
				Msg("dropping command response after maximum delivery attempts")
			_ = msg.Ack()
			done(core.ResultFailed)
		} else {
			// Leave it UNACKED (do not nak) so AckWait paces redelivery — an
			// immediate nak would burn MaxDeliver in ~1.4ms inside an outage.
			// Reference disposition: event-sources' settler (ADR-030).
			log.Error().Err(err).Str("command", response.CommandToken).Str("correlation", msg.CorrelationID()).Msg("unable to record command response")
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
//
// This makes delivery single-sweeper, NOT exactly-once, and the difference is worth
// stating because it is easy to over-read. A pg advisory lock is bound to the session
// holding it, so if this pod's connection dies mid-sweep — a failover, a network blip —
// Postgres releases the lock immediately and a peer may pick up the same still-QUEUED
// rows and publish them again. deliverCommand also publishes BEFORE it marks SENT, so a
// publish that succeeds and a MarkSent that fails leaves the command queued for the next
// pass. Command delivery is therefore at-least-once, as it was before; what this removes
// is the guaranteed, every-single-tick duplication of running N sweepers by design.
// Closing the remaining window needs a from-state-predicated claim before the publish,
// which needs an intermediate lifecycle state — see the note on TrySweepLock.
func (cproc *CommandDeliveryProcessor) sweepLocked(ctx context.Context) {
	ran, err := cproc.Api.TrySweepLock(ctx, func() error {
		count, byFromStatus, err := cproc.Api.ExpireStale(core.WithSystemContext(ctx), time.Now())
		if err != nil {
			log.Error().Err(err).Msg("command expiry sweep failed")
		}
		// Report the breakdown, not just the total. A command that lapsed out of
		// HELD was never dispatched — the device was absent for its whole TTL — while
		// one that lapsed out of SENT was delivered to and ignored. Those point at
		// opposite halves of the system (the fleet vs the devices' firmware), and a
		// single "expired N commands" line cannot tell an operator which they have.
		if count > 0 {
			log.Info().Int64("expired", count).Interface("fromStatus", byFromStatus).
				Msg("Command expiry sweep reached a terminal state for stale commands.")
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
