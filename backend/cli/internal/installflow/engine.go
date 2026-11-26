// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Errors returned by the hub and the broker.
var (
	// ErrInvalidEvent: a payload broke its vocabulary or identifier grammar.
	ErrInvalidEvent = errors.New("installflow: invalid event")
	// ErrLifecycle: an event broke the step or prompt lifecycle (a step that
	// is not active, a second start, a child outliving its parent, ...).
	ErrLifecycle = errors.New("installflow: event out of lifecycle order")
	// ErrNotStarted: an event other than run.started came first.
	ErrNotStarted = errors.New("installflow: run not started")
	// ErrFinished: the run's summary has been sent; nothing may follow it.
	ErrFinished = errors.New("installflow: run already finished")
	// ErrDeliveryStalled: the presenter did not take control events within
	// the grace period. The hub has requested a graceful cancel, and the
	// run's summary will not report success.
	ErrDeliveryStalled = errors.New("installflow: event delivery stalled")
	// ErrSecretTooShort: AddSecret was given a value too short to redact
	// without redacting ordinary text.
	ErrSecretTooShort = errors.New("installflow: secret too short to redact")
)

// Engine runs one operation. It owns execution, ordering, safety fences,
// cancellation and the facts; presenters only render what it reports.
type Engine interface {
	Run(ctx context.Context, req Request, rt Runtime) (Summary, error)
}

// Request is what an engine is asked to do. It is never serialised into an
// event. The install engine extends it with the resolved options and whether
// each was supplied explicitly.
type Request struct {
	Operation Operation
	DryRun    bool
	// AssumeYes is --yes: the confirmations it already answers today are
	// answered without asking. It never supplies a secret.
	AssumeYes bool
	// ProgressOnly says the presenter shows progress and nothing that waits on a
	// person: the terminal UI under --yes. Such a run never reveals a secret, even one
	// it is otherwise eligible to show once (see Reveal); the summary points at where
	// the value is kept instead. The plain presenter never sets it: it prints what
	// dcctl always printed.
	ProgressOnly bool
}

// Runtime is what an engine is handed to talk to the outside world.
type Runtime struct {
	Events  Reporter
	Prompts PromptBroker
	Clock   Clock
	// Preflight runs the read-only host checks. The command layer owns the
	// doctor and injects it here; an engine that is asked to run a preflight
	// and has no runner fails rather than skipping it.
	Preflight PreflightRunner
	// Console is where the engine's human-readable output goes. The presenter
	// chooses it: the plain presenter passes the process's own streams, and a
	// presenter that draws its own screen passes writers that feed the hub's
	// log ring. The engine never writes to os.Stdout or os.Stderr itself.
	Console Console
	// ObserveTools asks the engine to run a tool in its machine-readable mode where
	// that is the only way to observe its progress (OpenTofu's apply -json), and to
	// report what it observes as measured progress.
	//
	// 🔴 IT CHANGES THE TOOL'S OWN TEXT ON Console. In that mode the console gets
	// the lines the engine renders from the tool's messages, not the tool's human
	// output, so a presenter that must reproduce today's bytes (the plain one)
	// leaves it off. Readiness gates are reported either way: they change nothing
	// a presenter prints.
	ObserveTools bool
	// Admission, when set, admits this run only once the previous run it
	// admitted has settled (see Admission). Hub.Run applies it and sets
	// Outlive; a presenter that offers a re-run in the same process sets it.
	Admission *Admission
	// Reveals receives the run's one-time disclosures (see Reveal). Nil means the
	// presenter can show none, and the engine then offers none: the value stays where
	// it is kept, and only a reference to it is reported.
	Reveals RevealSink
	// Outlive registers work the run is leaving behind when it returns — a
	// tool it stopped waiting for — and returns the function that says that
	// work has ended. Nil when nothing gates a re-run on it.
	Outlive func() (ended func())
}

// Console is the pair of writers an engine's human-readable output goes to,
// handed by the presenter to every part of the engine that prints, rather than
// reached through the process's global streams.
type Console struct {
	Stdout io.Writer
	Stderr io.Writer
	// Flush waits for previously emitted events to be rendered, when the
	// presenter renders asynchronously.
	Flush func()
	// ChildStreams optionally hands a subprocess its streams after flushing.
	// The plain terminal presenter uses this to preserve inherited file
	// descriptors; other consoles keep using the writers above.
	ChildStreams func() (io.Writer, io.Writer)
}

// Reporter is the engine's side of the hub.
type Reporter interface {
	// Emit sends one event. Logs and progress never block; a control event
	// blocks at most for the hub's grace period.
	Emit(Event) error
	// Finish sends the one run.summary. A second call is ErrFinished.
	Finish(Summary) error
	// AddSecret registers a value to redact from every text the hub carries.
	AddSecret(value string) error
	// LogWriter returns a writer that turns tool output into log lines. Its
	// Write never blocks on a presenter and never fails.
	LogWriter(step StepID, source string, level LogLevel) (io.WriteCloser, error)
}

// Event is what an engine emits. The kind comes from the payload.
type Event struct {
	Step    StepID
	Parent  StepID
	Attempt int
	Payload Payload
}

// Clock is the time source. Tests inject a manual one.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a scheduled callback.
type Timer interface{ Stop() bool }

// SystemClock is the real clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time                            { return time.Now() }
func (SystemClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Run executes e against this hub and guarantees the stream ends with exactly
// one run.summary: if the engine returns without finishing, its returned
// summary is sent (an error never closes as success); if it panics, a failed
// summary is sent and the panic continues. It returns the summary the hub
// actually sent.
//
// rt supplies the presenter's parts of the runtime (prompts, console,
// preflight); its Events and Clock are always this hub's.
//
// With rt.Admission set the engine runs only if the previous run has settled;
// otherwise the run is refused with ErrNotSettled and a failed summary, before
// anything starts. The run counts as settled once e.Run has returned and every
// tool it outlived (rt.Outlive) has ended — not when its summary is sent.
func (h *Hub) Run(ctx context.Context, e Engine, req Request, rt Runtime) (sum Summary, err error) {
	if req.Operation == "" {
		req.Operation = h.op
	}
	rt.Events, rt.Clock = h, h.clock
	if rt.Admission != nil {
		ticket, aerr := rt.Admission.Admit()
		if aerr != nil {
			_ = h.Finish(Summary{Outcome: RunFailed, InstallRecord: RecordNotRead,
				Readiness: ReadinessNotChecked, Claim: ClaimNotClaimed, PartialState: EffectNoneObserved})
			final, _ := h.Final()
			return final, aerr
		}
		// Registered first, so it runs last: after the panic path below has
		// sent its summary, and only once e.Run has returned.
		defer ticket.Done()
		rt.Outlive = ticket.Outlive
	}
	defer func() {
		if p := recover(); p != nil {
			_ = h.Finish(Summary{
				Outcome: RunFailed, InstallRecord: RecordUnknown, Readiness: ReadinessUnknown,
				Claim: ClaimUnknown, PartialState: EffectMayHaveChanged,
			})
			panic(p)
		}
	}()
	sum, err = e.Run(ctx, req, rt)
	if _, done := h.Final(); !done {
		if err != nil && sum.Succeeded() {
			sum.Outcome = RunFailed
		}
		if ferr := h.Finish(sum); ferr != nil && err == nil {
			err = fmt.Errorf("closing the run: %w", ferr)
		}
	}
	final, _ := h.Final()
	return final, err
}
