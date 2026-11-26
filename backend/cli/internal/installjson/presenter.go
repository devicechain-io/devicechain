// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package installjson is the NDJSON presenter of a dcctl install
// (`dcctl install --output json`): every event of the run, one JSON object per
// line, on stdout, and nothing else there.
//
// It is for automation, so three rules hold whatever the run does:
//
//   - STRICT STDOUT. The presenter's Stdout receives envelopes and nothing else:
//     each event is marshalled whole and written with its newline in one write,
//     from the hub's one delivery goroutine. The engine's human-readable output
//     never reaches it raw. It is taken as log.line events instead: split into
//     lines, stripped of terminal escapes, passed through known-value redaction
//     and carried inside envelopes like any other event.
//   - NON-INTERACTIVE. Nothing is read from stdin, ever. A confirmation --yes
//     answers is answered by it; any other prompt resolves as unavailable, and
//     the engine refuses the run with the documented code
//     installflow.ErrorPromptUnavailable.
//   - ONE VERDICT, LAST. The stream ends with exactly one run.summary, even for
//     a command refused before its run could start (Refuse), and Run returns
//     only once that line has been written.
//
// The exit status is unchanged: 0 for a run that succeeded or was planned (a
// dry run), 1 otherwise. A stream that could not be written in full never exits
// 0.
//
// It imports no terminal library and never touches a terminal.
package installjson

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"sync"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// Log sources of the engine's console output in the stream.
const (
	SourceConsole       = "dcctl"
	SourceConsoleStderr = "dcctl.stderr"
)

// Presenter runs an engine and writes its events to Stdout as NDJSON.
type Presenter struct {
	// Stdout is the stream. Nothing but envelopes is ever written to it.
	Stdout io.Writer
	// RunID fixes the run id; empty is random. Tests fix it.
	RunID string
	// Clock fixes the time source; nil is the system clock. Tests fix it.
	Clock installflow.Clock
}

// errIncomplete: the engine reported no error, yet the run did not close as a
// success, so the presenter could not be kept up to date and nothing may claim
// the run finished.
var errIncomplete = errors.New("the run's progress could not be reported, so it is not known to have finished")

// Run executes e and streams its events. It returns the engine's own error, so
// the command prints the same reason on stderr that it always has, and it
// returns only once the run's summary has been written.
func (p Presenter) Run(ctx context.Context, e installflow.Engine, req installflow.Request,
	preflight installflow.PreflightRunner) error {
	err, werr := p.run(ctx, e, req, preflight)
	if werr != nil && err == nil {
		err = werr
	}
	return err
}

// run is Run with the stream's own write failure returned apart from the
// engine's error.
func (p Presenter) run(ctx context.Context, e installflow.Engine, req installflow.Request,
	preflight installflow.PreflightRunner) (err, werr error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	sink := &lineSink{w: p.Stdout}
	hub, err := installflow.NewHub(installflow.Options{
		Operation: req.Operation, RunID: p.RunID, Clock: p.Clock, Sink: sink,
		// A consumer that cannot keep up stops the run gracefully, saying why; the
		// hub then refuses to report it as a success.
		OnDeliveryFailure: func() {
			cancel(installflow.CancelRequest{Reason: installflow.CancelDeliveryFailure})
		},
	})
	if err != nil {
		return err, nil
	}
	stdout, err := hub.LogWriter("", SourceConsole, installflow.LogInfo)
	if err != nil {
		return err, nil
	}
	stderr, err := hub.LogWriter("", SourceConsoleStderr, installflow.LogInfo)
	if err != nil {
		return err, nil
	}
	// No ChildStreams: a tool the engine runs writes into the same log writers,
	// never onto an inherited terminal or the process's stdout.
	con := installflow.Console{Stdout: stdout, Stderr: stderr, Flush: hub.Flush}
	// A nil responder: --yes settles what it settles, and every other prompt is
	// unavailable. Nothing here holds stdin.
	broker := installflow.NewBroker(hub, nil, req.AssumeYes)
	sum, err := runRecovering(ctx, hub, e, req, installflow.Runtime{
		Prompts: broker, Preflight: preflight, Console: con,
		// The console is only ever log lines here, so the tool may run in the mode
		// that lets the engine report measured progress.
		ObserveTools: true,
	})
	<-hub.Done()
	if err == nil && !sum.Succeeded() {
		err = errIncomplete
	}
	return err, sink.failure()
}

// runRecovering is hub.Run with an engine panic returned as an error.
//
// 🔴 A PANIC STILL ENDS IN THE ONE VERDICT, AND THE EXIT AGREES WITH IT. hub.Run
// queues a failed summary and panics on. Left to unwind, the panic would skip the
// wait for that summary to be written — the stream could end without its last line
// — and the process would exit 2, where the summary says 1. Recovered here, the
// caller waits for the summary like any other run's and returns an error, so the
// command exits 1. The panic's value and stack go to stderr with that error: it is
// a bug, and the person reading stderr needs both to report it.
func runRecovering(ctx context.Context, hub *installflow.Hub, e installflow.Engine, req installflow.Request,
	rt installflow.Runtime) (sum installflow.Summary, err error) {
	defer func() {
		if p := recover(); p != nil {
			sum, err = installflow.Summary{}, &PanicError{Value: p, Stack: debug.Stack()}
		}
	}()
	return hub.Run(ctx, e, req, rt)
}

// PanicError is an engine panic, recovered so the stream could close.
type PanicError struct {
	Value any
	Stack []byte
}

func (p *PanicError) Error() string {
	return fmt.Sprintf("dcctl failed unexpectedly (a panic: %v). This is a bug in dcctl; please report it "+
		"with the stack below.\n\n%s", p.Value, p.Stack)
}

// Refuse writes the stream of a command refused before its run could start: a
// single run.summary that failed at failedStep and changed nothing. It returns
// an error only when that line could not be written; the refusal itself is the
// caller's to return.
func (p Presenter) Refuse(ctx context.Context, op installflow.Operation, failedStep installflow.StepID) error {
	err, werr := p.run(ctx, refusal{step: failedStep}, installflow.Request{Operation: op}, nil)
	switch {
	case werr != nil:
		return werr
	case errors.Is(err, errRefused):
		return nil
	case err == nil:
		return errors.New("installjson: a refusal closed as a success")
	}
	return err
}

var errRefused = errors.New("refused before the run started")

// refusal is the engine of a run refused before it started.
type refusal struct{ step installflow.StepID }

func (r refusal) Run(_ context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
	s := installflow.Summary{
		Outcome: installflow.RunFailed, InstallRecord: installflow.RecordNotRead,
		Readiness: installflow.ReadinessNotChecked, Claim: installflow.ClaimNotClaimed,
		FailedStep: r.step, PartialState: installflow.EffectNoneObserved,
	}
	return s, errors.Join(errRefused, rt.Events.Finish(s))
}

// lineSink writes each envelope as one line, in one write.
type lineSink struct {
	w io.Writer

	mu  sync.Mutex
	err error
}

// Deliver writes e. An envelope that cannot be marshalled is not written at
// all — never half a line — and nothing is written after a failed write; either
// makes the run's result an error.
func (s *lineSink) Deliver(e installflow.Envelope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		s.err = fmt.Errorf("encoding event %d (%s): %w", e.Seq, e.Kind, err)
		return
	}
	if _, err := s.w.Write(append(b, '\n')); err != nil {
		s.err = fmt.Errorf("writing the event stream: %w", err)
	}
}

func (s *lineSink) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
