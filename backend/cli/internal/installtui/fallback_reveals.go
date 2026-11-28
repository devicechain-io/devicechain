// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
)

// errRevealLost is the run's failure when a one-time disclosure could not be shown at all.
var errRevealLost = errors.New("a one-time disclosure could not be shown after the terminal UI stopped; " +
	"read it from the Secret the report names")

// fallbackReveals is the instance journey's reveal sink. While the screen is up it is the
// inbox the first-login screen takes from after the run; once the run has fallen back to
// plain output it shows each disclosure the moment it is offered, as plain output always
// has.
//
// 🔴 A ONE-TIME DISCLOSURE IS NEVER LOST SILENTLY. After a fallback no screen will ever take
// from the inbox, and the run's report has already said the value is "shown only this
// once". So the fallback shows what the inbox holds, there and then, and what is offered
// afterwards goes to plain output directly. A disclosure that cannot be shown even there
// (the writer failed, or the offer has no plain form) is recorded, and the run is not
// counted a success.
//
// 🔴 PLAIN OUTPUT THAT IS NOT A TERMINAL NEVER CARRIES THE VALUE. Redirected output is a CI
// log or a file; there the Secret the value is kept in is named instead, which is not a
// loss: the value is still where the report says it is.
type fallbackReveals struct {
	inbox *RevealInbox
	w     io.Writer
	// terminal answers whether w is a person's terminal. Nil asks w itself.
	terminal func() bool

	mu    sync.Mutex
	plain bool
	lost  bool
}

// Offer implements installflow.RevealSink.
func (r *fallbackReveals) Offer(o installflow.RevealOffer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.plain {
		r.inbox.Offer(o)
		return
	}
	r.show(o, false)
}

// fallBack switches to plain output and shows what the inbox holds.
func (r *fallbackReveals) fallBack() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plain {
		return
	}
	r.plain = true
	for _, o := range r.inbox.Take() {
		r.show(o, true)
	}
}

// show writes o's value as plain output writes it. late says it was offered while the
// screen was up, so the lines around it in the report went to the screen's log: it is
// introduced, since nothing else on the terminal says what it is.
func (r *fallbackReveals) show(o installflow.RevealOffer, late bool) {
	if o.PlainLine == nil {
		o.Reveal.Discard()
		r.lost = true
		return
	}
	w := &checkedWriter{w: r.w}
	atTerminal := r.terminal
	if atTerminal == nil {
		atTerminal = func() bool { return installplain.IsTerminal(r.w) }
	}
	if !atTerminal() {
		o.Reveal.Discard()
		fmt.Fprintf(w, "dcctl: a one-time value is not printed to output that is not a terminal; "+
			"read it from Secret %s/%s, key %s\n", o.Where.Namespace, o.Where.Name, o.Where.Key)
		if w.err != nil {
			r.lost = true
		}
		return
	}
	if late {
		fmt.Fprintln(w, "dcctl: the terminal UI stopped before it showed this one-time value; it is shown here, once:")
	}
	if err := o.Reveal.Show(func(v []byte) { o.PlainLine(w, v) }); err != nil || w.err != nil {
		r.lost = true
	}
}

// wasLost reports whether a disclosure could not be shown.
func (r *fallbackReveals) wasLost() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lost
}

// checkedWriter remembers the first error a write returned.
type checkedWriter struct {
	w   io.Writer
	err error
}

func (c *checkedWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	if err != nil && c.err == nil {
		c.err = err
	}
	return n, err
}
