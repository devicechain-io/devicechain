// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package installplain is the plain presenter of a dcctl install: the output
// dcctl has always printed, byte for byte, on the streams it has always used.
//
// It renders a run from two sources, and keeps them in the order the engine
// produced them:
//
//   - the event stream. Today's plain output has text for two kinds of event,
//     and this package owns that text: the preflight doctor's report, rendered
//     from typed observations, and the prompts, rendered from their question
//     codes by the Responder. The other events — steps starting and ending, the
//     summary — print nothing here, because dcctl never printed anything for
//     them; the error a failed run returns is printed by the command, as it
//     always was.
//   - the engine's own human-readable output, which it writes to the Console
//     the presenter hands it. Here that is the process's own stdout and stderr,
//     each write preceded by a flush of the hub, so nothing the engine says can
//     overtake an event rendered before it.
//
// It imports no terminal-UI library.
package installplain

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// Presenter runs an engine and renders it as plain text.
type Presenter struct {
	Stdout io.Writer
	Stderr io.Writer
	// Stdin answers the prompts, read a line at a time as dcctl always has, so
	// a piped "y" still answers the kind-cluster confirmation.
	Stdin io.Reader
	// ReadHidden reads a secret answer with echo off. Nil reads os.Stdin's terminal.
	ReadHidden func() ([]byte, error)
}

// errIncomplete is returned when the engine reported no error but the run did not
// close as a success: the presenter could not be kept up to date, so nothing
// may claim the run finished.
var errIncomplete = errors.New("the run's progress could not be reported, so it is not known to have finished")

// Run executes e and renders the run. It returns the engine's own error
// unchanged, so the command prints exactly what it printed before the engine
// reported through events. It returns only once every event has been rendered.
func (p Presenter) Run(ctx context.Context, e installflow.Engine, req installflow.Request,
	preflight installflow.PreflightRunner) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	hub, err := installflow.NewHub(installflow.Options{
		Operation: req.Operation,
		Sink:      Sink{W: p.Stdout},
		// Text output renders what it always rendered: only its error is redacted
		// of what the run found (a kubeconfig's credentials).
		TokensOnlyInErrors: true,
		// A presenter that cannot keep up stops the run gracefully, saying why; the
		// hub then refuses to report it as a success.
		OnDeliveryFailure: func() {
			cancel(installflow.CancelRequest{Reason: installflow.CancelDeliveryFailure})
		},
	})
	if err != nil {
		return err
	}
	con := installflow.Console{
		Stdout: flushingWriter{hub: hub, w: p.Stdout},
		Stderr: flushingWriter{hub: hub, w: p.Stderr},
		Flush:  hub.Flush,
	}
	if p.Stdout == os.Stdout && p.Stderr == os.Stderr {
		con.ChildStreams = func() (io.Writer, io.Writer) {
			hub.Flush()
			return p.Stdout, p.Stderr
		}
	}
	broker := installflow.NewBroker(hub, Responder{Out: con.Stdout, In: p.Stdin, ReadHidden: p.ReadHidden}, req.AssumeYes)
	sum, err := hub.Run(ctx, e, req, installflow.Runtime{Prompts: broker, Preflight: preflight, Console: con,
		Reveals: Reveals{W: con.Stdout}})
	<-hub.Done()
	if err == nil && !sum.Succeeded() {
		err = errIncomplete
	}
	// Redacted: a tool's diagnostics appended to an error must not print a value
	// the run registered. The console is NOT redacted: text output shows the person
	// at the terminal credentials on purpose (a bootstrap's access summary), and
	// what a tool prints there it always printed.
	return hub.RedactError(err)
}

// flushingWriter writes to w once every event emitted before the write has been
// rendered.
type flushingWriter struct {
	hub interface{ Flush() }
	w   io.Writer
}

func (f flushingWriter) Write(b []byte) (int, error) {
	f.hub.Flush()
	return f.w.Write(b)
}

// Sink renders the events today's plain output has text for, to W.
type Sink struct{ W io.Writer }

// Deliver renders one event.
func (s Sink) Deliver(e installflow.Envelope) {
	if o, ok := e.Payload.(installflow.PreflightObservation); ok {
		RenderObservation(s.W, o)
	}
}

// Reveals shows a one-time disclosure the moment it is offered, in the line and at the
// place dcctl's output always showed it: plain output has no later screen to keep it
// for. Showing consumes the capability, so nothing can show it a second time.
//
// 🔴 THE VALUE GOES TO W AND NOWHERE ELSE: not to the hub, so never into an event, the
// log ring or the summary.
type Reveals struct{ W io.Writer }

// Offer shows the value now.
func (r Reveals) Offer(o installflow.RevealOffer) {
	if o.PlainLine == nil {
		o.Reveal.Discard()
		return
	}
	_ = o.Reveal.Show(func(v []byte) { o.PlainLine(r.W, v) })
}
