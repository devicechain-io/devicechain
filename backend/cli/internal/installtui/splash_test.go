// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/devicechain-io/dcctl/internal/installtui/brand"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

type manualClock struct{ now time.Time }

func (c *manualClock) Now() time.Time { return c.now }

func splashCaps(width, height int) terminal.Capabilities {
	return terminal.Evaluate(terminal.Inputs{
		InputTTY: true, OutputTTY: true, ErrorTTY: true, GOOS: "linux",
		Env:   map[string]string{"TERM": "xterm-256color", "LANG": "en_US.UTF-8"},
		Width: width, Height: height, SizeKnown: true, Profile: colorprofile.TrueColor, UI: terminal.TUI,
	})
}

// manualSplash returns a splash whose frames are scheduled by hand: each tick
// request is recorded instead of starting a timer.
func manualSplash(clock *manualClock) (*SplashModel, *[]time.Duration) {
	m := NewSplash(brand.Options{Caps: splashCaps(120, 40)}, clock)
	var ticks []time.Duration
	m.tick = func(d time.Duration) tea.Cmd {
		ticks = append(ticks, d)
		return func() tea.Msg { return splashTick{} }
	}
	return m, &ticks
}

// messages runs a command and flattens batches.
func messages(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, messages(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func TestSplashRunsOnTheInjectedClock(t *testing.T) {
	clock := &manualClock{now: time.Unix(1_800_000_000, 0)}
	m, ticks := manualSplash(clock)
	start := clock.now
	cmd := m.Init()
	var done []SplashDone
	for i := 0; i < 100 && cmd != nil; i++ {
		clock.now = clock.now.Add((*ticks)[len(*ticks)-1])
		var msgs []tea.Msg
		msgs = messages(cmd)
		cmd = nil
		for _, msg := range msgs {
			if d, ok := msg.(SplashDone); ok {
				done = append(done, d)
				continue
			}
			_, cmd = m.Update(msg)
		}
	}
	if len(done) != 1 || done[0].Skipped {
		t.Fatalf("hand-off messages %v", done)
	}
	if m.Splash().Elapsed() != brand.Duration || clock.now.Sub(start) < brand.Duration {
		t.Errorf("hand-off after %v", m.Splash().Elapsed())
	}
	if len(*ticks) > 15 {
		t.Errorf("%d frames requested", len(*ticks))
	}
	if _, cmd := m.Update(splashTick{}); cmd != nil {
		t.Error("a stale frame after the hand-off scheduled more work")
	}
}

func TestSplashSkipConsumesTheKey(t *testing.T) {
	clock := &manualClock{now: time.Unix(1_800_000_000, 0)}
	m, _ := manualSplash(clock)
	m.Init()
	clock.now = clock.now.Add(100 * time.Millisecond)
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); cmd != nil || m.Splash().Done() {
		t.Fatal("Ctrl+C skipped the splash; it belongs to the cancel path")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	msgs := messages(cmd)
	if len(msgs) != 1 || msgs[0] != (SplashDone{Skipped: true}) {
		t.Fatalf("a skip produced %#v; the key must be consumed and the hand-off immediate", msgs)
	}
	if _, cmd := m.Update(splashTick{}); cmd != nil {
		t.Error("the frame pending at the skip was drawn")
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Error("the hand-off was sent twice")
	}
}

// The same guarantee through a real Bubble Tea loop: the key that skips the
// splash never reaches the next screen.
func TestSplashSkipKeyNeverReachesTheNextScreen(t *testing.T) {
	parent := &handoff{splash: NewSplash(brand.Options{Caps: splashCaps(120, 40)}, SystemClock{})}
	tm := teatest.NewTestModel(t, parent, teatest.WithInitialTermSize(120, 40))
	tm.Send(tea.KeyPressMsg{Code: 'x', Text: "x"})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("next screen")) },
		teatest.WithDuration(3*time.Second))
	// Leave time for anything forwarded to arrive before the closing key.
	time.Sleep(200 * time.Millisecond)
	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(*handoff)
	if !final.done.Skipped || strings.Join(final.keys, ",") != "q" {
		t.Fatalf("hand-off %+v; next screen received %q", final.done, final.keys)
	}
	if _, err := io.ReadAll(tm.FinalOutput(t)); err != nil {
		t.Fatal(err)
	}
}

// handoff stands in for the program's screen router: the splash, then a
// screen that records every key it is given and exits on q.
type handoff struct {
	splash *SplashModel
	done   SplashDone
	after  bool
	keys   []string
}

func (h *handoff) Init() tea.Cmd { return h.splash.Init() }

func (h *handoff) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if d, ok := msg.(SplashDone); ok {
		h.done, h.after = d, true
		return h, nil
	}
	if !h.after {
		_, cmd := h.splash.Update(msg)
		return h, cmd
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		h.keys = append(h.keys, k.String())
		if k.String() == "q" {
			return h, tea.Quit
		}
	}
	return h, nil
}

func (h *handoff) View() tea.View {
	if h.after {
		return tea.NewView("next screen")
	}
	return h.splash.View()
}
