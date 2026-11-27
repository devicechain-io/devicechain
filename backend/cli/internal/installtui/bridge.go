// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
)

// bridge is the one connection between a run and the program drawing it. Everything the
// engine says reaches the screen through it, as tea messages: events from the hub,
// questions from the prompt broker, the engine's own words through the hub's log ring.
//
// 🔴 IT CAN SWITCH THE SAME RUN TO PLAIN OUTPUT, AND NEVER STARTS A SECOND ONE. When the
// program ends before the run does (the renderer failed, or panicked and was recovered),
// the run is not restarted and is not abandoned: from then on events render as plain
// text, the engine's words go to the real streams, and questions are read from stdin,
// which the program has let go of by the time it returns.
type bridge struct {
	send  func(tea.Msg)
	plain atomic.Bool
	// done is closed once the program has returned and the terminal is restored.
	done chan struct{}

	plainSink      installplain.Sink
	plainErr       io.Writer
	plainResponder installplain.Responder
}

func (b *bridge) fallBack() { b.plain.Store(true) }

// Deliver is the hub's sink.
func (b *bridge) Deliver(e installflow.Envelope) {
	if b.plain.Load() {
		b.plainSink.Deliver(e)
		// The plain presenter prints no log lines; but what a child wrote to the
		// process's own streams was the terminal's before the takeover, so after a
		// fallback it is printed there, redacted as it was in the log pane.
		if l, ok := e.Payload.(installflow.LogLine); ok && l.Source == processLogSource && b.plainErr != nil {
			_, _ = io.WriteString(b.plainErr, l.Text.String()+"\n")
		}
		return
	}
	b.send(eventMsg{env: e})
}

// Respond is the broker's responder: the question goes to the screen as a message and
// the answer comes back on a channel the screen writes once. Inside the program no other
// reader ever touches stdin, so the prompt cannot race a keypress for it.
func (b *bridge) Respond(ctx context.Context, q installflow.Question) (installflow.Response, error) {
	if b.plain.Load() {
		return b.plainResponder.Respond(ctx, q)
	}
	reply := make(chan installflow.Response, 1) // the screen never blocks on an abandoned question
	b.send(promptMsg{q: q, reply: reply})
	select {
	case r := <-reply:
		return r, nil
	case <-ctx.Done():
		return installflow.Response{}, ctx.Err()
	case <-b.done:
		if b.plain.Load() {
			return b.plainResponder.Respond(ctx, q)
		}
		return installflow.Response{}, installflow.ErrPromptUnavailable
	}
}

// switchWriter is one of the engine's console streams: the hub's log ring while the
// screen is up, the real stream once the run has fallen back to plain output.
type switchWriter struct {
	b     *bridge
	log   io.Writer
	plain io.Writer
	flush func()
	mu    sync.Mutex
}

func (w *switchWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.b.plain.Load() {
		w.flush()
		return w.plain.Write(p)
	}
	return w.log.Write(p)
}
