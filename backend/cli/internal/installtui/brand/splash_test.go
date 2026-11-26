// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package brand

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/devicechain-io/dcctl/internal/installtui/brand/gen"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

var t0 = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func inputs(width, height int, change ...func(*terminal.Inputs)) terminal.Inputs {
	in := terminal.Inputs{
		InputTTY: true, OutputTTY: true, ErrorTTY: true, GOOS: "linux",
		Env:   map[string]string{"TERM": "xterm-256color", "LANG": "en_US.UTF-8"},
		Width: width, Height: height, SizeKnown: true, Profile: termenv.TrueColor, UI: terminal.TUI,
	}
	for _, c := range change {
		c(&in)
	}
	return in
}

func caps(width, height int, change ...func(*terminal.Inputs)) terminal.Capabilities {
	return terminal.Evaluate(inputs(width, height, change...))
}

func splashAt(c terminal.Capabilities, elapsed time.Duration) *Splash {
	s := New(Options{Caps: c, Subtitle: "Prepare a Kubernetes cluster", Version: "dcctl v1.12.0"})
	s.Start(t0)
	s.Advance(t0.Add(elapsed))
	return s
}

// visible returns the display-width span [left, right) of a line's content,
// measured only with lipgloss.Width. Padding is plain spaces; content may be
// styled, so a byte or rune count would be wrong.
func visible(line string) (left, right int, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if strings.TrimSpace(trimmed) == "" {
		return 0, 0, false
	}
	left = lipgloss.Width(line) - lipgloss.Width(trimmed)
	right = lipgloss.Width(strings.TrimRight(line, " "))
	return left, right, true
}

// Every logo row's visible centre sits within half a cell of the mark's
// axis, and the axis sits in the middle of the terminal, at odd and even
// widths, for every profile that shows the mark.
func TestSymmetry(t *testing.T) {
	for _, profile := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256} {
		for _, size := range [][2]int{{120, 40}, {121, 40}, {100, 30}, {81, 24}, {80, 24}, {80, 22}, {99, 21}} {
			c := caps(size[0], size[1], func(in *terminal.Inputs) { in.Profile = profile })
			s := splashAt(c, Duration)
			if s.Art() != terminal.LargeMark && s.Art() != terminal.CompactMark {
				t.Fatalf("%v: no mark at %dx%d", profile, size[0], size[1])
			}
			m := Large()
			if s.Art() == terminal.CompactMark {
				m = Compact()
			}
			lines := strings.Split(s.View(), "\n")
			first := (size[1] - (m.Rows + 6)) / 2
			if first < 0 {
				first = 0
			}
			axis := float64(size[0]) / 2
			for i, line := range lines[first : first+m.Rows] {
				left, right, ok := visible(line)
				if !ok {
					t.Fatalf("%v %dx%d: logo row %d is empty", profile, size[0], size[1], i)
				}
				if centre := float64(left+right) / 2; math.Abs(centre-axis) > 0.5 {
					t.Errorf("%v %dx%d: logo row %d centred at %.1f, terminal axis %.1f", profile, size[0], size[1], i, centre, axis)
				}
				// The same row, measured inside the mark, centres on the mark's own axis.
				if centre := float64(left+right)/2 - float64(left-firstOpaque(m, i)); math.Abs(centre-m.Axis) > 0.5 {
					t.Errorf("%v %dx%d: logo row %d centred at %.1f in the mark, axis %.3f", profile, size[0], size[1], i, centre, m.Axis)
				}
			}
			word := lines[first+m.Rows+1]
			left, right, _ := visible(word)
			if right-left != len("DeviceChain") || math.Abs(float64(left+right)/2-axis) > 0.5 {
				t.Errorf("%v %dx%d: wordmark spans [%d,%d), axis %.1f", profile, size[0], size[1], left, right, axis)
			}
		}
	}
}

func firstOpaque(m Mark, row int) int {
	for x, c := range m.Cells[row] {
		if c.Top != 0 || c.Bottom != 0 {
			return x
		}
	}
	return 0
}

// No animation frame moves a row: every frame's content stays inside the
// static mark's span on the same row.
func TestFramesStayInPlace(t *testing.T) {
	c := caps(120, 40)
	static := strings.Split(splashAt(c, Duration).View(), "\n")
	for e := time.Duration(0); e <= Duration; e += FrameInterval {
		lines := strings.Split(splashAt(c, e).View(), "\n")
		if len(lines) != len(static) {
			t.Fatalf("%v: frame has %d lines, static %d", e, len(lines), len(static))
		}
		for i := range lines[:len(lines)-6] {
			l, r, ok := visible(lines[i])
			if !ok {
				continue
			}
			sl, sr, _ := visible(static[i])
			if l < sl || r > sr {
				t.Errorf("%v: line %d spans [%d,%d), static [%d,%d)", e, i, l, r, sl, sr)
			}
		}
	}
}

// The beats at 0, 250, 600, 850 and 1,200 ms, each checked 1 ms either side.
func TestAnimationBeats(t *testing.T) {
	m := Large()
	hidden := func(at time.Duration) int {
		n := 0
		for y := 0; y < m.Pixels; y++ {
			for x := 0; x < m.Pixels; x++ {
				if k := m.Pixel(x, y); k != 0 {
					if _, ok := pixelColor(m, k, x, y, at); !ok {
						n++
					}
				}
			}
		}
		return n
	}
	canonical := func(at time.Duration) bool {
		for y := 0; y < m.Pixels; y++ {
			for x := 0; x < m.Pixels; x++ {
				if k := m.Pixel(x, y); k != 0 {
					if c, ok := pixelColor(m, k, x, y, at); !ok || c != Facets()[k-1].RGB {
						return false
					}
				}
			}
		}
		return true
	}
	ms := time.Millisecond
	// 0-250 ms: the rim traces clockwise from the top; the cube is dim.
	if hidden(0) == 0 || hidden(TraceEnd/2) >= hidden(0) || hidden(TraceEnd/2) == 0 || hidden(TraceEnd) != 0 {
		t.Errorf("rim trace: hidden %d at 0, %d at 125 ms, %d at 250 ms", hidden(0), hidden(TraceEnd/2), hidden(TraceEnd))
	}
	cubeDim := func(at time.Duration) (dim, total int) {
		for y := 0; y < m.Pixels; y++ {
			for x := 0; x < m.Pixels; x++ {
				if k := m.Pixel(x, y); k != 0 && Facets()[k-1].Part == gen.Cube {
					total++
					if c, _ := pixelColor(m, k, x, y, at); c == lerp(Facets()[k-1].RGB, background, 0.75) {
						dim++
					}
				}
			}
		}
		return dim, total
	}
	for _, at := range []time.Duration{0, TraceEnd - ms} {
		if dim, total := cubeDim(at); dim != total {
			t.Errorf("%v: %d of %d cube pixels dim", at, dim, total)
		}
	}
	// 250-600 ms: the facets brighten under one diagonal light band.
	// (At 251 ms the brightening is still below one colour step.)
	if dim, total := cubeDim(TraceEnd + 30*ms); dim == total {
		t.Errorf("280 ms: all %d cube pixels still fully dim", dim)
	}
	if dim, _ := cubeDim(SweepEnd - ms); dim != 0 {
		t.Errorf("599 ms: %d cube pixels still fully dim", dim)
	}
	// 600-850 ms: locked colours with a fading rim highlight; the wordmark appears.
	for _, at := range []time.Duration{0, TraceEnd, TraceEnd + ms, SweepEnd - ms, SweepEnd, SweepEnd + ms, LockEnd - 50*ms} {
		if canonical(at) {
			t.Errorf("%v: already canonical before the lock", at)
		}
	}
	for y := 0; y < m.Pixels; y++ {
		for x := 0; x < m.Pixels; x++ {
			if k := m.Pixel(x, y); k != 0 {
				c, ok := pixelColor(m, k, x, y, LockEnd-ms)
				base := Facets()[k-1].RGB
				for i := range c {
					if !ok || int(c[i])-int(base[i]) > 1 || int(base[i])-int(c[i]) > 1 {
						t.Fatalf("849 ms: pixel (%d,%d) %v is not settling on %v", x, y, c, base)
					}
				}
			}
		}
	}
	// 850-1,200 ms: the canonical colours, held.
	for _, at := range []time.Duration{LockEnd, LockEnd + ms, Duration - ms, Duration, Duration + ms, -1} {
		if !canonical(at) {
			t.Errorf("%v: not the canonical colours at lock", at)
		}
	}
	for _, at := range []time.Duration{0, 250, 600, 850, 1200} {
		golden(t, fmt.Sprintf("frame-%04d.ansi", at), strings.Join(Frame(m, termenv.TrueColor, at*ms), "\n")+"\n")
	}
	c := caps(120, 40)
	if v := splashAt(c, SweepEnd-ms).View(); strings.Contains(v, "Device") {
		t.Error("the wordmark appeared before 600 ms")
	}
	if v := splashAt(c, SweepEnd).View(); !strings.Contains(v, "Device") {
		t.Error("the wordmark is missing at 600 ms")
	}
	if s := splashAt(c, Duration-ms); s.Done() {
		t.Error("the splash ended before 1,200 ms")
	}
	if s := splashAt(c, Duration+ms); !s.Done() || s.Elapsed() != Duration {
		t.Error("the splash ran past 1,200 ms")
	}
	if s := splashAt(c, Duration); !s.Done() || s.Skipped() || s.Elapsed() != Duration {
		t.Errorf("the splash did not hand off at 1,200 ms: done=%v elapsed=%v", s.Done(), s.Elapsed())
	}
	if s := splashAt(c, 10*time.Second); s.Elapsed() != Duration {
		t.Errorf("elapsed %v runs past the hold", s.Elapsed())
	}
}

func TestTicksDropLateFramesAndStayWithinBudget(t *testing.T) {
	s := New(Options{Caps: caps(120, 40), Subtitle: "Prepare a Kubernetes cluster", Version: "dcctl v1.12.0"})
	s.Start(t0)
	now, frames, bytes := t0, 0, len(s.View())
	var last time.Duration
	for !s.Done() {
		d := s.NextTick()
		if d <= 0 {
			t.Fatalf("non-positive tick at %v", s.Elapsed())
		}
		now = now.Add(d)
		s.Advance(now)
		if gap := s.Elapsed() - last; gap < FrameInterval && !s.Done() {
			t.Fatalf("redraw %v after the previous one", gap)
		}
		last = s.Elapsed()
		frames++
		bytes += len(s.View())
	}
	if now.Sub(t0) != Duration {
		t.Errorf("the hold lasted %v", now.Sub(t0))
	}
	if frames > 15 {
		t.Errorf("%d frames in %v exceeds 12 redraws a second", frames, Duration)
	}
	if bytes > 192*1024 {
		t.Errorf("the splash wrote %d bytes, over the 192 KiB budget", bytes)
	}
	// A late tick drops the frames it missed instead of replaying them.
	s = New(Options{Caps: caps(120, 40)})
	s.Start(t0)
	s.Advance(t0.Add(300 * time.Millisecond))
	if d := s.NextTick(); d != 4*FrameInterval-300*time.Millisecond {
		t.Errorf("after a late tick at 300 ms the next frame is %v away", d)
	}
	// Time never runs backwards.
	s.Advance(t0.Add(100 * time.Millisecond))
	if s.Elapsed() != 300*time.Millisecond {
		t.Errorf("elapsed went back to %v", s.Elapsed())
	}
	// A static splash holds once, with no frames.
	s = New(Options{Caps: caps(120, 40, func(in *terminal.Inputs) { in.Env["SSH_CONNECTION"] = "x" })})
	s.Start(t0)
	if s.NextTick() != Duration {
		t.Errorf("static hold ticks after %v", s.NextTick())
	}
}

func TestSkipEndsAtOnceAndConsumesTheKey(t *testing.T) {
	s := New(Options{Caps: caps(120, 40)})
	if s.Skip() {
		t.Fatal("a splash that has not started consumed a key")
	}
	s.Start(t0)
	s.Advance(t0.Add(100 * time.Millisecond))
	if !s.Skip() {
		t.Fatal("the skip key was not consumed")
	}
	if !s.Done() || !s.Skipped() {
		t.Fatal("skip did not end the splash at once")
	}
	if s.Elapsed() != 100*time.Millisecond {
		t.Errorf("skip moved the clock to %v", s.Elapsed())
	}
	if s.Skip() {
		t.Error("a key after the hand-off was consumed by the splash")
	}
	s.Advance(t0.Add(time.Second))
	if s.Elapsed() != 100*time.Millisecond {
		t.Error("a finished splash kept advancing")
	}
}

// Eligibility comes from the session's capabilities and is never re-derived.
func TestEligibility(t *testing.T) {
	ssh := func(in *terminal.Inputs) { in.Env["SSH_TTY"] = "/dev/pts/0" }
	noColor := func(in *terminal.Inputs) { in.Env["NO_COLOR"] = "" }
	yes := func(in *terminal.Inputs) { in.Yes = true }
	noSplash := func(in *terminal.Inputs) { in.NoSplash = true }
	dev := func(in *terminal.Inputs) { in.Dev = true }
	dryRun := func(in *terminal.Inputs) { in.DryRun = true }
	for name, tc := range map[string]struct {
		change  func(*terminal.Inputs)
		art     terminal.Art
		animate bool
	}{
		"eligible":    {func(*terminal.Inputs) {}, terminal.LargeMark, true},
		"ssh":         {ssh, terminal.LargeMark, false},
		"NO_COLOR":    {noColor, terminal.TextHeader, false},
		"--yes":       {yes, terminal.NoArt, false},
		"--no-splash": {noSplash, terminal.NoArt, false},
		"--dev":       {dev, terminal.NoArt, false},
		"--dry-run":   {dryRun, terminal.NoArt, false},
	} {
		s := New(Options{Caps: caps(120, 40, tc.change)})
		s.Start(t0)
		if s.Art() != tc.art || s.Animating() != tc.animate {
			t.Errorf("%s: art %v animate %v, want %v %v", name, s.Art(), s.Animating(), tc.art, tc.animate)
		}
		if tc.art == terminal.NoArt && (!s.Done() || s.View() != "" || s.Skip()) {
			t.Errorf("%s: a splash with no art paused or drew", name)
		}
		if name == "NO_COLOR" && strings.Contains(s.View(), "\x1b[3") {
			t.Errorf("NO_COLOR drew colour: %q", s.View())
		}
	}
	// Animation needs the Capabilities' decision, not just a large mark.
	c := caps(120, 40)
	c.Animate = false
	if New(Options{Caps: c}).Animating() {
		t.Error("animated without the capability decision")
	}
}

func TestResize(t *testing.T) {
	s := New(Options{Caps: caps(120, 40)})
	s.Start(t0)
	s.Resize(130, 45)
	if !s.Animating() {
		t.Fatal("growing stopped the animation")
	}
	s.Resize(110, 40)
	if s.Animating() || s.Art() != terminal.LargeMark {
		t.Fatal("shrinking did not stop the animation")
	}
	s.Resize(200, 60)
	if s.Animating() {
		t.Fatal("growing replayed the animation")
	}
	s.Resize(90, 22)
	if s.Art() != terminal.CompactMark {
		t.Fatalf("art %v at 90x22", s.Art())
	}
	s.Resize(200, 60)
	if s.Art() != terminal.CompactMark {
		t.Fatal("the art grew back")
	}
	s.Resize(70, 30)
	if s.Art() != terminal.TextHeader || !strings.Contains(s.View(), "Device") {
		t.Fatalf("art %v at 70x30", s.Art())
	}
	// Resizing never ends or restarts the hold.
	s.Advance(t0.Add(500 * time.Millisecond))
	if s.Done() || s.Elapsed() != 500*time.Millisecond {
		t.Fatal("resizing changed the hold")
	}
}

func TestArtForMatchesEvaluate(t *testing.T) {
	for _, p := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256, termenv.ANSI} {
		for w := 50; w <= 130; w += 3 {
			for h := 14; h <= 34; h++ {
				c := caps(w, h, func(in *terminal.Inputs) { in.Profile = p })
				if got := artFor(c.Level, w, h); got != c.Splash {
					t.Fatalf("%v %dx%d: artFor %v, Evaluate %v", p, w, h, got, c.Splash)
				}
			}
		}
	}
}

func TestSplashGoldens(t *testing.T) {
	for _, tc := range []struct {
		name   string
		w, h   int
		change func(*terminal.Inputs)
	}{
		{"l4-120x40", 120, 40, nil},
		{"l3-100x30", 100, 30, func(in *terminal.Inputs) { in.Profile = termenv.ANSI256 }},
		{"l4-80x22", 80, 22, nil},
		{"l2-80x24", 80, 24, func(in *terminal.Inputs) { in.Profile = termenv.ANSI }},
		{"l1-80x24", 80, 24, func(in *terminal.Inputs) { in.Env["NO_COLOR"] = "1" }},
		{"l4-60x20", 60, 20, nil},
	} {
		var change []func(*terminal.Inputs)
		if tc.change != nil {
			change = append(change, tc.change)
		}
		view := splashAt(caps(tc.w, tc.h, change...), Duration).View() + "\n"
		golden(t, "splash-"+tc.name+".ansi", view)
	}
}
