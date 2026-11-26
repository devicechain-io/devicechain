// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package brand

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/devicechain-io/dcctl/internal/installtui/brand/gen"
)

// The animation's beats. The terminal version of the Unity intro runs once,
// within Duration, and every frame is a pure function of the elapsed time.
const (
	// TraceEnd: the rim has traced clockwise from the top; the cube is dim.
	TraceEnd = 250 * time.Millisecond
	// SweepEnd: the facets have brightened under one diagonal light band.
	SweepEnd = 600 * time.Millisecond
	// LockEnd: the mark is in its canonical colours and the wordmark is up.
	LockEnd = 850 * time.Millisecond
	// Duration ends the still hold and hands off to the next screen.
	Duration = 1200 * time.Millisecond
	// FrameInterval keeps redraws at or below 12 a second.
	FrameInterval = 84 * time.Millisecond
)

// ChainColor is the brand colour of "Chain" in the wordmark.
const ChainColor = "#208CB7"

// background is the dark backdrop the dimmed cube fades from during the
// animation. The static mark and every frame from LockEnd on use only the
// canonical facet colours.
var background = [3]uint8{0x0B, 0x16, 0x20}

type rgb = [3]uint8

// pixelColor is the colour of pixel (x, y) of facet k at elapsed time t. The
// second result is false while the rim trace has not yet reached the pixel.
// static (t < 0) is the canonical mark.
func pixelColor(m Mark, k uint8, x, y int, t time.Duration) (rgb, bool) {
	f := facets[k-1]
	base := f.RGB
	if t < 0 || t >= LockEnd {
		return base, true
	}
	ms := float64(t) / float64(time.Millisecond)
	rim := f.Part == gen.Rim
	c := float64(m.Pixels-1) / 2
	if rim {
		// 0 degrees is the top, increasing clockwise.
		angle := math.Mod(math.Atan2(float64(x)-c, -(float64(y)-c))*180/math.Pi+360, 360)
		if angle > math.Min(1, ms/250)*360 {
			return base, false
		}
	} else {
		switch {
		case t < TraceEnd:
			return lerp(base, background, 0.75), true
		case t < SweepEnd:
			base = lerp(base, background, 0.75*(1-(ms-250)/350))
		}
	}
	if t >= TraceEnd && t < SweepEnd {
		band := (ms-250)/350*float64(2*m.Pixels) - float64(m.Pixels)/2
		if d := math.Abs(float64(x+y) - band); d < 3 {
			base = lerp(base, rgb{255, 255, 255}, 0.45*(1-d/3))
		}
	}
	if t >= SweepEnd && rim {
		base = lerp(base, rgb{255, 255, 255}, 0.22*(1-(ms-600)/250))
	}
	return base, true
}

func lerp(a, b rgb, t float64) rgb {
	var out rgb
	for i := range out {
		out[i] = uint8(math.RoundToEven(float64(a[i]) + (float64(b[i])-float64(a[i]))*t))
	}
	return out
}

// painter turns colours into styled runs for one colour profile. The profile
// is always explicit; Lip Gloss never probes the terminal.
type painter struct {
	r       *lipgloss.Renderer
	profile termenv.Profile
}

func newPainter(p termenv.Profile) painter {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(p)
	return painter{r: r, profile: p}
}

// color is the profile's colour for c. ANSI256 uses the pinned nearest-colour
// mapping from the generator, so the canonical facets match the manifest.
func (p painter) color(c rgb) lipgloss.Color {
	if p.profile == termenv.ANSI256 {
		return lipgloss.Color(strconv.Itoa(gen.Nearest256(c[0], c[1], c[2])))
	}
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", c[0], c[1], c[2]))
}

// markRows renders the mark at elapsed time t (t < 0 is the static mark).
// Every row is exactly m.Cols cells wide: transparent cells are plain spaces,
// so the block's left edge is the same on every row. Runs with the same style
// are coalesced to keep the output small.
func markRows(m Mark, p painter, t time.Duration) []string {
	rows := make([]string, 0, m.Rows)
	for r, cells := range m.Cells {
		var b strings.Builder
		var run strings.Builder
		var runStyle string
		var style lipgloss.Style
		flush := func() {
			if run.Len() == 0 {
				return
			}
			if runStyle == "" {
				b.WriteString(run.String())
			} else {
				b.WriteString(style.Render(run.String()))
			}
			run.Reset()
		}
		for x, cell := range cells {
			var top, bottom *rgb
			if cell.Top != 0 {
				if c, ok := pixelColor(m, cell.Top, x, 2*r, t); ok {
					top = &c
				}
			}
			if cell.Bottom != 0 {
				if c, ok := pixelColor(m, cell.Bottom, x, 2*r+1, t); ok {
					bottom = &c
				}
			}
			glyph, key := " ", ""
			var next lipgloss.Style
			switch {
			case top == nil && bottom == nil:
			case bottom == nil:
				glyph, key = "▀", fmt.Sprint("f", *top)
				next = p.r.NewStyle().Foreground(p.color(*top))
			case top == nil:
				glyph, key = "▄", fmt.Sprint("f", *bottom)
				next = p.r.NewStyle().Foreground(p.color(*bottom))
			case *top == *bottom:
				glyph, key = "█", fmt.Sprint("f", *top)
				next = p.r.NewStyle().Foreground(p.color(*top))
			default:
				glyph, key = "▀", fmt.Sprint("f", *top, "b", *bottom)
				next = p.r.NewStyle().Foreground(p.color(*top)).Background(p.color(*bottom))
			}
			if key != runStyle {
				flush()
				runStyle, style = key, next
			}
			run.WriteString(glyph)
		}
		flush()
		rows = append(rows, b.String())
	}
	return rows
}

// Static renders the canonical mark for profile p, one string per cell row,
// each exactly m.Cols cells wide.
func Static(m Mark, p termenv.Profile) []string { return markRows(m, newPainter(p), -1) }

// Frame renders the animation at elapsed time t, clamped to [0, Duration].
func Frame(m Mark, p termenv.Profile, t time.Duration) []string {
	return markRows(m, newPainter(p), min(max(t, 0), Duration))
}

// Center places line in the middle of a field width cells wide, measuring the
// line by its display width (escape sequences and wide runes counted as they
// appear, never by bytes). Trailing padding is not emitted.
func Center(line string, width int) string {
	pad := (width - lipgloss.Width(line)) / 2
	if pad <= 0 {
		return line
	}
	return strings.Repeat(" ", pad) + line
}
