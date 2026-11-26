// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/devicechain-io/dcctl/internal/installtui/brand"
)

// Clock is the injected monotonic clock. time.Now carries a monotonic reading,
// so a wall-clock step never moves the animation.
type Clock interface{ Now() time.Time }

// SystemClock is the production clock.
type SystemClock struct{}

// Now returns time.Now.
func (SystemClock) Now() time.Time { return time.Now() }

// SplashDone is emitted exactly once, when the splash hands off to the next
// screen. Skipped is true when a key ended it; that key has been consumed.
type SplashDone struct{ Skipped bool }

type splashTick struct{}

// SplashModel is the splash screen's Bubble Tea face over brand.Splash. It
// never quits the program and never forwards a key: the parent switches
// screens on SplashDone.
type SplashModel struct {
	splash *brand.Splash
	clock  Clock
	// tick schedules the next frame; tests replace it to drive frames by hand.
	tick func(time.Duration) tea.Cmd
	sent bool
}

// NewSplash returns the splash model for the given options and clock.
func NewSplash(o brand.Options, clock Clock) *SplashModel {
	return &SplashModel{splash: brand.New(o), clock: clock, tick: func(d time.Duration) tea.Cmd {
		return tea.Tick(d, func(time.Time) tea.Msg { return splashTick{} })
	}}
}

// Splash exposes the underlying state, for the parent and for tests.
func (m *SplashModel) Splash() *brand.Splash { return m.splash }

// Init starts the hold and schedules the first frame.
func (m *SplashModel) Init() tea.Cmd {
	m.splash.Start(m.clock.Now())
	return m.next()
}

func (m *SplashModel) next() tea.Cmd {
	if m.splash.Done() {
		if m.sent {
			return nil
		}
		m.sent = true
		skipped := m.splash.Skipped()
		return func() tea.Msg { return SplashDone{Skipped: skipped} }
	}
	return m.tick(m.splash.NextTick())
}

// Update handles frames, sizes and keys. Any key except Ctrl+C skips the
// splash and is consumed; Ctrl+C belongs to the program's cancel path and is
// handled before a screen sees it.
func (m *SplashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case splashTick:
		if m.splash.Done() {
			return m, nil // a frame already scheduled when a key ended the splash
		}
		m.splash.Advance(m.clock.Now())
		return m, m.next()
	case tea.WindowSizeMsg:
		m.splash.Resize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, nil
		}
		if m.splash.Skip() {
			return m, m.next()
		}
	}
	return m, nil
}

// View renders the current frame.
func (m *SplashModel) View() string { return m.splash.View() }
