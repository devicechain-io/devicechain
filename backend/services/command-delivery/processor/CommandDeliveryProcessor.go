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
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
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

	// DispatchNonce names the claim this publish belongs to. A transport that finds the
	// device unreachable quotes it back when handing the command over, so a request still
	// in redelivery cannot park a row that has since been re-claimed and actuated. It is
	// opaque to a device and nothing needs to read it except the transport that may park.
	DispatchNonce string `json:"dispatchNonce,omitempty"`
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
	// command first, BY DISPATCH PATH, and ClaimsStranded counts commands left reading
	// SENT because the publish failed AND the release failed too.
	//
	// 🔑 BOTH EXIST BECAUSE THEY USED TO BE SILENT. A lost claim was previously a
	// zero-row update nobody looked at; a stranded row has no representation at all
	// except a TIMEOUT that blames the device. Nil is tolerated (skipped) for the same
	// reason TenantDeleted is: this struct is assembled by literal in tests.
	//
	// 🔴🔴 THE path LABEL EXISTS BECAUSE THE DISPATCH NUDGE INVERTED THIS COUNTER'S
	// MEANING, AND IT LANDED IN THE SAME CHANGE THAT INVERTED IT. Unlabelled, its reading
	// was: "while nothing wrote HELD this could not happen at all, so a standing rate is
	// the signal that two dispatch paths are overlapping." The nudge MAKES two dispatch
	// paths the design — a nudge and a sweep tick racing for one row is the ordinary,
	// correct case, and the CAS in MarkSent is what makes it safe — so a standing rate
	// became normal and the overlap bug the counter was watching for became invisible
	// underneath it. Labelling by path restores the signal: sweep-versus-nudge losses are
	// the expected series, and a rate on ONE path with no traffic on the other is the
	// shape that still means something is wrong. An unlabelled counter whose meaning is
	// silently inverted by a feature is worse than no counter.
	ClaimsLost     *prometheus.CounterVec
	ClaimsStranded prometheus.Counter

	// SweepInterval is the operator-configured cadence of the delivery sweep. ZERO MEANS
	// "use the platform default" — read it through sweepInterval, never directly, for the
	// same reason the fields below carry that warning: this struct is exported and
	// assembled by literal in places the constructor never runs, so a zero here is an
	// unset field and not an operator asking for a zero-second tick.
	SweepInterval time.Duration

	// Presence answers, for a batch of one tenant's devices, whether dispatching to
	// each is worth doing right now. MAY BE NIL — read it through presenceStates, never
	// directly.
	//
	// 🔴 NIL MEANS THE GATE IS OFF, AND OFF MEANS DELIVER. That is the same fail-open
	// every other authority on this path takes, and here it is the only safe direction:
	// a gate that withheld when it could not reach the projection would convert an
	// unreachable device-state into a platform-wide command stall. It is nil whenever
	// device-state is not deployed in this instance's profile, which is a supported
	// configuration, not a misconfiguration.
	Presence presence.Reader

	// HoldsPlaced counts commands withheld for an absent device, UndeliverableFailed
	// counts commands failed because their transport carries no command path at all,
	// and PresenceReadErrors counts sweep passes that could not reach the projection.
	//
	// 🔑 PresenceReadErrors IS THE ONE THAT MATTERS OPERATIONALLY. A read failure
	// fails open, so a permanently broken projection read looks exactly like a fleet
	// that is entirely present: every command dispatches, nothing is held, and the only
	// difference from a working gate is that the silent losses it exists to prevent are
	// happening again. Without this counter, the gate can be dead for months and the
	// only symptom is the absence of a symptom.
	HoldsPlaced         prometheus.Counter
	UndeliverableFailed prometheus.Counter
	PresenceReadErrors  prometheus.Counter

	// HoldsReleased counts withheld commands returned to QUEUED because their device
	// came back. Read it against HoldsPlaced: the two should track each other over a
	// fleet's duty cycle, and holds placed with nothing released is the signature of a
	// gate that has become a one-way door.
	HoldsReleased prometheus.Counter

	// StrandedObserved counts commands the stranded pass found sitting in SENT past the
	// grace horizon, StrandedRecovered counts those it re-armed (by where they landed),
	// and StrandedSkipped counts those it declined, by reason.
	//
	// 🔴🔴 StrandedSkipped IS WHAT STOPS THIS BEING A SILENT NO-OP, and it is the one of
	// the three that is easy to dismiss as noise. The pass declines far more rows than it
	// acts on — that is the design, not a defect — so without a reason-labelled count of
	// the declines, a reconciler that refuses EVERY row looks exactly like one with
	// nothing to do: observed climbs, recovered stays flat, and both readings are equally
	// consistent with "working perfectly" and "gate wired backwards".
	//
	// ⚠️ ON AN MQTT-HEAVY INSTANCE skipped{reason="transport"} IS THE DOMINANT SERIES BY
	// DESIGN. It is not a fault and must never be alerted on as one; see the gate in
	// reconcileStrandedCommand for why MQTT is excluded deliberately.
	//
	// 🔑 StrandedRecovered IS LABELLED BY WHERE THE ROW ACTUALLY LANDED, and the label is
	// the write's own report rather than this pass's inference. Re-arming normally lands a
	// command on PARKED, but one whose batch was called off meanwhile lands on CANCELLED —
	// two genuinely different events ("we gave a device another chance" versus "we cleaned
	// up a row from an abandoned batch") that a single number reports as one.
	//
	// 🔴 IT HAS TO COME FROM THE WRITE. Which branch fires depends on whether the batch is
	// cancelled at the instant of the update, which can change after the scan that selected
	// the row — so a label derived from anything read earlier would be a guess at a race.
	// An earlier version of this comment justified having no label by claiming the
	// cancelled case was "already counted on the batch-cancel path". It is not: that path
	// counts a SENT row as already_sent AT CANCEL TIME, and the row's later arrival at
	// CANCELLED was metered nowhere at all.
	//
	// Nil is tolerated on all three, like every counter above.
	StrandedObserved  prometheus.Counter
	StrandedRecovered *prometheus.CounterVec
	StrandedSkipped   *prometheus.CounterVec

	// ResponsesRefused counts device responses rejected because the device that published
	// them does not own the command they name.
	//
	// 🔴🔴 IT IS ITS OWN COUNTER BECAUSE THE SHARED RESULT VOCABULARY CANNOT EXPRESS IT.
	// ProcessorMetrics labels this message "invalid", the same bucket as an undecodable
	// payload — and the two mean opposite things. An undecodable payload is a device that
	// is broken; this is a device that works perfectly and is answering for its
	// neighbours. Left in that bucket the one signal distinguishing "a fleet has a bad
	// firmware build" from "something in this tenant is forging outcomes" is a rate an
	// operator has no way to separate.
	//
	// A standing non-zero rate here is not routine noise to be tuned away. It is either a
	// device doing something its grant should already prevent — which would mean the
	// broker's authorization is not doing what this design assumes — or the platform's own
	// dispatch putting a command on the wrong device's subject. Both are worth waking
	// someone for, and neither is visible anywhere else.
	ResponsesRefused prometheus.Counter

	// dead records a device response that could not be recorded against its command
	// (ADR-024). Nil when no dead-letter writer is configured, in which case the response
	// is dropped as it was before.
	dead *deadletter.Sink
	// area names this service on the letters it writes, read once at construction so the
	// failure path never dereferences anything.
	area string
	// ResponsesDeadLettered and ResponsesDeadLetterLost are counted apart: the second is
	// the only outcome here where a device's answer disappears with no record of it.
	ResponsesDeadLettered   prometheus.Counter
	ResponsesDeadLetterLost prometheus.Counter

	// NudgeMetrics measures the dispatch nudge — the second dispatch path, which puts a
	// freshly enqueued command in front of a dispatcher without waiting for a sweep tick.
	// Every field is nil-tolerant, like every counter above, because this struct is
	// assembled by literal in tests. See NudgeMetrics for why each one is there.
	NudgeMetrics NudgeMetrics

	// nudger is the bounded queue behind that path. Unexported and built by the
	// constructor: the queue's depth, drop policy and worker count are the design, not
	// something a caller assembling this struct by literal should be able to restate. A
	// literal-built processor has none, and NudgeDevice is nil-receiver safe so that the
	// enqueue path is a no-op rather than a panic in that configuration.
	nudger *dispatchNudger

	// reconcileCursor is where the next reconcile pass resumes its walk of the withheld
	// set. Per-pod and reset on restart, which merely restarts the walk — it is a
	// position in a scan, not state anything depends on.
	reconcileCursor uint

	// strandedCursor is the same idea for the stranded-SENT walk, and it is load-bearing
	// in a way reconcileCursor is not: most rows this pass reads are DECLINED and stay
	// just as eligible, so without a cursor the walk would re-read the same undeclinable
	// page forever and never reach the rows it can act on.
	strandedCursor model.StrandedCursor

	lifecycle core.LifecycleManager
	quit      chan struct{}
}

// NewCommandDeliveryProcessor creates a new command delivery processor.
//
// tenantDeleted gates delivery on the ADR-077 tenant lifecycle. Nil disables the gate
// (every tenant reads live), matching the resolver's own fail-open, so an unwired gate
// behaves like an unreachable authority rather than stalling every tenant's commands.
//
// presenceReader is the presence gate, and nil disables it the same way — see the field.
func NewCommandDeliveryProcessor(ms *core.Microservice, responses messaging.MessageReader,
	commands messaging.MessageWriter, callbacks core.LifecycleCallbacks,
	api model.CommandDeliveryApi, tenantDeleted func(string) bool,
	presenceReader presence.Reader, dead deadletter.Writer) *CommandDeliveryProcessor {
	cproc := &CommandDeliveryProcessor{
		Microservice:           ms,
		CommandResponsesReader: responses,
		DeviceCommandsWriter:   commands,
		Api:                    api,
		metrics:                ms.NewProcessorMetrics("response"),
		TenantDeleted:          tenantDeleted,
		Presence:               presenceReader,
		ClaimsLost: ms.NewCounterVec("command_delivery_claims_lost_total",
			"Dispatches abandoned because another dispatcher claimed the command first, by the "+
				"dispatch path that lost. \"sweep\" is the periodic pass, \"nudge\" is the dispatch "+
				"issued when a command is enqueued; the two racing for one row is expected and safe "+
				"(the claim is a compare-and-set), so read a rate on one path with none on the other "+
				"rather than the total", []string{"path"}),
		ClaimsStranded: ms.NewCounter("command_delivery_claims_stranded_total",
			"Commands left reading SENT because their publish failed and the release failed too. "+
				"On LwM2M the stranded reconciler re-arms these; on MQTT they still expire as "+
				"TIMEOUT, wrongly blaming the device", nil),
		HoldsPlaced: ms.NewCounter("command_delivery_holds_placed_total",
			"Commands withheld from dispatch because the device is authoritatively absent", nil),
		UndeliverableFailed: ms.NewCounter("command_delivery_undeliverable_total",
			"Commands failed because the device's transport carries no command path at all", nil),
		PresenceReadErrors: ms.NewCounter("command_delivery_presence_read_errors_total",
			"Sweep passes that could not read the presence projection; the gate fails OPEN, so a "+
				"standing rate here means commands are being dispatched ungated", nil),
		HoldsReleased: ms.NewCounter("command_delivery_holds_released_total",
			"Withheld commands returned to the dispatch queue because their device came back", nil),
		StrandedObserved: ms.NewCounter("command_delivery_stranded_observed_total",
			"Commands found sitting in SENT with no outcome for longer than the platform could "+
				"still have been retrying them", nil),
		StrandedRecovered: ms.NewCounterVec("command_delivery_stranded_recovered_total",
			"Stranded commands re-armed instead of expiring as TIMEOUT against a device that was "+
				"never sent them, by the status they landed on. PARKED means the command will be "+
				"delivered when its device next wakes; CANCELLED means its batch had been called "+
				"off, so the row was cleaned up rather than re-delivered", []string{"disposition"}),
		StrandedSkipped: ms.NewCounterVec("command_delivery_stranded_skipped_total",
			"Stranded commands the reconciler declined to act on, by reason. A high and steady "+
				"reason=\"transport\" rate is expected on MQTT deployments and is not a fault", []string{"reason"}),
		area: ms.FunctionalArea,
		ResponsesDeadLettered: ms.NewCounter("command_delivery_responses_dead_lettered_total",
			"Device command responses written to the dead-letter stream after every attempt to "+
				"record them failed, so an answer the device did give can be seen rather than "+
				"leaving its command looking unanswered (ADR-024).", nil),
		ResponsesDeadLetterLost: ms.NewCounter("command_delivery_responses_dead_letter_lost_total",
			"Device command responses that could be neither recorded NOR dead-lettered — the "+
				"write failed on a delivery that will not repeat, so the device's answer is gone.", nil),
		ResponsesRefused: ms.NewCounter("command_delivery_responses_refused_total",
			"Device responses rejected because the publishing device does not own the command "+
				"they name. Expected to be zero: either a device is answering for another device, "+
				"or dispatch addressed a command to the wrong one", nil),
	}

	cproc.NudgeMetrics = NudgeMetrics{
		Requested: ms.NewCounter("command_delivery_nudges_requested_total",
			"Dispatch nudges accepted onto the enqueue-time dispatch queue, one per command "+
				"created through createCommand (a fleet batch issues none)", nil),
		Dropped: ms.NewCounter("command_delivery_nudges_dropped_total",
			"Dispatch nudges discarded because the queue was full. A LATENCY signal, not an error "+
				"rate: the command still goes out on the delivery sweep, which is the net under "+
				"every nudge", nil),
		Declined: ms.NewCounterVec("command_delivery_nudges_declined_total",
			"Dispatch nudges the drain refused to act on, by reason. A high and steady "+
				"reason=\"not_sole\" rate is expected — the nudge stands down whenever a device has "+
				"more than one queued command — and is not a fault", []string{"reason"}),
		Applied: ms.NewCounter("command_delivery_nudges_applied_total",
			"Dispatch nudges that found exactly one queued command and put it through the delivery "+
				"gates. NOT a count of publishes: the presence gate may still hold or fail the "+
				"command, exactly as it would on a sweep tick", nil),
	}
	// 🔴 THE NUDGER IS BUILT HERE AND STARTED IN ExecuteStart. Api.Nudger is bound to it
	// while the service is still wiring up, so a command created before the processor
	// starts must land in a buffer rather than on a nil interface — and a processor that
	// is constructed and never started must not leak workers.
	cproc.nudger = newDispatchNudger(cproc, cproc.NudgeMetrics)

	// 🔴 EXPORT BOTH PATHS AT ZERO, BEFORE EITHER HAS LOST A CLAIM. A CounterVec gathers
	// NOTHING until a label combination is first used, and this counter is not only read
	// by an operator: the chart's dashboard drives its instance picker off
	// label_values(command_delivery_claims_lost_total, namespace), chosen precisely
	// BECAUSE it was a plain counter that exports from registration while the batch
	// counters are vectors that stay empty until the first fleet write.
	//
	// Turning it into a vector without this loop would hand it exactly the defect it was
	// picked to avoid — on any instance that has never lost a claim, which after B2's
	// stand-down is the ordinary state, the picker would name no instance at all and the
	// board would be unusable. It also makes "one path losing while the other is silent"
	// readable, since a silent path is then a zero series rather than no series.
	for _, path := range []dispatchPath{pathSweep, pathNudge} {
		if cproc.ClaimsLost != nil {
			cproc.ClaimsLost.WithLabelValues(string(path))
		}
	}

	if dead != nil {
		cproc.dead = deadletter.NewSink(dead, func(error) { incr(cproc.ResponsesDeadLetterLost, 1) })
	}

	// Create lifecycle manager.
	ipname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "command-delivery-proc")
	cproc.lifecycle = core.NewLifecycleManager(ipname, cproc, callbacks)
	return cproc
}

// deliverPendingCommands fetches the still-QUEUED commands across tenants and puts each
// through the presence gate, which either dispatches it, withholds it, or fails it.
// Per-command errors are logged and skipped so one bad command does not abort the batch.
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
	for _, batch := range groupByTenant(pending, cproc.tenantDeleted) {
		cproc.deliverTenantBatch(ctx, batch, pathSweep)
	}
}

// Nudger is the seam CreateCommand is bound to (model.CommandNudger). Wire it once the
// processor exists; nothing else in this package hands the queue out.
//
// 🔴 IT RETURNS AN EXPLICIT nil RATHER THAN THE FIELD WHEN THERE IS NO QUEUE. `return
// cproc.nudger` on a nil pointer produces a NON-NIL interface holding a nil pointer, so a
// caller's `if proc.Nudger() != nil` would read true for a processor that has no queue at
// all. Calling through it is harmless either way — NudgeDevice is nil-receiver safe — but a
// nil check that cannot detect nil is the kind of thing a later reader builds on.
func (cproc *CommandDeliveryProcessor) Nudger() model.CommandNudger {
	if cproc.nudger == nil {
		return nil
	}
	return cproc.nudger
}

// DrainDevice is the dispatch nudge's whole decision: one device, one tenant, dispatched
// now rather than on the sweep's next tick — or declined.
//
// 🔴 WHY A SECOND DISPATCH PATH IS SAFE AT ALL, AND WHERE THE SAFETY ACTUALLY LIVES. It is
// not the sweep lock: that exists to stop N replicas repeating one walk, and delivery is
// documented as single-sweeper, NOT exactly-once (see sweepLocked). It is the CLAIM.
// MarkSent is a compare-and-set that runs BEFORE the publish, so whichever of the two paths
// reaches a row second matches zero rows, declines, and counts a lost claim. That is why
// this must go through deliverTenantBatch/deliverCommand and must never grow a publish of
// its own: a second way to put a message on a device's subject would be a second way to
// actuate hardware, and the claim would no longer be standing in front of it.
//
// 🔴 IT DISPATCHES ONLY WHEN THE DEVICE HAS EXACTLY ONE QUEUED COMMAND, WHICH CLOSES THE
// REORDER BETWEEN ROWS BOTH PATHS CAN SEE. Per-device ordering is a delivery guarantee — a
// firmware update is a sequence whose order IS its meaning — and while both paths order by
// id, two dispatchers walking one backlog can still interleave BETWEEN rows: the sweep
// publishes row 1 while the nudge, holding a newer read, publishes row 2, and the device
// receives them in the wrong order with nothing in either path having done anything wrong.
// So the nudge refuses to reason about the interleaving and declines instead. What it gives
// up is a case it was never for; what it keeps is the case that matters — one command to an
// idle device, which is console dispatch and every REACT send-command.
//
// ⚠️ IT IS NOT A TOTAL ORDER GUARANTEE, AND THE GAP IS WORTH NAMING RATHER THAN IMPLYING
// OTHERWISE. The probe selects QUEUED, so a row the sweep has CLAIMED but not yet published
// is invisible to it, and a newer command's probe then reads "sole" and proceeds. Two
// interleavings follow, and only the second is reachable in practice:
//
//   - claim then a slow publish: the nudge must still do its probe, a presence read and its
//     own claim before publishing, so the sweep would have to stall between MarkSent and the
//     publish for longer than all of that. Scheduler-sized; practically closed.
//   - claim, publish FAILS, ReleaseClaim: the older row sits in SENT for the whole failed
//     publish — seconds, up to the JetStream ack timeout — and a newer command nudged inside
//     that window goes out first. This one is NEW with the nudge: before it, the sweep
//     re-read the backlog in id order and the older row went first.
//
// That residue is the same in-flight window MarkSentByToken already documents for
// double-publish, and closing it would need the claim to be visible to the probe — which is
// its own change. An older HELD or PARKED row is likewise invisible here, but it was equally
// invisible to the sweep (sweepableStatusStrings is QUEUED alone), so that is pre-existing
// rather than widened.
//
// 🔑 THE SOLE-COMMAND TEST IS THE WORKER'S OWN READ, NOT SOMETHING THE ENQUEUE DECIDED. A
// count taken inside CreateCommand would be stale by the time a worker acted on it, and
// stale in the dangerous direction: a second command enqueued in between would be invisible
// to a decision already made. Reading here means the answer describes the instant the
// dispatch happens.
//
// 🔴 IT APPLIES THE SAME GATES THE SWEEP DOES, BY CALLING THE SAME FUNCTIONS. groupByTenant
// carries the tenant lifecycle refusal — publishing is a physical actuation, so a command
// queued before an operator deleted the tenant must not fire a relay on an offboarded
// customer's hardware — and deliverTenantBatch carries the presence gate. Reimplementing
// either here would mean a gate that holds on one dispatch path and not the other, which is
// the same as not having it.
func (cproc *CommandDeliveryProcessor) DrainDevice(ctx context.Context, tenant, deviceToken string) {
	tenantCtx := core.WithTenant(ctx, tenant)
	queued, err := cproc.Api.QueuedCommandsForDevice(tenantCtx, deviceToken, model.NudgeProbeLimit)
	if err != nil {
		// Fails closed: no read, no dispatch. The sweep covers it, and a retry loop here
		// would hold a worker against an outage while the queue behind it filled.
		incrLabel(cproc.NudgeMetrics.Declined, declineReadFailed)
		log.Debug().Err(err).Str("tenant", tenant).Str("device", deviceToken).
			Msg("Could not read a device's queued commands for a dispatch nudge; the delivery sweep will.")
		return
	}
	if len(queued) == 0 {
		incrLabel(cproc.NudgeMetrics.Declined, declineNothingQueued)
		return
	}
	if len(queued) > 1 {
		incrLabel(cproc.NudgeMetrics.Declined, declineNotSole)
		return
	}
	// The tenant comes from the ROW, via groupByTenant, not from the request — the same
	// source the sweep gates on, so one lifecycle check answers for both paths.
	batches := groupByTenant(queued, cproc.tenantDeleted)
	if len(batches) == 0 {
		incrLabel(cproc.NudgeMetrics.Declined, declineTenantDeleted)
		return
	}
	for _, batch := range batches {
		cproc.deliverTenantBatch(ctx, batch, pathNudge)
	}
	incr(cproc.NudgeMetrics.Applied, 1)
}

// sweepInterval is the configured cadence, falling back to the platform default.
func (cproc *CommandDeliveryProcessor) sweepInterval() time.Duration {
	if cproc.SweepInterval > 0 {
		return cproc.SweepInterval
	}
	return config.DefaultSweepIntervalSeconds * time.Second
}

// dispatchPath names which of the two dispatch paths a delivery attempt came down.
//
// 🔴 IT IS AN EXPLICIT ARGUMENT AND NOT A CONTEXT VALUE OR A FIELD ON THE PROCESSOR. Both
// paths run concurrently in one process against one processor, so a field would be a data
// race that happened to read correctly most of the time, and a context value would make
// the label invisible at the call site — which is where a reader has to be able to see
// which path a publish belongs to. These are Prometheus label values, so the set is closed.
type dispatchPath string

const (
	// pathSweep is the periodic expiry + redelivery pass, holder of the sweep lock.
	pathSweep dispatchPath = "sweep"
	// pathNudge is the dispatch issued when a command is enqueued (see DrainDevice).
	pathNudge dispatchPath = "nudge"
)

// tenantBatch is one tenant's slice of a sweep tick.
type tenantBatch struct {
	tenant   string
	commands []*model.Command
}

// groupByTenant splits a cross-tenant read into per-tenant batches, dropping tenants that
// have been through the ADR-077 delete door. Both passes over commands share it — the
// delivery sweep and the hold reconciler — which is the point: the lifecycle gate belongs
// to every path that can put a command in front of a dispatcher, not only to the one that
// publishes.
//
// 🔴 THE ADR-077 REFUSAL HAPPENS HERE, BEFORE ANY BATCH IS BUILT, and that placement is
// deliberate: publishing is a PHYSICAL ACTUATION that happens BEFORE MarkSent, so a
// command queued before an operator deleted the tenant would otherwise fire a valve or a
// relay on an offboarded customer's hardware — and once the tenant's rows are swept, the
// actuation has already happened by the time MarkSent fails to find its row. The device
// acts, and the platform's only record is an error log about a command it can no longer
// describe. Dropping the tenant here also means no presence read is issued for it, so the
// gate never asks a deleted tenant's projection anything.
//
// 🔑 THE RECONCILER NEEDS IT JUST AS MUCH, AND THAT IS THE EASY ONE TO MISS. Releasing a
// hold is not an actuation, so it looks harmless — but it returns the row to QUEUED, and
// the sweep dispatches QUEUED. A release is an actuation one tick later.
//
// The refused rows are LEFT WHERE THEY ARE, not failed. They are about to be deleted with
// the rest of the tenant, so transitioning them would be writing into data being erased in
// order to describe the erasure; and if the purge is ever abandoned, an untouched command
// is the state an operator can reason about. It is a refusal, not a failure, so it is not
// logged either — one line per command per pass forever is how a correct refusal gets
// mistaken for an outage.
//
// Batches come back in first-seen order, which preserves PendingCommands' oldest-first
// ordering across tenants rather than handing the sweep whatever order a map iterates in.
func groupByTenant(pending []*model.Command, deleted func(string) bool) []tenantBatch {
	batches := make([]tenantBatch, 0)
	index := make(map[string]int, len(pending))
	for _, cmd := range pending {
		if deleted(cmd.TenantId) {
			continue
		}
		at, seen := index[cmd.TenantId]
		if !seen {
			at = len(batches)
			index[cmd.TenantId] = at
			batches = append(batches, tenantBatch{tenant: cmd.TenantId})
		}
		batches[at].commands = append(batches[at].commands, cmd)
	}
	return batches
}

// deliverTenantBatch reads presence once for the tenant, then applies the gate's verdict
// to each of its commands.
func (cproc *CommandDeliveryProcessor) deliverTenantBatch(ctx context.Context, batch tenantBatch,
	path dispatchPath) {
	tenantCtx := core.WithTenant(ctx, batch.tenant)
	states := cproc.presenceStates(tenantCtx, distinctDevices(batch.commands))
	for _, cmd := range batch.commands {
		// 🔴 A MISSING KEY READS AS State{Known:false}, WHICH Decide ANSWERS Dispatch.
		// That is the fail-open, and it carries three separate cases on one line: a
		// device the projection has never seen, a device whose chunk of the read failed,
		// and an instance with no gate wired at all. All three must deliver.
		switch presence.Decide(states[cmd.DeviceToken]) {
		case presence.Hold:
			cproc.holdCommand(tenantCtx, cmd)
		case presence.Undeliverable:
			cproc.failUndeliverable(tenantCtx, cmd)
		default:
			if err := cproc.deliverCommand(ctx, cmd, path); err != nil {
				log.Error().Err(err).Uint("command", cmd.ID).Str("device", cmd.DeviceToken).
					Msg("unable to deliver command")
			}
		}
	}
}

// distinctDevices reduces a batch to the devices it targets. A tenant with a hundred
// queued commands to three devices asks about three.
func distinctDevices(commands []*model.Command) []string {
	seen := make(map[string]bool, len(commands))
	tokens := make([]string, 0, len(commands))
	for _, cmd := range commands {
		if !seen[cmd.DeviceToken] {
			seen[cmd.DeviceToken] = true
			tokens = append(tokens, cmd.DeviceToken)
		}
	}
	return tokens
}

// presenceStates reads the projection for one tenant's devices, answering an empty Read
// — whose every lookup then reads as "unknown", hence Dispatch — when the gate is
// unwired.
//
// 🔴 A FAILED READ MUST NOT WITHHOLD. The gate exists to stop commands being thrown at
// devices that cannot receive them; it is not an authority on whether the platform is
// allowed to dispatch. Treating an unreachable device-state as "everything is absent"
// would convert one service's outage into a platform-wide command stall, which is a
// strictly worse failure than the silent loss the gate prevents. The counter is what
// stops that fail-open being invisible.
//
// 🔑 SO THIS SIDE DELIBERATELY DOES NOT CALL presence.Resolved. A device nobody could ask
// about and a device the projection has never seen get the same answer here — dispatch —
// and that equivalence is written down as a decision rather than left to be re-derived,
// because the OTHER caller (the hold reconciler) owes them different answers and asks
// Resolved for exactly that reason. What the partial read buys is narrower: the chunks
// that DID succeed still gate their own devices, so an outage midway through a large
// tenant un-gates one page of commands instead of all of them.
//
// The meter counts once per tenant pass, not once per failed chunk: it answers "can the
// gate see presence", and per-chunk counting would make one outage read as ten times
// worse on a large tenant than on a small one.
func (cproc *CommandDeliveryProcessor) presenceStates(ctx context.Context, devices []string) map[string]presence.State {
	if cproc.Presence == nil {
		return nil
	}
	states, err := cproc.Presence.StatesFor(ctx, devices)
	if err != nil {
		incr(cproc.PresenceReadErrors, 1)
		log.Warn().Err(err).Int("asked", len(devices)).Int("answered", len(states)).
			Msg("Could not read presence for some of this tenant's devices; those commands dispatch ungated.")
	}
	return states
}

// holdCommand withholds one command whose device is authoritatively absent.
//
// A lost race is DEBUG, not an error: it means another dispatcher claimed the row between
// the sweep's read and this write, which is the conditional update doing its job.
func (cproc *CommandDeliveryProcessor) holdCommand(ctx context.Context, cmd *model.Command) {
	held, err := cproc.Api.HoldCommand(ctx, cmd.ID)
	if err != nil {
		log.Error().Err(err).Uint("command", cmd.ID).Str("device", cmd.DeviceToken).
			Msg("unable to withhold a command for an absent device")
		return
	}
	if !held {
		log.Debug().Str("command", cmd.Token).
			Msg("A command left QUEUED between the sweep's read and its hold; another dispatcher has it.")
		return
	}
	incr(cproc.HoldsPlaced, 1)
}

// failUndeliverable ends one command whose transport cannot carry a command at all.
func (cproc *CommandDeliveryProcessor) failUndeliverable(ctx context.Context, cmd *model.Command) {
	failed, err := cproc.Api.MarkUndeliverable(ctx, cmd.ID, undeliverableReason)
	if err != nil {
		log.Error().Err(err).Uint("command", cmd.ID).Str("device", cmd.DeviceToken).
			Msg("unable to fail an undeliverable command")
		return
	}
	if !failed {
		return
	}
	incr(cproc.UndeliverableFailed, 1)
	log.Warn().Str("command", cmd.Token).Str("device", cmd.DeviceToken).
		Msg("Failed a command to a device whose transport has no command path; it was never dispatched.")
}

// undeliverableReason is what the tenant reads on the failed command.
//
// It names the PLATFORM as the cause, twice over. The device did nothing wrong, and a
// message that reads like a delivery failure sends an operator to check hardware that is
// working perfectly. Nor is it the protocol's fault — Sparkplug does define a command
// message; what is missing is our path for carrying one. "Does not support commands"
// would be a claim about the transport, and it would be false.
const undeliverableReason = "This platform has no command delivery path for the device's " +
	"transport, so the command was never dispatched."

// incr adds to an optional counter, skipping a nil one so a literal-built processor
// needs no metrics registry.
func incr(c prometheus.Counter, n float64) {
	if c != nil {
		c.Add(n)
	}
}

// incrLabel adds one to a labelled counter, tolerating an unwired vector for the same
// reason incr tolerates a nil counter: this struct is assembled by literal in tests.
func incrLabel(c *prometheus.CounterVec, label string) {
	if c != nil {
		c.WithLabelValues(label).Inc()
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
// TestConstructorWiresBothDeliveryGates is what closes that, and it is the reason this
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
func (cproc *CommandDeliveryProcessor) deliverCommand(ctx context.Context, cmd *model.Command,
	path dispatchPath) error {
	// Publish to the command's tenant subject and mark it SENT under the same
	// tenant context.
	tenantCtx := core.WithTenant(ctx, cmd.TenantId)
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
	nonce, claimed, err := cproc.Api.MarkSent(tenantCtx, cmd.ID)
	if err != nil {
		return err
	}
	if !claimed {
		incrLabel(cproc.ClaimsLost, string(path))
		log.Debug().Str("command", cmd.Token).Str("device", cmd.DeviceToken).
			Msg("Another dispatcher claimed this command first; not publishing it again.")
		return nil
	}

	// 🔴 THE ENVELOPE IS BUILT AFTER THE CLAIM BECAUSE IT CARRIES THE CLAIM'S NONCE. A
	// transport that finds the device unreachable hands the command back by naming the
	// dispatch it was given, so a request still in redelivery cannot park a row that has
	// since been re-claimed and run. Marshalling before the claim would have nothing to
	// name. (See model.Command.DispatchNonce.)
	envelope := deliveryEnvelope{
		Token:         cmd.Token,
		DeviceToken:   cmd.DeviceToken,
		Name:          cmd.Name,
		DispatchNonce: nonce,
	}
	if cmd.Payload != nil {
		raw := json.RawMessage(*cmd.Payload)
		envelope.Payload = &raw
	}
	value, err := json.Marshal(envelope)
	if err != nil {
		// The claim is already placed, so a marshal failure must undo it rather than
		// leave a row reading SENT for a command no transport will ever see.
		if _, rerr := cproc.Api.ReleaseClaim(tenantCtx, cmd.ID); rerr != nil {
			incr(cproc.ClaimsStranded, 1)
			log.Error().Err(rerr).Str("command", cmd.Token).
				Msg("Could not release a command whose envelope would not marshal.")
		}
		return err
	}
	msg := messaging.Message{
		Key:   []byte(cmd.Token),
		Value: value,
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

	// ...and the responding DEVICE from the same subject, for the same reason and with
	// the same fail-closed disposition.
	//
	// 🔴 THE SUBJECT, NEVER THE PAYLOAD. A device's signed grant permits publishing only
	// to its own response subject, so this token is one the broker has already verified;
	// the identical-looking field in the JSON would be whatever the sender typed. That
	// difference is the entire fix for a device settling another device's command, and it
	// evaporates the moment anyone reads the body instead.
	//
	// A subject we cannot parse a device out of is poison rather than a message to
	// process anonymously: MarkResponse has no anonymous mode, and inventing one here
	// would be a way back to the behaviour this replaced.
	responder, ok := messaging.ParseDeviceFromScopedSubject(msg.Subject, messaging.SubjectCommandResponses)
	if !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).Msg(fmt.Sprintf("Skipping command response with no parseable device in subject %q", msg.Subject))
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

	if _, err := cproc.Api.MarkResponse(tenantCtx, response.CommandToken, responder,
		response.Success, response.Payload, response.Error); err != nil {
		// A device answering for a command it does not own is refused, and the refusal is
		// TERMINAL, not transient: the same message would be refused on every redelivery,
		// so retrying it only burns the delivery budget. Ack it, count it as invalid, and
		// say so — the log line and the counter are the only places this is visible, and a
		// silent drop here would turn a device misbehaving (or a dispatcher misrouting)
		// into a command that merely never finishes.
		if errors.Is(err, model.ErrResponderNotCommandOwner) {
			incr(cproc.ResponsesRefused, 1)
			log.Warn().Err(err).Str("device", responder).Str("command", response.CommandToken).
				Str("correlation", msg.CorrelationID()).
				Msg("Refusing a command response from a device that does not own the command")
			_ = msg.Ack()
			done(core.ResultInvalid)
			return false
		}
		// Treat a failed persist as transient. Leave it unacked to retry until
		// the redelivery cap, then ack to give up (the device can resend and the
		// command sweep handles redelivery of the command itself).
		if msg.NumDelivered >= messaging.MaxDeliver {
			log.Error().Err(err).Str("command", response.CommandToken).Str("correlation", msg.CorrelationID()).Int("attempts", msg.NumDelivered).
				Msg("dead-lettering command response after maximum delivery attempts")
			cproc.deadLetterResponse(tenantCtx, msg, response.CommandToken, err)
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
// rows and publish them again.
//
// ⚠️ THIS COMMENT USED TO SAY deliverCommand PUBLISHES BEFORE IT MARKS SENT, AND ALSO
// THAT CLOSING THE WINDOW WOULD NEED A CLAIM AND AN INTERMEDIATE STATE. Both were true
// when written and neither is now: the claim exists, SENT is the state, and
// deliverCommand marks before it publishes (see its own comment, which explains why that
// order is not negotiable). Left as a correction rather than deleted because the residual
// below is easy to misread as the window that was closed.
//
// What remains is the OTHER direction: a claim that lands and a publish that then fails
// leaves the row SENT until ReleaseClaim returns it. Command delivery is therefore
// at-least-once, as it was before; what the sweep lock removes is the guaranteed,
// every-single-tick duplication of running N sweepers by design.
func (cproc *CommandDeliveryProcessor) sweepLocked(ctx context.Context) {
	ran, err := cproc.Api.TrySweepLock(ctx, func() error {
		count, byFromStatus, err := cproc.Api.ExpireStale(core.WithSystemContext(ctx), time.Now())
		if err != nil {
			log.Error().Err(err).Msg("command expiry sweep failed")
		}
		// Report the breakdown, not just the total. A command that lapsed out of HELD was
		// never dispatched — the device was absent for its whole TTL; one that lapsed out
		// of PARKED was dispatched and found nobody there; one that lapsed out of SENT was
		// dispatched toward a device believed live and went unanswered. Those point at
		// different parts of the system (the fleet, the platform's reachability picture,
		// the devices' firmware), and a single "expired N commands" line cannot tell an
		// operator which they have.
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

// runSweepTicker drives the expiry + delivery sweep on the configured cadence until the
// processor is asked to stop.
//
// Extracted from ExecuteStart so the cadence is OBSERVABLE. Inline, the only thing a test
// could reach was sweepInterval() itself — and an accessor returning the configured value
// while the ticker was built from the package default would have scored exactly the same,
// which is the shape where a helper is certified and its one caller is not.
func (cproc *CommandDeliveryProcessor) runSweepTicker(ctx context.Context) {
	ticker := time.NewTicker(cproc.sweepInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-cproc.quit:
			return
		case <-ticker.C:
			cproc.sweepLocked(ctx)
		}
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

	// Background expiry + delivery ticker.
	go cproc.runSweepTicker(ctx)

	// The dispatch nudge's workers. Started here rather than in the constructor so a
	// processor that is built and never started leaks none of them, and stopped in
	// ExecuteStop. Nudges enqueued before this point are already in the buffer and are
	// picked up now; nudges enqueued after ExecuteStop are dropped, which costs a sweep
	// tick of latency and nothing else.
	cproc.nudger.Start()

	// The hold-reconcile net, on its own slower ticker. It is deliberately NOT folded
	// into the sweep above: the sweep's cadence is chosen for delivery latency, and a
	// walk of the accumulated withheld set has no business running at that rate. Its own
	// ticker also means a slow reconcile pass cannot delay a delivery pass.
	go func() {
		ticker := time.NewTicker(config.HoldReconcileInterval * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-cproc.quit:
				return
			case <-ticker.C:
				cproc.reconcileHolds(ctx)
			}
		}
	}()

	// The stranded-SENT net, on its own slower ticker again. Same reasoning as the hold
	// reconciler's separate ticker, plus one of its own: a row is not eligible here until
	// it has been abandoned for StrandedSentGrace, so this pass has nothing to gain from
	// running at either of the other two cadences.
	go func() {
		ticker := time.NewTicker(config.StrandedReconcileInterval * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-cproc.quit:
				return
			case <-ticker.C:
				cproc.reconcileStranded(ctx)
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
//
// The nudge queue is stopped BEFORE quit is closed, and the ordering is the same one the
// ticker goroutines rely on in reverse: Stop waits for the workers, and a worker mid-drain
// is holding a claim it must finish releasing or publishing. Closing quit first would not
// interrupt it — nothing in the drain reads quit — it would merely make the wait happen
// with less of the processor still standing.
func (cproc *CommandDeliveryProcessor) ExecuteStop(context.Context) error {
	cproc.nudger.Stop()
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

// deadLetterResponse records a device's answer that could not be written against its
// command.
//
// 🔴 IT RUNS ONLY AT THE REDELIVERY CAP, so no redelivery follows whatever this consumer
// does; leaving the message unacked would strand it rather than buy another attempt. The
// write gets bounded in-process retries (core/deadletter), and one that still fails is
// counted as a LOSS.
//
// 🔑 THE REFUSED-RESPONSE PATH ABOVE IS NOT DEAD-LETTERED, and the distinction is the
// point of having two. A response from a device that does not own the command is not work
// the platform accepted and failed to finish — it is a claim it declined, and recording it
// under the command's tenant would file another device's message as that command's answer.
func (cproc *CommandDeliveryProcessor) deadLetterResponse(ctx context.Context, msg messaging.Message,
	command string, cause error) {
	if cproc.dead == nil {
		return
	}
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	err := cproc.dead.Write(ctx, deadletter.Envelope{
		Kind:   deadletter.KindCommandResponse,
		Reason: deadletter.ReasonExhausted,
		Source: cproc.area,
		Summary: "a device answered a command and the answer could not be recorded against " +
			"it after every attempt, so the command still looks unanswered",
		Detail:      detail,
		Attempts:    msg.NumDelivered,
		Subject:     msg.Subject,
		Sequence:    msg.StreamSeq,
		Correlation: msg.CorrelationID(),
		Reference:   command,
		OccurredAt:  time.Now().UTC(),
		Payload:     msg.Value,
	})
	if err != nil {
		// The counter moves in the sink's loss hook, not here — see deadletter.Sink.
		log.Error().Err(err).Str("command", command).
			Msg("LOST command response: it could be neither recorded nor dead-lettered.")
		return
	}
	incr(cproc.ResponsesDeadLettered, 1)
}
