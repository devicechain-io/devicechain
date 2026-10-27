// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// clock is a settable time source for the breaker.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock { return &clock{t: time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)} }

// A session that ends within five seconds of a command publish opens the breaker; one that ends
// later, or with no command in flight, does not. The paired cases are the control for each other.
func TestADisconnectRightAfterAPublishOpensTheBreakerAndAnyOtherDoesNot(t *testing.T) {
	clk := newClock()
	b := NewBreaker(clk.now)

	assert.False(t, b.Disconnected(), "a disconnect with no command ever published opened the breaker")
	assert.Equal(t, BreakerClosed, b.State())

	b.Published()
	clk.advance(breakerWatch)
	assert.False(t, b.Disconnected(), "a disconnect a full watch after the publish was blamed on the command")
	assert.Equal(t, BreakerClosed, b.State())

	b.Published()
	clk.advance(breakerWatch - time.Millisecond)
	assert.True(t, b.Disconnected(), "a disconnect inside the watch did not open the breaker")
	assert.Equal(t, BreakerOpen, b.State())
	assert.False(t, b.Admit(), "an open breaker admitted a command")
}

// Open for five minutes, then half-open: exactly one command is let through, and a second asks to
// wait for the first one's verdict.
func TestHalfOpenLetsExactlyOneCommandThrough(t *testing.T) {
	clk := newClock()
	b := NewBreaker(clk.now)
	b.Published()
	assert.True(t, b.Disconnected())

	clk.advance(breakerMinBackoff - time.Second)
	assert.Equal(t, BreakerOpen, b.State(), "the breaker reopened before its backoff ended")
	assert.False(t, b.Admit())

	clk.advance(time.Second)
	assert.Equal(t, BreakerHalfOpen, b.State())
	assert.True(t, b.Admit(), "the half-open breaker refused its probe")
	assert.False(t, b.Admit(), "the half-open breaker let a second command through before the probe settled")
}

// A probe that stays on the wire for the watch without a disconnect closes the breaker and clears
// its history; the next trip starts again from the shortest backoff.
func TestACleanProbeClosesTheBreakerAndResetsTheBackoff(t *testing.T) {
	clk := newClock()
	b := NewBreaker(clk.now)
	b.Published()
	assert.True(t, b.Disconnected())
	clk.advance(breakerMinBackoff)

	assert.True(t, b.Admit())
	b.Published()
	clk.advance(breakerWatch)
	assert.Equal(t, BreakerClosed, b.State(), "a probe with no disconnect inside the watch did not close the breaker")
	assert.True(t, b.Admit())
	assert.True(t, b.Admit(), "a closed breaker limits nothing")

	b.Published()
	assert.True(t, b.Disconnected())
	clk.advance(breakerMinBackoff)
	assert.Equal(t, BreakerHalfOpen, b.State(), "the backoff did not start over after a clean probe")
}

// A disconnect that follows the probe reopens the breaker for twice as long, up to an hour.
func TestAFailedProbeReopensWithADoubledBackoffUpToAnHour(t *testing.T) {
	clk := newClock()
	b := NewBreaker(clk.now)
	b.Published()
	assert.True(t, b.Disconnected())

	want := breakerMinBackoff
	for i := 0; i < 6; i++ {
		clk.advance(want - time.Second)
		assert.Equal(t, BreakerOpen, b.State(), "round %d: reopened early", i)
		clk.advance(time.Second)
		assert.Equal(t, BreakerHalfOpen, b.State(), "round %d: not half-open after %s", i, want)
		assert.True(t, b.Admit())
		b.Published()
		clk.advance(time.Second)
		assert.True(t, b.Disconnected(), "round %d: a disconnect after the probe did not reopen the breaker", i)
		want = min(want*2, breakerMaxBackoff)
	}
	assert.Equal(t, time.Hour, want, "the doubling did not reach the ceiling the test expects")
	clk.advance(time.Hour)
	assert.Equal(t, BreakerHalfOpen, b.State(), "the backoff exceeded an hour")
}

// A disconnect while the breaker is open does not extend it: its commands were not being sent.
func TestADisconnectWhileOpenDoesNotExtendTheBackoff(t *testing.T) {
	clk := newClock()
	b := NewBreaker(clk.now)
	b.Published()
	assert.True(t, b.Disconnected())
	clk.advance(time.Minute)
	assert.False(t, b.Disconnected(), "a disconnect while open was counted as another trip")
	clk.advance(breakerMinBackoff - time.Minute)
	assert.Equal(t, BreakerHalfOpen, b.State(), "a disconnect while open pushed the backoff out")
}

// A probe that was admitted and never reached the wire gives its slot back, and a slot nobody
// returns frees itself, so one failed probe cannot wedge the breaker half-open for good.
func TestAProbeThatNeverReachedTheWireDoesNotWedgeTheBreaker(t *testing.T) {
	clk := newClock()
	b := NewBreaker(clk.now)
	b.Published()
	assert.True(t, b.Disconnected())
	clk.advance(breakerMinBackoff)

	assert.True(t, b.Admit())
	assert.False(t, b.Admit())
	b.Release()
	assert.True(t, b.Admit(), "a released probe slot was not available again")

	clk.advance(breakerProbeGrace)
	assert.True(t, b.Admit(), "a probe slot nobody returned never freed itself")
}
