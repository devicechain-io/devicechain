// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
)

// reviewSettle is the quiet period a screen whose Enter leads on (the install review,
// and setup's hand-over and instance review) must see before it takes a key: since the
// visit began, AND since the last input was read from the terminal.
//
// 🔴 A KEY TYPED BEFORE THE SCREEN COULD BE SEEN NEVER ACTS ON IT. Keys typed ahead (a
// second Enter after Continue, a Tab and an Enter queued while the screen was being drawn,
// a burst typed while a stalled terminal drew nothing) arrive after the screen has
// changed, faster than anyone could read it. It is a variable only so that tests need not
// wait for it.
var reviewSettle = 500 * time.Millisecond

// settlePoll is how often a settling screen looks again at whether it has settled.
var settlePoll = 100 * time.Millisecond

// settleMsg asks visit gen whether it has settled; token numbers the ask, so one left
// over from an earlier ask or visit arms nothing.
type settleMsg struct{ gen, token int }

// inputClock is when input was last read from the terminal, on a monotonic clock.
//
// 🔴 IT IS STAMPED BY THE READER, NOT BY THE EVENT LOOP. The stdin gate owns the read of
// the terminal (stdinGate.programRead) and runs on its own goroutine, so it stamps every
// byte read even while the event loop is blocked (a renderer stalled on a terminal that
// does not take output blocks the loop in its render). Keys typed during any stall
// therefore push the quiet period forward, wherever they later land in the message queue.
type inputClock struct {
	base time.Time
	// last is the time of the last input read, as nanoseconds since base; 0 is none.
	last atomic.Int64
	// readerStamps says the reader stamps input. Where it cannot (a Windows console,
	// whose records Bubble Tea reads past the gate), the screen stamps each key as it
	// handles it instead; see the residual in settleGate.
	readerStamps bool
	// now, when set, replaces the monotonic clock (tests).
	now func() time.Duration
}

func newInputClock(readerStamps bool) *inputClock {
	return &inputClock{base: time.Now(), readerStamps: readerStamps}
}

func (c *inputClock) since() time.Duration {
	if c.now != nil {
		return c.now()
	}
	return time.Since(c.base)
}

// stamp records input read now.
func (c *inputClock) stamp() {
	if c != nil {
		c.last.Store(max(int64(c.since()), 1))
	}
}

// lastInput is when input was last read, and whether any has been.
func (c *inputClock) lastInput() (time.Duration, bool) {
	l := c.last.Load()
	return time.Duration(l), l != 0
}

// settleGate is the arming of one screen whose Enter leads on: the install review
// (approval 1), and setup's hand-over and instance review (approval 2).
//
// A visit is armed when, as a settle tick is handled, BOTH hold:
//
//  1. at least reviewSettle has passed since the visit began; and
//  2. at least reviewSettle has passed since the last input was read from the terminal.
//
// Until then the tick is asked again, and every key on the screen is dropped (the reader
// has already stamped it, so it has pushed condition 2 forward; that includes keys a
// too-small terminal drops before this gate sees them). And when a key is handled on an
// armed visit it is checked again: if no input has been read since the visit armed, the
// key was read BEFORE the arming (it waited in the queue behind the tick, as keys typed
// during a stall do), so it is dropped and the visit settles again.
//
// 🔴 THE RESIDUAL. A key the person presses AFTER the visit armed is accepted, even if
// the terminal is at that moment still stalled on the previous screen's frame: the screen
// cannot know what the terminal shows. The quiet period makes that a key pressed at least
// reviewSettle after the last key and after the screen was entered. In the queue, the
// same goes for a key read before the arming but handled after a newer key was read: the
// queue is in reading order, so it takes a later key typed after the arming, and the
// stale key is then accepted with it. On a Windows console the reader cannot stamp
// (Bubble Tea reads the console's records itself), so the screen stamps each key as it
// handles it: keys typed during a stall count from when the loop handles them, and only
// condition 1 and the quiet period between handled keys protect the approval there.
type settleGate struct {
	clock *inputClock
	// gen counts visits; token counts asks, so a stale settle message arms nothing.
	gen, token int
	enteredAt  time.Duration
	armed      bool
	armedAt    time.Duration
}

func (g *settleGate) ensureClock() *inputClock {
	if g.clock == nil {
		g.clock = newInputClock(false)
	}
	return g.clock
}

// enter starts a new visit, unarmed, and asks when it has settled.
func (g *settleGate) enter() tea.Cmd {
	g.gen++
	g.armed = false
	g.enteredAt = g.ensureClock().since()
	return g.ask()
}

// leave ends the visit: nothing from it arms anything afterwards.
func (g *settleGate) leave() {
	g.token++
	g.armed = false
}

// ask schedules a settle check of the visit on screen; earlier asks no longer count.
func (g *settleGate) ask() tea.Cmd {
	g.token++
	gen, token := g.gen, g.token
	return tea.Tick(settlePoll, func(time.Time) tea.Msg { return settleMsg{gen: gen, token: token} })
}

// quiet reports whether reviewSettle has passed both since the visit began and since
// the last input was read.
func (g *settleGate) quiet() bool {
	c := g.ensureClock()
	now := c.since()
	if now-g.enteredAt < reviewSettle {
		return false
	}
	if last, ok := c.lastInput(); ok && now-last < reviewSettle {
		return false
	}
	return true
}

// settled handles a settle tick: the visit arms when it is quiet, and asks again when
// not. A tick of an earlier ask or visit does nothing (the current ask stands).
func (g *settleGate) settled(m settleMsg) tea.Cmd {
	if m.gen != g.gen || m.token != g.token || g.armed {
		return nil
	}
	if !g.quiet() {
		return g.ask()
	}
	g.armed, g.armedAt = true, g.ensureClock().since()
	return nil
}

// admit reports whether a key may act on the visit. A key before the visit armed is
// dropped. A key handled on an armed visit when no input has been read since the arming
// was read before it (it waited behind the tick): it is dropped, and the visit settles
// again.
func (g *settleGate) admit() (bool, tea.Cmd) {
	c := g.ensureClock()
	if !c.readerStamps {
		c.stamp() // nothing reads the terminal for us: this key is the latest input
	}
	if !g.armed {
		return false, nil
	}
	if last, ok := c.lastInput(); !ok || last < g.armedAt {
		g.armed = false
		return false, g.ask()
	}
	return true, nil
}
