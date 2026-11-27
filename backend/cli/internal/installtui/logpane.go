// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// logSource is the hub's log ring as the screen reads it. The screen holds a position
// in the ring, never a copy of it: each frame copies only the rows it draws, so a ring
// full of a flood costs a frame no more than an empty one, and nothing the screen does
// can hold up the writers filling it (the hub's LogWriter never waits on a presenter).
type logSource interface {
	LogWindow(from uint64, n int, tail bool) installflow.LogWindow
}

// logPane is the collapsible pane over the ring. Following, it shows the newest lines;
// PgUp pauses it where it is (new lines keep arriving below, counted, not drawn) and f
// or End follows again.
type logPane struct {
	open   bool
	follow bool
	top    uint64 // absolute line number of the first row, while paused
	rows   int    // rows the last frame gave the lines, for paging
}

func newLogPane(open bool) logPane { return logPane{open: open, follow: true} }

func (p *logPane) toggle() { p.open = !p.open }

func (p *logPane) pageUp(src logSource) {
	if src == nil {
		return
	}
	p.open = true
	rows := max(p.rows, 1)
	if p.follow {
		p.top = src.LogWindow(0, rows, true).From
		p.follow = false
	}
	if p.top > uint64(rows) {
		p.top -= uint64(rows)
	} else {
		p.top = 0
	}
	p.top = src.LogWindow(p.top, rows, false).From // clamp to what is still kept
}

func (p *logPane) pageDown(src logSource) {
	if src == nil || p.follow {
		return
	}
	rows := max(p.rows, 1)
	w := src.LogWindow(p.top+uint64(rows), rows, false)
	p.top = w.From
	if w.From+uint64(len(w.Lines)) >= w.End {
		p.follow = true // paged back down to the newest line
	}
}

func (p *logPane) followTail() { p.follow = true }

// render draws the pane in exactly height rows (or one, collapsed) of width cells.
//
// line is result.Input.Line: every log row is checked whole, its
// time and source included, and drawn with its text withheld when it may not be drawn
// as composed.
func (p *logPane) render(src logSource, t theme, cols, height int, loc *time.Location, line func(s, fallback string) string) []string {
	if src == nil {
		return nil
	}
	if !p.open || height < 3 {
		w := src.LogWindow(0, 0, true)
		kept := w.End - w.First
		return []string{fmt.Sprintf("%s Logs %s", t.g.collapsed,
			t.dim.Render(truncate(fmt.Sprintf("(collapsed %s %d lines kept %s l to open)", t.g.dot, kept, t.g.dot), max(cols-7, 0), t.g.ellipsis)))}
	}
	rows := height - 2 // a header and a status line around the lines
	p.rows = rows
	win := src.LogWindow(p.top, rows, p.follow)
	if !p.follow {
		p.top = win.From
	}
	mode := "following"
	if !p.follow {
		mode = "paused " + t.g.dot + " f to follow"
	}
	out := []string{fmt.Sprintf("%s Logs %s", t.g.expanded, t.dim.Render(t.g.dot+" sanitized "+t.g.dot+" "+mode))}
	for _, l := range win.Lines {
		out = append(out, logRow(l, t, cols, loc, line))
	}
	for len(out) < rows+1 {
		out = append(out, "")
	}
	newer := win.End - (win.From + uint64(len(win.Lines)))
	status := fmt.Sprintf("%d earlier lines kept %s %d aged out", win.From-win.First, t.g.dot, win.First)
	if newer > 0 {
		status += fmt.Sprintf(" %s %d newer", t.g.dot, newer)
	}
	out = append(out, t.dim.Render(truncate(t.g.ellipsis+" "+status, cols, t.g.ellipsis)))
	return out
}

func logRow(l installflow.LogEntry, t theme, cols int, loc *time.Location, check func(s, fallback string) string) string {
	stamp := l.Time.In(loc).Format("15:04:05")
	src := truncate(l.Source, 7, "")
	text := strings.ReplaceAll(l.Text.String(), "\t", " ")
	rest := cols - width(stamp) - 1 - 7 - 1
	text = truncate(text, rest, t.g.ellipsis)
	prefix := stamp + " " + pad(src, 7) + " "
	switch checked := check(prefix+text, prefix+withheld); checked {
	case prefix + text:
	case prefix + withheld:
		text = withheld
	default:
		return checked
	}
	if l.Level == installflow.LogWarn || l.Level == installflow.LogError {
		text = t.warn.Render(text)
	}
	return t.dim.Render(stamp) + " " + pad(src, 7) + " " + text
}
