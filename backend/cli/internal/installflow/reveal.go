// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

// ErrRevealConsumed: a Reveal was asked to show its value after it had already shown
// it, or after it was discarded.
var ErrRevealConsumed = errors.New("installflow: this value has already been revealed or discarded")

const revealMark = "[one-time reveal]"

// Reveal is a one-use capability to show one secret value to the person running
// dcctl: the generated superuser seed password, which a bootstrap shows once and never
// again.
//
// 🔴 IT IS THE ONLY WAY A SECRET LEAVES AN ENGINE FOR DISPLAY, AND IT TRAVELS OUTSIDE
// THE EVENT STREAM. A Reveal is handed to the presenter through Runtime.Reveals, never
// in an event, a log line, a JSON line or the summary; the summary carries a SecretRef
// to where the value is kept instead. Every textual form of a Reveal is a placeholder
// and marshalling one is an error, so one that strays into an encoder fails loudly.
//
// The first Show consumes it: the value is handed to the callback, then cleared. Every
// later Show is ErrRevealConsumed and calls nothing, so a screen that is re-rendered,
// a key pressed twice or a second presenter cannot show it again. Discard consumes it
// without showing it — what a presenter does when its screen is left unrevealed.
type Reveal struct {
	mu     sync.Mutex
	secret Secret
	used   bool
}

// NewReveal copies value into a new, unconsumed Reveal.
func NewReveal(value []byte) *Reveal { return &Reveal{secret: NewSecret(value)} }

// Show hands the value to f, once. The slice is valid only during f and is cleared
// when f returns; f must not keep it. A second call, or a call after Discard, returns
// ErrRevealConsumed without calling f.
func (r *Reveal) Show(f func(value []byte)) error {
	if r == nil {
		return ErrRevealConsumed
	}
	r.mu.Lock()
	if r.used {
		r.mu.Unlock()
		return ErrRevealConsumed
	}
	r.used = true
	v := r.secret
	r.mu.Unlock()
	defer v.Zero()
	f(v.Expose())
	return nil
}

// Discard consumes the Reveal without showing it. It is safe to call at any time,
// including after Show.
func (r *Reveal) Discard() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.used {
		r.used = true
		r.secret.Zero()
	}
}

// Consumed reports whether the Reveal has been shown or discarded.
func (r *Reveal) Consumed() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.used
}

func (*Reveal) String() string               { return revealMark }
func (*Reveal) GoString() string             { return revealMark }
func (*Reveal) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte(revealMark)) }
func (*Reveal) MarshalJSON() ([]byte, error) { return nil, errNotSerialisable }
func (*Reveal) MarshalText() ([]byte, error) { return nil, errNotSerialisable }

// RevealSubject names what a Reveal shows. It is an identifier, never the value.
type RevealSubject string

// RevealSuperuserSeed is the superuser's generated seed password, shown once at the end
// of the bootstrap that generated it.
const RevealSuperuserSeed RevealSubject = "bootstrap.superuser-seed"

// RevealOffer is one disclosure an engine offers its presenter.
type RevealOffer struct {
	// Step is the stage the offer is made in.
	Step StepID
	// Subject says what the value is.
	Subject RevealSubject
	// Where is where the value is kept: the reference a presenter shows in place of
	// the value, and the one the summary carries.
	Where SecretRef
	// Reveal is the capability. A presenter that does not show it discards it.
	Reveal *Reveal
	// PlainLine writes the value as dcctl's plain output has always shown it, for the
	// presenter that reproduces that output.
	PlainLine func(w io.Writer, value []byte)
}

// RevealSink is a presenter's side of a disclosure. Offer must not block on a person:
// a presenter that shows the value later (on an explicit "show once" action) keeps the
// offer until then, and discards it if the action never comes.
type RevealSink interface {
	Offer(RevealOffer)
}
