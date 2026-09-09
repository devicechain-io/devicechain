// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// DeliveryMetrics is every Prometheus instrument the command delivery processor exports.
//
// It is a type of its own so it can be built in a DIFFERENT PHASE from the processor
// that reads it, and it is embedded by value there so every reader keeps its original
// spelling. See NewDeliveryMetrics.
//
// Every field is nil-tolerant, and that has not changed: a processor assembled by
// literal — which is how the tests in this package build it — gets a zero
// DeliveryMetrics whose instruments are all nil, exactly as before they lived here.
type DeliveryMetrics struct {
	// RED metrics for the response-consumer path (E13).
	metrics *core.ProcessorMetrics

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

	// DispatchesExhausted counts commands the release path stopped retrying, having failed
	// to publish them as many times as the configured bound allows.
	//
	// 🔴 IT IS THE ONLY THING THAT MAKES THIS FAILURE VISIBLE WHILE IT IS HAPPENING. The
	// behaviour it replaced was silent by construction: a command nothing could publish was
	// claimed, failed and re-queued twice a minute for a week, and the only trace was a
	// TIMEOUT at the end of it that read as an unanswered device rather than as a platform
	// that never sent anything. A rate here says the opposite, and says it in minutes.
	//
	// Read it against the publish errors beside it. A few exhaustions with no broader
	// error rate are poison commands — an oversized payload, a device whose stream is gone
	// — and each row's error column says so. A spike across many tenants at once is the
	// messaging layer, and these commands are collateral: they will need re-issuing once it
	// is back, because the bound is deliberately shorter than the TTL.
	//
	// 🔑 THIS COUNTER AND THE ROW ARE THE WHOLE RECORD — NO DEAD LETTER IS WRITTEN, AND
	// THAT IS A DECISION RATHER THAN AN OVERSIGHT. A dead letter is the durable trace for
	// work whose ORIGINAL is a message that will age off the stream, which is why every
	// existing kind names something living elsewhere. Here the original is a ROW that
	// survives, carrying the terminal status, the failure count and a reason a tenant can
	// read, already queryable through the ordinary command search. Adding a kind to the
	// platform's closed give-up vocabulary — a metric label, a query filter, and the
	// operator-facing list of what the platform gave up on — to index a record that is
	// already indexed would put two entries in front of an operator for one event.
	//
	// The near-miss is worth naming so nobody adds one casually: the only reason that
	// could apply is the exhausted one, which is exactly the reason the dead-letter
	// write-back acts on to drive a command to FAILED. A letter about a command the
	// platform has ALREADY driven to FAILED would be a second consumer settling a row that
	// is settled, filtered apart today only by its kind.
	//
	// Nil is tolerated (skipped) like every counter here.
	DispatchesExhausted prometheus.Counter

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

	// ResponsesNotAnswerable counts device responses that named the dispatch their command
	// is still on, and could nevertheless not settle it.
	//
	// 🔴 IT IS THE RESIDUAL'S COUNTER, AND IT SHOULD READ ZERO FOREVER. The refusal behind
	// it is unreachable in today's status vocabulary — see ErrCommandNotAnswerable — so any
	// movement means a state was added without anyone deciding whether an answer may settle
	// it. That is worth a series precisely because the alternative to noticing is a fleet's
	// answers piling up in the dead-letter stream with nothing pointing at the cause.
	//
	// 🔑 IT USED TO COUNT THE RELEASED-THEN-ANSWERED CASE, WHICH HAS MOVED. That answer now
	// SETTLES its command when it names the dispatch that was released, and is counted on
	// ResponsesStaleNonce when it names one the command has moved off. An alert or dashboard
	// carried over from the old meaning is watching the wrong series.
	//
	// 🔴🔴 IT IS A PLAIN COUNTER, NOT A VECTOR, AND THAT IS THE POINT OF IT. A CounterVec
	// gathers nothing until a label combination is first used, so on an instance where this
	// has never happened there would be no series at all — and a rule alerting on it could
	// not fire, on exactly the instances where the first occurrence is the thing worth
	// hearing about. A plain counter is registered at construction and exports 0 from the
	// first scrape, so "this has never happened" and "nothing is reporting" stay tellable
	// apart. Same reasoning ClaimsLost's zero-warm loop below is written for.
	//
	// 🔑 IT IS NOT ResponsesRefused, and folding the two would lose the distinction that
	// decides what to do about it. A refused response is a device answering for SOMEBODY
	// ELSE'S command — an identity problem. This is the right device giving a real answer
	// the platform has no live row to put it against, which is a DELIVERY problem: the
	// ordinary way to reach it is a publish that reported an error, after which the command
	// is returned to the queue while the device may already have run it.
	ResponsesNotAnswerable prometheus.Counter

	// ResponsesWithoutNonce counts device answers refused because they named no dispatch,
	// and ResponsesStaleNonce counts those refused because they named a dispatch their
	// command had already moved off.
	//
	// 🔴🔴 ResponsesWithoutNonce IS THE EVIDENCE THAT THE REFUSAL IS SAFE TO HAVE SWITCHED
	// ON, AND IT IS THE ONLY EVIDENCE THERE IS. Requiring the nonce breaks any client that
	// does not echo it — every client in this repository does, but a device built against
	// an older contract does not, and nothing in the platform can enumerate those. This
	// counter is what turns that unknown into a reading: zero means every device answering
	// this instance speaks the current contract, and a non-zero rate names a fleet whose
	// commands are no longer being settled. It is a plain counter for the reason
	// ResponsesNotAnswerable is one — it must export 0 from the first scrape, so "this has
	// never happened" and "nothing is reporting" stay tellable apart.
	//
	// 🔑 ResponsesStaleNonce MEASURES THE DEFECT ITSELF RATHER THAN A MIGRATION. A device
	// only ever quotes a nonce the platform gave it, so a mismatch means the command was
	// dispatched more than once: the first publish reported an error, the command was
	// returned to the queue and issued again, and the device's answer to the first arrived
	// afterwards. Before the nonce that answer settled the SECOND dispatch and nothing
	// recorded it; this is that event finally having a number.
	ResponsesWithoutNonce prometheus.Counter
	ResponsesStaleNonce   prometheus.Counter

	// ResponsesDeadLettered and ResponsesDeadLetterLost are counted apart: the second is
	// the only outcome here where a device's answer disappears with no record of it.
	ResponsesDeadLettered   prometheus.Counter
	ResponsesDeadLetterLost prometheus.Counter

	// NudgeMetrics measures the dispatch nudge — the second dispatch path, which puts a
	// freshly enqueued command in front of a dispatcher without waiting for a sweep tick.
	// Every field is nil-tolerant, like every counter above, because this struct is
	// assembled by literal in tests. See NudgeMetrics for why each one is there.
	NudgeMetrics NudgeMetrics
}

// NewDeliveryMetrics builds the command delivery processor's instruments.
//
// 🔴 CALL IT FROM THE INITIALIZE PHASE, WHICH RUNS ONCE. The processor is built inside
// the NATS manager's oncreate callback — it has to be, because it holds a reader bound
// to the connection — and that callback runs on EVERY start. The two dozen collectors
// below, built there, would be registered again whenever that callback is entered
// again — which any start retried after a failed one does — and MustRegister panics on
// the first duplicate.
func NewDeliveryMetrics(ms *core.Microservice) DeliveryMetrics {
	return DeliveryMetrics{
		metrics: ms.NewProcessorMetrics("response"),
		ClaimsLost: ms.NewCounterVec("command_delivery_claims_lost_total",
			"Dispatches abandoned because another dispatcher claimed the command first, by the "+
				"dispatch path that lost. \"sweep\" is the periodic pass, \"nudge\" is the dispatch "+
				"issued when a command is enqueued; the two racing for one row is expected and safe "+
				"(the claim is a compare-and-set), so read a rate on one path with none on the other "+
				"rather than the total", []string{"path"}),
		ClaimsStranded: ms.NewCounter("command_delivery_claims_stranded_total",
			"Commands left reading SENT because their publish failed and the release failed too. "+
				"On LwM2M the stranded reconciler re-arms these; on MQTT they still expire as "+
				"TIMEOUT, wrongly blaming the device"),
		DispatchesExhausted: ms.NewCounter("command_delivery_dispatches_exhausted_total",
			"Commands failed because the platform could not publish them to their device as many "+
				"times as the configured bound allows, so it stopped retrying. Each row records "+
				"FAILED with the platform named as the cause, rather than retrying until its TTL "+
				"and then recording TIMEOUT against a device it never reached"),
		HoldsPlaced: ms.NewCounter("command_delivery_holds_placed_total",
			"Commands withheld from dispatch because the device is authoritatively absent"),
		UndeliverableFailed: ms.NewCounter("command_delivery_undeliverable_total",
			"Commands failed because the device's transport carries no command path at all"),
		PresenceReadErrors: ms.NewCounter("command_delivery_presence_read_errors_total",
			"Sweep passes that could not read the presence projection; the gate fails OPEN, so a "+
				"standing rate here means commands are being dispatched ungated"),
		HoldsReleased: ms.NewCounter("command_delivery_holds_released_total",
			"Withheld commands returned to the dispatch queue because their device came back"),
		StrandedObserved: ms.NewCounter("command_delivery_stranded_observed_total",
			"Commands found sitting in SENT with no outcome for longer than the platform could "+
				"still have been retrying them"),
		StrandedRecovered: ms.NewCounterVec("command_delivery_stranded_recovered_total",
			"Stranded commands re-armed instead of expiring as TIMEOUT against a device that was "+
				"never sent them, by the status they landed on. PARKED means the command will be "+
				"delivered when its device next wakes; CANCELLED means its batch had been called "+
				"off, so the row was cleaned up rather than re-delivered", []string{"disposition"}),
		StrandedSkipped: ms.NewCounterVec("command_delivery_stranded_skipped_total",
			"Stranded commands the reconciler declined to act on, by reason. A high and steady "+
				"reason=\"transport\" rate is expected on MQTT deployments and is not a fault", []string{"reason"}),
		ResponsesDeadLettered: ms.NewCounter("command_delivery_responses_dead_lettered_total",
			"Device command responses written to the dead-letter stream after every attempt to "+
				"record them failed, so an answer the device did give can be seen rather than "+
				"leaving its command looking unanswered (ADR-024)."),
		ResponsesDeadLetterLost: ms.NewCounter("command_delivery_responses_dead_letter_lost_total",
			"Device command responses that could be neither recorded NOR dead-lettered — the "+
				"write failed on a delivery that will not repeat, so the device's answer is gone."),
		ResponsesRefused: ms.NewCounter("command_delivery_responses_refused_total",
			"Device responses rejected because the publishing device does not own the command "+
				"they name. Expected to be zero: either a device is answering for another device, "+
				"or dispatch addressed a command to the wrong one"),
		ResponsesNotAnswerable: ms.NewCounter("command_delivery_responses_not_answerable_total",
			"Answers that named the dispatch their command is still on and could nevertheless "+
				"not settle it. Expected to stay at zero: no command state can produce this "+
				"today, so any movement means a state was added to the command lifecycle without "+
				"a decision about whether a device's answer may settle it. Each one is written "+
				"to the dead-letter stream rather than dropped"),
		ResponsesWithoutNonce: ms.NewCounter("command_delivery_responses_without_nonce_total",
			"Device answers refused because they did not name the dispatch they were answering. "+
				"Expected to be zero: every client the platform ships echoes the dispatch nonce "+
				"from the delivery envelope, so a rate here names devices built against an older "+
				"contract whose commands are no longer being settled. Each one is written to the "+
				"dead-letter stream rather than dropped"),
		ResponsesStaleNonce: ms.NewCounter("command_delivery_responses_stale_nonce_total",
			"Device answers refused because they named a dispatch their command had already moved "+
				"off. A device only quotes a nonce the platform gave it, so a rate here means "+
				"commands are being dispatched more than once — a publish reported an error, the "+
				"command was requeued and issued again, and the answer to the first dispatch "+
				"arrived after the second went out"),
		NudgeMetrics: NudgeMetrics{
			Requested: ms.NewCounter("command_delivery_nudges_requested_total",
				"Dispatch nudges accepted onto the enqueue-time dispatch queue, one per command "+
					"created through createCommand (a fleet batch issues none)"),
			Dropped: ms.NewCounter("command_delivery_nudges_dropped_total",
				"Dispatch nudges discarded because the queue was full. A LATENCY signal, not an error "+
					"rate: the command still goes out on the delivery sweep, which is the net under "+
					"every nudge"),
			Declined: ms.NewCounterVec("command_delivery_nudges_declined_total",
				"Dispatch nudges the drain refused to act on, by reason. A high and steady "+
					"reason=\"not_sole\" rate is expected — the nudge stands down whenever a device has "+
					"more than one queued command — and is not a fault", []string{"reason"}),
			Applied: ms.NewCounter("command_delivery_nudges_applied_total",
				"Dispatch nudges that found exactly one queued command and put it through the delivery "+
					"gates. NOT a count of publishes: the presence gate may still hold or fail the "+
					"command, exactly as it would on a sweep tick"),
		},
	}
}
