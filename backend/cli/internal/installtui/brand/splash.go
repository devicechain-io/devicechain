// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package brand

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// Options configure a splash. Caps is the session's capability snapshot; its
// Splash and Animate fields are the eligibility decision (never over SSH, with
// NO_COLOR, --yes, --dev, --no-splash or --dry-run) and are not re-derived.
type Options struct {
	Caps terminal.Capabilities
	// Subtitle names the journey, for example "Prepare a Kubernetes cluster".
	Subtitle string
	// Version is shown dimmed under the subtitle; it may be empty.
	Version string
}

// SkipHint is shown under the splash while it can be skipped.
const SkipHint = "any key: skip"

// Splash is the splash screen's state. It is driven entirely by its caller:
// Start and Advance take times from an injected monotonic clock, Skip is a key
// press, Resize a new terminal size. It holds for Duration, animated only when
// the capabilities allow, and is done when the hold ends or a key skips it.
type Splash struct {
	opts      Options
	art       terminal.Art
	animating bool
	width     int
	height    int
	start     time.Time
	started   bool
	elapsed   time.Duration
	done      bool
	skipped   bool
}

// New returns a splash for the given options. Nothing is shown until Start.
func New(o Options) *Splash {
	s := &Splash{opts: o, art: o.Caps.Splash, width: o.Caps.Width, height: o.Caps.Height}
	if !o.Caps.SizeKnown {
		s.width, s.height = 0, 0
	}
	s.animating = o.Caps.Animate && s.art == terminal.LargeMark
	return s
}

// Start begins the hold at now. A splash with no art is done immediately:
// NoArt never pauses the session.
func (s *Splash) Start(now time.Time) {
	if s.started {
		return
	}
	s.started, s.start = true, now
	if s.art == terminal.NoArt {
		s.done = true
	}
}

// Advance moves the splash to now. Time never runs backwards, and the splash
// is done once Duration has elapsed.
func (s *Splash) Advance(now time.Time) {
	if !s.started || s.done {
		return
	}
	if e := now.Sub(s.start); e > s.elapsed {
		s.elapsed = e
	}
	if s.elapsed >= Duration {
		s.elapsed, s.done = Duration, true
	}
}

// NextTick is the delay from the current elapsed time to the next frame.
// Frames fall on multiples of FrameInterval from the start, so a late tick
// drops the frames it missed rather than replaying them; the last tick lands
// exactly on Duration.
func (s *Splash) NextTick() time.Duration {
	if s.done {
		return 0
	}
	if !s.animating {
		return Duration - s.elapsed
	}
	next := (s.elapsed/FrameInterval + 1) * FrameInterval
	return min(next, Duration) - s.elapsed
}

// Skip ends the splash at once. It reports whether the key was consumed by
// the splash, which is true whenever the splash was still showing; the caller
// must then not deliver the key anywhere else.
func (s *Splash) Skip() bool {
	if !s.started || s.done {
		return false
	}
	s.done, s.skipped = true, true
	return true
}

// Resize applies a new terminal size. Shrinking the terminal stops the
// animation for good (the static mark is shown for the rest of the hold), and
// growing it never replays the animation. The art never grows beyond what the
// session's capabilities chose, and shrinks to what still fits.
func (s *Splash) Resize(width, height int) {
	if width < s.width || height < s.height || width <= 0 || height <= 0 {
		s.animating = false
	}
	s.width, s.height = width, height
	if fit := artFor(s.opts.Caps.Level, width, height); fit < s.art {
		s.art = fit
	}
	if s.art != terminal.LargeMark {
		s.animating = false
	}
}

// artFor mirrors the size thresholds of terminal.Evaluate for a given level.
// TestArtForMatchesEvaluate keeps the two in step.
func artFor(level terminal.Level, width, height int) terminal.Art {
	if width <= 0 || height <= 0 {
		return terminal.TextHeader
	}
	if level >= terminal.L3 && width >= 80 && height >= 20 {
		if height >= 24 {
			return terminal.LargeMark
		}
		return terminal.CompactMark
	}
	return terminal.TextHeader
}

// Done reports whether the hold ended or a key skipped it.
func (s *Splash) Done() bool { return s.done }

// Skipped reports whether a key ended the splash.
func (s *Splash) Skipped() bool { return s.skipped }

// Animating reports whether frames are still animated.
func (s *Splash) Animating() bool { return s.animating }

// Elapsed is the time shown, clamped to Duration.
func (s *Splash) Elapsed() time.Duration { return s.elapsed }

// Art is the art currently shown.
func (s *Splash) Art() terminal.Art { return s.art }

// View renders the splash centred in the terminal. Every line is centred by
// its display width, so styled runs never shift a row.
func (s *Splash) View() string {
	if s.art == terminal.NoArt {
		return ""
	}
	profile := s.opts.Caps.ColorDepth
	if s.opts.Caps.NoColor {
		profile = colorprofile.ASCII
	}
	p := newPainter(profile)
	var lines []string
	wordmark := true
	switch s.art {
	case terminal.LargeMark, terminal.CompactMark:
		m := Large()
		if s.art == terminal.CompactMark {
			m = Compact()
		}
		t := time.Duration(-1)
		if s.animating {
			t = s.elapsed
			wordmark = s.elapsed >= SweepEnd
		}
		lines = append(markRows(m, p, t), "")
	}
	// A profile with no colour (NO_COLOR, or a terminal without it) is plain
	// text: no bold or faint either, as the plain-text levels promise.
	bold, dim := lipgloss.NewStyle(), lipgloss.NewStyle()
	if profile > colorprofile.ASCII {
		bold, dim = bold.Bold(true), dim.Faint(true)
	}
	if wordmark {
		lines = append(lines, bold.Render("Device")+p.style(p.convert(lipgloss.Color(ChainColor)), nil).Inherit(bold).Render("Chain"))
	} else {
		lines = append(lines, "")
	}
	if s.opts.Subtitle != "" {
		lines = append(lines, s.opts.Subtitle)
	}
	if s.opts.Version != "" {
		lines = append(lines, dim.Render(s.opts.Version))
	}
	lines = append(lines, "", dim.Render(SkipHint))
	out := make([]string, 0, len(lines)+s.height)
	if top := (s.height - len(lines)) / 2; top > 0 {
		out = append(out, make([]string, top)...)
	}
	for _, line := range lines {
		out = append(out, strings.TrimRight(Center(line, s.width), " "))
	}
	return strings.Join(out, "\n")
}
