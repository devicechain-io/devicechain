// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
)

// reviewSettle is the quiet period a screen whose Enter leads on must see, after a frame
// of it has been written to the terminal, before it takes a key.
//
// 🔴 A KEY TYPED BEFORE THE SCREEN COULD BE SEEN NEVER ACTS ON IT. Keys the operator
// typed ahead (a second Enter after Continue, a Tab and an Enter queued while the screen
// was being drawn, a burst typed while a stalled terminal was not drawing at all) arrive
// after the screen has changed, faster than anyone could read it. It is a variable only
// so that tests need not wait for it.
var reviewSettle = 500 * time.Millisecond

// settleMsg ends the quiet period numbered token of visit gen.
type settleMsg struct{ gen, token int }

// frameShownMsg says the screen writer finished writing a frame whose write started
// after draw number seq (frameClock.drawn) had been taken.
type frameShownMsg struct{ seq uint64 }

// frameClock connects the screens to the writer that puts their frames on the terminal.
// The model counts each draw of a settling screen; the writer, when asked, reports the
// first frame it finishes writing, with the draw count as it stood when that write began.
type frameClock struct {
	drawn atomic.Uint64
	want  atomic.Bool
}

// wrote is the screen writer's report of one finished write, begun at draw count start.
// It reports only when a screen asked for it, once per asking.
func (c *frameClock) wrote(start uint64, report func(frameShownMsg)) {
	if c != nil && c.want.CompareAndSwap(true, false) {
		report(frameShownMsg{seq: start})
	}
}

// settleGate is the arming of one screen whose Enter leads on: the install review
// (approval 1), and the instance journey's hand-over and review (approval 2).
//
// A visit is armed only when BOTH hold:
//
//  1. a frame of this visit has been written to the terminal: a draw of it was counted,
//     and the writer then finished a write that began after that draw; and
//  2. a quiet period of reviewSettle has passed since then with no key at all. Every key
//     before the visit is armed is dropped, and a key after its frame was shown restarts
//     the quiet period.
//
// So a burst typed ahead, however it is delivered (before the frame, after it, or after
// the stalled renderer let a timer fire), never arms the screen: the first key that
// counts is one pressed after the screen could be seen and the keyboard had gone quiet.
type settleGate struct {
	clock *frameClock
	// gen counts visits; token counts quiet periods, so a settle message from a period
	// a key restarted, or from an earlier visit, arms nothing.
	gen, token int
	// drawSeq is the draw count of this visit's first draw (0: not drawn yet).
	drawSeq      uint64
	shown, armed bool
}

// enter starts a new visit: unarmed, not shown, waiting for its first frame. It returns
// nothing to run: the quiet period starts from the frame, not from here.
func (g *settleGate) enter() tea.Cmd {
	g.gen++
	g.token++
	g.drawSeq, g.shown, g.armed = 0, false, false
	return nil
}

// leave ends the visit: nothing from it arms anything afterwards.
func (g *settleGate) leave() {
	g.token++
	g.drawSeq, g.shown, g.armed = 0, false, false
}

// drew records that the visit's screen was drawn whole, and asks the writer to report
// the frame that carries it.
func (g *settleGate) drew() {
	if g.gen == 0 || g.shown || g.drawSeq != 0 {
		return
	}
	g.drawSeq = 1
	if g.clock != nil {
		g.drawSeq = g.clock.drawn.Add(1)
		g.clock.want.Store(true)
	}
}

// frameShown takes the writer's report. A frame begun before this visit was drawn does
// not count, and the writer is asked again.
func (g *settleGate) frameShown(m frameShownMsg) tea.Cmd {
	if g.gen == 0 || g.shown || g.drawSeq == 0 {
		return nil
	}
	if m.seq < g.drawSeq {
		if g.clock != nil {
			g.clock.want.Store(true)
		}
		return nil
	}
	g.shown = true
	return g.quiet()
}

// quiet starts a new quiet period; any earlier one no longer counts.
func (g *settleGate) quiet() tea.Cmd {
	g.token++
	gen, token := g.gen, g.token
	return tea.Tick(reviewSettle, func(time.Time) tea.Msg { return settleMsg{gen: gen, token: token} })
}

// settled arms the visit when the quiet period that ended is the current one.
func (g *settleGate) settled(m settleMsg) {
	if m.gen == g.gen && m.token == g.token && g.shown {
		g.armed = true
	}
}

// admit reports whether a key may act on the visit. A key before it is armed is
// dropped; once its frame has been shown, it also restarts the quiet period.
func (g *settleGate) admit() (bool, tea.Cmd) {
	if g.armed {
		return true, nil
	}
	if g.shown {
		return false, g.quiet()
	}
	return false, nil
}
