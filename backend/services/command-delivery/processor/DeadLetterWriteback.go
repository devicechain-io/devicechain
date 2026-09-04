// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// writebackReadErrorBackoff paces a retry after a read failure, so a broker that is
// briefly away does not spin this loop.
const writebackReadErrorBackoff = time.Second

// CommandDispositionWriter is the one write this consumer performs. It is declared here,
// at the point of use, rather than added to CommandDeliveryApi: the write-back needs
// exactly one method, and a narrow interface is what lets its dispositions be driven from
// a test without a database.
type CommandDispositionWriter interface {
	// MarkResponseLost drives a command to its terminal FAILED state because the
	// device's answer to it was dead-lettered, reporting whether THIS call moved it.
	MarkResponseLost(ctx context.Context, token string) (bool, error)
}

// DeadLetterWriteback closes the loop between a dead letter and the record it is about.
//
// 🔴 THE GAP IT EXISTS FOR IS A RECORD THAT GOES QUIET AND THEN LIES. When a device's
// answer to a command exhausts its redelivery cap, CommandDeliveryProcessor writes a dead
// letter and acks — the correct disposition for the MESSAGE, and it leaves the COMMAND
// reading SENT with nothing left that knows better. Nothing moves it back: a redelivery
// will not come, the stranded-SENT reconciler only parks rows for a queue-mode transport,
// and a cancel does not accept SENT.
//
// 🔑 IT IS NOT "FOREVER", AND SAYING SO WOULD BE THE FIRST THING A READER CHECKED. Expiry
// does eventually reach the row: ApplyDefaults floors DefaultCommandTTLSeconds positive,
// and both enqueue paths stamp it when a caller supplies none, so a production command
// always has a horizon and a stuck one lapses at it. What that leaves is worse than a
// stall rather than better. Until the TTL elapses — the platform default is measured in
// DAYS — the command reads as in flight while the device has already answered; and when it
// does lapse it lands on TIMEOUT, which means "dispatched, and never answered". So the
// record's final word blames hardware that replied, and the answer it gave sits in a dead
// letter three services away. That mis-attribution is the same one PARKED was introduced
// to remove, arrived at down a different path.
//
// 🔑 IT GOES THROUGH THE BROKER RATHER THAN WRITING THE DISPOSITION AT THE DEAD-LETTER
// CALL SITE, and that is not indirection for its own sake. The reason the answer could
// not be recorded is, in the ordinary case, that the DATABASE refused the write — so a
// second database write issued from inside that same failure handler fails for the same
// reason, in the same instant, with no attempts left. Publishing the dead letter and
// consuming it back gives the write-back its OWN delivery budget, minutes later, against
// a database that has had time to come back.
//
// It reads the platform dead-letter stream, which user-management's store also consumes.
// Two durables over one stream is the ordinary JetStream arrangement (the stream is
// limits-retention, and DurableName is scoped per functional area), so each gets every
// letter and neither can consume the other's.
type DeadLetterWriteback struct {
	Microservice *core.Microservice
	reader       messaging.MessageReader
	api          CommandDispositionWriter

	// settled counts commands this consumer drove to a terminal state.
	settled prometheus.Counter
	// alreadyFinished counts letters whose command had already reached a terminal state
	// some other way. It is a normal outcome, not an error: the write is predicated on
	// the states a response could still settle, so a late letter is a no-op by design.
	alreadyFinished prometheus.Counter
	// notOurs counts letters for other kinds of work. Every producer shares one stream,
	// so most of what arrives here belongs to somebody else.
	notOurs prometheus.Counter
	// unreadable counts letters this consumer cannot act on at all — no parseable tenant,
	// a body that is not an envelope, or an envelope naming no command. They are ACKED,
	// because no redelivery makes a malformed message parse.
	unreadable prometheus.Counter
	// stranded counts letters that exhausted every delivery attempt without the
	// disposition being written. Those commands keep the wrong story — in flight until
	// their TTL, then TIMEOUT, blaming a device that answered — which is exactly what this
	// consumer exists to correct, so it is counted apart from every other failure here.
	stranded prometheus.Counter

	procCtx    context.Context
	procCancel context.CancelFunc
	wg         sync.WaitGroup

	lifecycle core.LifecycleManager
}

// NewDeadLetterWriteback builds the write-back over the dead-letter reader and the
// command API.
//
// 🔴 IT REFUSES A NIL DEPENDENCY RATHER THAN TOLERATING ONE. A nil-tolerant handler here
// would turn a wiring mistake into a consumer that reads every letter, writes nothing,
// and reports no error — permanently and silently, on the path whose entire job is to
// stop a record going quiet. The caller builds this inside the NatsManager's create
// callback, where the reader exists; anything wrong there must stop the service starting.
func NewDeadLetterWriteback(ms *core.Microservice, reader messaging.MessageReader,
	api CommandDispositionWriter, callbacks core.LifecycleCallbacks) (*DeadLetterWriteback, error) {
	if ms == nil {
		return nil, errors.New("dead-letter write-back needs a microservice")
	}
	if reader == nil {
		return nil, errors.New("dead-letter write-back needs a dead-letter reader; without one " +
			"every command whose response was lost keeps the wrong outcome")
	}
	if api == nil {
		return nil, errors.New("dead-letter write-back needs the command API; without one it " +
			"would read every letter and write nothing")
	}
	w := &DeadLetterWriteback{
		Microservice: ms,
		reader:       reader,
		api:          api,
		settled: ms.NewCounter("command_response_lost_settled_total",
			"Commands driven to a terminal state because the device's answer to them was "+
				"dead-lettered, so a command whose response the platform lost stops reading "+
				"as though it were still in flight.", nil),
		alreadyFinished: ms.NewCounter("command_response_lost_already_finished_total",
			"Dead-lettered responses whose command had already reached a terminal state some "+
				"other way. Nothing is written: a late dead letter must not overwrite an "+
				"outcome that really happened.", nil),
		notOurs: ms.NewCounter("command_dead_letters_not_ours_total",
			"Dead letters read from the shared stream that describe some other kind of work. "+
				"Acked and ignored — every producer writes to one stream.", nil),
		unreadable: ms.NewCounter("command_dead_letters_unreadable_total",
			"Dead letters this consumer could not act on — no parseable tenant, a body that is "+
				"not an envelope, or an envelope naming no command. Acked and counted rather "+
				"than retried, because no redelivery makes a malformed message parse.", nil),
		stranded: ms.NewCounter("command_response_lost_unsettled_total",
			"Dead-lettered responses that exhausted every delivery attempt without their "+
				"command's disposition being written. Those commands read as in flight until "+
				"their TTL and then lapse to TIMEOUT, blaming a device that did answer.", nil),
	}
	w.lifecycle = core.NewLifecycleManager(
		fmt.Sprintf("%s-%s", ms.FunctionalArea, "dead-letter-writeback"), w, callbacks)
	return w, nil
}

func (w *DeadLetterWriteback) Initialize(ctx context.Context) error {
	return w.lifecycle.Initialize(ctx)
}

func (w *DeadLetterWriteback) ExecuteInitialize(context.Context) error {
	w.procCtx, w.procCancel = context.WithCancel(context.Background())
	return nil
}

func (w *DeadLetterWriteback) Start(ctx context.Context) error { return w.lifecycle.Start(ctx) }

func (w *DeadLetterWriteback) ExecuteStart(context.Context) error {
	w.wg.Add(1)
	go w.loop()
	return nil
}

func (w *DeadLetterWriteback) Stop(ctx context.Context) error { return w.lifecycle.Stop(ctx) }

func (w *DeadLetterWriteback) ExecuteStop(context.Context) error {
	if w.procCancel != nil {
		w.procCancel()
	}
	w.wg.Wait()
	return nil
}

func (w *DeadLetterWriteback) Terminate(ctx context.Context) error {
	return w.lifecycle.Terminate(ctx)
}

func (w *DeadLetterWriteback) ExecuteTerminate(context.Context) error { return nil }

// loop reads until shutdown.
func (w *DeadLetterWriteback) loop() {
	defer w.wg.Done()
	for {
		select {
		case <-w.procCtx.Done():
			return
		default:
		}
		msg, err := w.reader.ReadMessage(w.procCtx)
		if err != nil {
			if errors.Is(err, io.EOF) || w.procCtx.Err() != nil {
				return
			}
			w.reader.HandleResponse(err)
			w.pause()
			continue
		}
		w.Handle(msg)
	}
}

func (w *DeadLetterWriteback) pause() {
	select {
	case <-time.After(writebackReadErrorBackoff):
	case <-w.procCtx.Done():
	}
}

// Handle applies one letter's disposition to the command it is about. Exported so the
// dispositions can be driven directly from a test without a broker — the loop above adds
// nothing to them but a read.
//
// The dispositions, and the asymmetry between them:
//
//   - A letter that is not about a command response, or that cannot be made sense of, is
//     ACKED. No redelivery turns somebody else's work into ours or makes a malformed body
//     parse; leaving it unacked would only hold this durable behind it.
//   - A write that FAILS below the redelivery cap is left unacked, which buys the next
//     delivery attempt. That is a database saying no, and this consumer's whole value is
//     that it does not give up on the first refusal.
//   - A write that fails ON THE FINAL ATTEMPT leaves the command reading in flight
//     until its TTL and then lapses to TIMEOUT. It is counted and logged as the loss it
//     is, and acked, because after the
//     cap no redelivery follows and pretending otherwise is worse than saying so.
//   - A tenant whose data has been ERASED is terminal on the first look. See below.
func (w *DeadLetterWriteback) Handle(msg messaging.Message) {
	tenantCtx, tenant, ok := messaging.TenantContextFromSubject(w.procCtx, msg.Subject)
	if !ok {
		log.Warn().Str("subject", msg.Subject).
			Msg("Dropping a dead letter with no parseable tenant in its subject; its command " +
				"cannot be found without one.")
		w.unreadable.Inc()
		w.ack(msg)
		return
	}
	e, err := deadletter.Unmarshal(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("subject", msg.Subject).
			Msg("Dropping a dead letter whose body is not an envelope.")
		w.unreadable.Inc()
		w.ack(msg)
		return
	}
	// 🔑 FILTERED ON KIND, NOT ON SOURCE. Kind names the WORK — a device's reply that
	// could not be recorded against its command — and that is what decides whether a
	// command record needs settling. Source names the service that gave up, which
	// core/deadletter records precisely because the two can diverge: a kind can move
	// service, and a dead letter outlives the deploy that wrote it. Matching on the area
	// name would silently stop settling commands the day this work moved or the area was
	// renamed, and the symptom would be the original defect returning.
	if e.Kind != deadletter.KindCommandResponse {
		w.notOurs.Inc()
		w.ack(msg)
		return
	}
	if e.Reference == "" {
		log.Warn().Str("subject", msg.Subject).
			Msg("Dropping a dead-lettered command response that names no command; there is " +
				"nothing to settle.")
		w.unreadable.Inc()
		w.ack(msg)
		return
	}
	// The tenant comes from the SUBJECT, which is what scoped the letter, and it has to:
	// the commands table is tenant-scoped, so a write without a tenant in context is
	// refused by the scope callback rather than running unscoped.
	settled, err := w.api.MarkResponseLost(tenantCtx, e.Reference)
	if err != nil {
		// 🔴 A PURGED TENANT IS TERMINAL, NOT TRANSIENT. Deleting a tenant erases its
		// rows and fences further writes, so this update will be refused on every
		// remaining attempt and on every one after that. Spending the cap on it would
		// bury the letters behind it, and the "stranded" alert would fire for a command
		// that no longer exists — an operator paged to look for a record that was
		// deliberately destroyed. There is no command left to settle, so ack and move on.
		if errors.Is(err, rdb.ErrTenantPurged) {
			log.Info().Str("tenant", tenant).Str("command", e.Reference).
				Msg("Skipping a dead-lettered command response for a deleted tenant; its " +
					"commands have been erased, so there is no record to settle.")
			w.notOurs.Inc()
			w.ack(msg)
			return
		}
		if msg.NumDelivered >= messaging.MaxDeliver {
			// No redelivery follows. Said plainly rather than logged as a retry.
			log.Error().Err(err).Str("tenant", tenant).Str("command", e.Reference).
				Int("attempts", msg.NumDelivered).
				Msg("STRANDED a command: its response was dead-lettered and every attempt to " +
					"record that on the command failed; it will read as in flight until its TTL " +
					"and then lapse to TIMEOUT, blaming a device that answered.")
			w.stranded.Inc()
			w.ack(msg)
			return
		}
		log.Warn().Err(err).Str("tenant", tenant).Str("command", e.Reference).
			Int("attempts", msg.NumDelivered).
			Msg("Could not settle a command whose response was dead-lettered; leaving the " +
				"letter unacked for another delivery attempt.")
		return
	}
	if settled {
		log.Warn().Str("tenant", tenant).Str("command", e.Reference).
			Msg("Settled a command whose device response the platform could not record: it " +
				"is FAILED, with the reason on the record.")
		w.settled.Inc()
	} else {
		w.alreadyFinished.Inc()
	}
	w.ack(msg)
}

// ack best-effort acks. A failed ack redelivers, and the write is idempotent — the
// second pass finds the command already terminal and writes nothing.
func (w *DeadLetterWriteback) ack(msg messaging.Message) {
	if err := msg.Ack(); err != nil {
		log.Warn().Err(err).Msg("Failed to ack a dead letter; a redelivery settles it idempotently.")
	}
}
