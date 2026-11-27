// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/result"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// silence is how long without an observation before the screen says so. It says the
// work is still running: no observation is not evidence that anything has hung.
const silence = 45 * time.Second

// singleColumnLogRows is the log pane's height in the 80-99 column layout.
const singleColumnLogRows = 5

// fallbackWidth and fallbackHeight are used until the terminal reports its size.
const fallbackWidth, fallbackHeight = 80, 24

// frame is one layout's measurements.
type frame struct {
	layout  terminal.Layout
	w, h    int  // the terminal
	border  bool // an outer border (and so a footer rule)
	inner   int  // usable columns inside it
	rows    int  // body rows: everything but the chrome (border, header, footer)
	nameW   int  // the stage-name column
	barW    int  // a stage's bar
	allBarW int  // the overall bar
}

func newFrame(w, h int) frame {
	if w <= 0 || h <= 0 {
		w, h = fallbackWidth, fallbackHeight
	}
	f := frame{layout: terminal.LayoutFor(w, h), w: w, h: h}
	switch f.layout {
	case terminal.Full:
		f.border, f.inner, f.rows = true, w-4, h-4
		f.nameW, f.barW, f.allBarW = 30, min(20, max(10, (w-4)/6)), 30
	case terminal.SingleColumn:
		f.border, f.inner, f.rows = true, w-4, h-4
		f.nameW, f.barW, f.allBarW = 28, 10, 30
	default: // Compact and Minimal: no border art, no logo
		f.inner, f.rows = w-2, h-2 // a header line and a footer line
		f.nameW, f.barW, f.allBarW = 28, 8, 20
	}
	return f
}

// stateWord is a row's state as a word: colour never carries a state alone.
func stateWord(t theme, g groupView) string {
	switch g.state {
	case groupRunning:
		return t.acc.Render("RUN ")
	case groupDone:
		return t.ok.Render("DONE")
	case groupFailed:
		return t.fail.Render("FAIL")
	case groupStopped:
		return t.warn.Render("STOP")
	}
	return t.dim.Render("WAIT")
}

// measured draws cur of total in w cells.
//
// 🔴 A RUNNING STAGE'S BAR IS NEVER FULL. A full count is not success: 63 of 63 actions
// applied is still a stage that has not returned, and its gates may yet fail. Only the
// stage's step.finished fills the bar (see doneBar). And a value is never drawn above
// its total: a snapshot that over-counts is shown at its total, not past it.
func measured(t theme, cur, total int64, w int) (bar, label string) {
	cur = max(min(cur, total), 0)
	fill := 0
	if total > 0 {
		fill = int(cur * int64(w) / total)
	}
	if fill >= w {
		// max: the minimal layout asks for no bar at all (w is 0), only its words.
		fill = max(w-1, 0)
	}
	return paint(t.acc, strings.Repeat(t.g.full, fill)) + paint(t.dim, strings.Repeat(t.g.empty, w-fill)),
		fmt.Sprintf("%d/%d", cur, total)
}

// indeterminate is work with no known total: a moving marker where motion is allowed,
// the word "working" where it is not. 🔴 IT NEVER CARRIES A NUMBER: with no total there
// is no fraction to draw, and a count with nothing to count against reads as one.
func indeterminate(t theme, w, tick int) string {
	if !t.animate || w < 4 {
		return t.acc.Render(pad(truncate("working", w, ""), w))
	}
	const size = 3
	pos := tick % (w - size + 1)
	return paint(t.dim, strings.Repeat(t.g.empty, pos)) + paint(t.acc, strings.Repeat(t.g.marker, size)) +
		paint(t.dim, strings.Repeat(t.g.empty, w-size-pos))
}

func unitWord(u installflow.Unit) string { return string(u) }

// rowBar is a row's bar and the words after it.
func rowBar(t theme, g groupView, w, tick int) (string, string) {
	switch g.state {
	case groupDone:
		word := "done"
		switch g.outcome {
		case installflow.StepUnchanged:
			word = "unchanged"
		case installflow.StepPlanned:
			word = "planned"
		}
		return paint(t.ok, strings.Repeat(t.g.full, w)), word
	case groupWaiting:
		return paint(t.dim, strings.Repeat(t.g.wait, w)), ""
	case groupFailed, groupStopped:
		word := "failed"
		if g.state == groupStopped {
			word = "stopped"
		}
		if g.metric != nil && g.metric.Mode == installflow.ProgressMeasured && g.metric.Total != nil {
			bar, frac := measured(t, g.metric.Current, *g.metric.Total, w)
			return bar, word + " at " + frac + " " + unitWord(g.metric.Unit)
		}
		return paint(t.dim, strings.Repeat(t.g.empty, w)), word
	}
	// Running.
	if g.metric != nil && g.metric.Mode == installflow.ProgressMeasured && g.metric.Total != nil {
		bar, frac := measured(t, g.metric.Current, *g.metric.Total, w)
		return bar, frac + " " + unitWord(g.metric.Unit)
	}
	if g.metric != nil {
		return indeterminate(t, w, tick), "total unknown"
	}
	return indeterminate(t, w, tick), ""
}

// overallBar is completed stages out of applicable stages, labelled as exactly that. It
// is never a percentage, and it is never "installed".
func overallBar(t theme, done, total, w int) string {
	fill := 0
	if total > 0 {
		fill = done * w / total
	}
	return paint(t.ok, strings.Repeat(t.g.full, fill)) + paint(t.dim, strings.Repeat(t.g.empty, w-fill)) +
		fmt.Sprintf("  %d/%d stages", done, total)
}

// clock formats an elapsed time as m:ss or h:mm:ss.
func clock(d time.Duration) string {
	d = max(d, 0).Truncate(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// short formats a duration as 15m, 3m12s or 45s.
func short(d time.Duration) string {
	d = max(d, 0).Truncate(time.Second)
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// progressLines is the screen's body, top to bottom, before the log pane: the overall
// bar, the clock line, the rows and the "Now" block.
func (m model) progressLines(f frame) []string {
	t, s, now := m.theme, &m.progress, m.now()
	var out []string
	gs := s.groups()
	done, total := overall(gs)
	out = append(out, "Overall  "+overallBar(t, done, total, f.allBarW))

	clockLine := "Remaining: unknown"
	if !s.runStarted.IsZero() {
		clockLine = "Elapsed " + clock(now.Sub(s.runStarted)) + " " + t.g.dot + " " + clockLine
	}
	if !s.lastEvent.IsZero() && m.summary == nil {
		quiet := now.Sub(s.lastEvent)
		if quiet >= silence {
			clockLine += fmt.Sprintf(" %s no new observation for %s; work is still running", t.g.dot, short(quiet))
		} else {
			clockLine += fmt.Sprintf(" %s last observation %s ago", t.g.dot, short(quiet))
		}
	}
	out = append(out, t.dim.Render(truncate(clockLine, f.inner, t.g.ellipsis)), "")

	if !s.manifest {
		out = append(out, t.dim.Render("Starting"+t.g.ellipsis))
	}
	for _, g := range gs {
		out = append(out, m.row(f, g))
	}
	out = append(out, "")
	out = append(out, m.nowBlock(f, done, total)...)
	return out
}

// row is one stage row. Its label carries the engine's unit word, so the row is checked
// whole, as text, and drawn without the label when the label is what makes it unsafe.
func (m model) row(f frame, g groupView) string {
	line := (result.Input{Redact: m.redact}).Line
	compose := func(t theme, label bool) string {
		bar, words := rowBar(t, g, f.barW, m.tick)
		labelW := f.inner - 4 - 2 - f.nameW - 2 - f.barW - 2
		row := stateWord(t, g) + "  " + pad(truncate(g.name, f.nameW, t.g.ellipsis), f.nameW) + "  " + bar
		if label && words != "" && labelW > 0 {
			row += "  " + truncate(words, labelW, t.g.ellipsis)
		}
		return row
	}
	plain := m.theme.plain()
	full, bare := compose(plain, true), compose(plain, false)
	switch checked := line(full, bare); checked {
	case full:
		return compose(m.theme, true)
	case bare:
		return compose(m.theme, false)
	default:
		return checked
	}
}

// nowBlock is the two lines under the rows: what is running and its numbers, or what
// the run is waiting for.
func (m model) nowBlock(f frame, done, total int) []string {
	line := (result.Input{Redact: m.redact}).Line
	t, now := m.theme, m.now()
	fit := func(s string) string { return truncate(s, f.inner, t.g.ellipsis) }
	switch {
	case m.prompt != nil:
		q := line(fit(questionWords(m.prompt.q, false)+" [y/N]"), fit(questionWords(m.prompt.q, true)+" [y/N]"))
		return []string{t.bold.Render(q), ""}
	case m.stopping && m.summary == nil:
		return []string{t.warn.Render(fit("Interrupt received. Waiting for the operation to stop safely. Nothing is being rolled back.")),
			fit("Press Ctrl+C again to exit immediately without waiting.")}
	case m.summary != nil:
		second := ""
		if !m.caps.ProgressOnly {
			second = "Press Enter to exit."
			if m.continues && m.summary.Succeeded() {
				second = "Press Enter to continue."
			}
		}
		return []string{t.bold.Render(fit(m.runWord() + " " + outcomeWord(m.summary.Outcome) + ".")), second}
	}
	v := m.progress.activeStep()
	if v == nil {
		return []string{"", ""}
	}
	if v.phase != phaseRunning {
		if total > 0 && done == total {
			// Every stage is closed, but only the summary says how the run ended.
			return []string{t.dim.Render("Now") + "  " + fit("Finalizing"), ""}
		}
		return []string{"", ""}
	}
	// A stage this table does not know shows by its ID, which the engine sent: the line
	// is checked as composed, label included.
	head := line("Now  "+truncate(result.StageName(v.id), f.inner-5, t.g.ellipsis), "Now  "+withheld)
	if rest, ok := strings.CutPrefix(head, "Now"); ok {
		head = t.dim.Render("Now") + rest
	}
	var parts []string
	if mt := v.metric; mt != nil && mt.Mode == installflow.ProgressMeasured && mt.Total != nil {
		cur := max(min(mt.Current, *mt.Total), 0)
		parts = append(parts, fmt.Sprintf("%d of %d %s", cur, *mt.Total, unitWord(mt.Unit)))
	} else if mt != nil {
		parts = append(parts, unitWord(mt.Unit)+": total unknown")
	}
	if v.budget > 0 {
		parts = append(parts, "limit "+short(v.budget))
	}
	if !v.started.IsZero() {
		parts = append(parts, "waited "+short(now.Sub(v.started)))
	}
	if v.attempt > 1 {
		parts = append(parts, fmt.Sprintf("attempt %d", v.attempt))
	}
	detail := func(ps []string) string {
		return truncate("     "+strings.Join(ps, " "+t.g.dot+" "), f.inner, t.g.ellipsis)
	}
	// The first part carries the engine's unit word; the fallback is the line without it.
	fallback := detail(parts)
	if len(parts) > 0 && v.metric != nil {
		fallback = detail(parts[1:])
	}
	return []string{head, t.dim.Render(line(detail(parts), fallback))}
}

// footer is generated from the key map that is active now, never written by hand.
func (m model) footer(f frame) string {
	var parts []string
	for _, b := range m.keys.active(m) {
		h := b.Help()
		parts = append(parts, m.theme.dim.Render(h.Key)+" "+h.Desc)
	}
	return truncateStyled(strings.Join(parts, "  "), f.inner)
}

// truncateStyled keeps a styled line to w cells by dropping whole trailing items rather
// than cutting through an escape sequence.
func truncateStyled(s string, w int) string {
	for width(s) > w {
		i := strings.LastIndex(s, "  ")
		if i < 0 {
			return ""
		}
		s = s[:i]
	}
	return s
}

// renderProgress lays the screen out in exactly the terminal's rows.
func (m model) renderProgress() string {
	line := (result.Input{Redact: m.redact}).Line
	f := newFrame(m.width, m.height)
	t := m.theme
	if f.layout == terminal.Minimal && !m.help.open {
		return m.renderMinimal(f)
	}
	var body []string
	if m.help.open {
		// Help is drawn in the body's place, at every layout: the minimal one too, without
		// a border, so it can be read and scrolled at any size.
		body = m.helpBody(f)
	} else {
		body = m.progressLines(f)
		pane := m.pane // render records the rows it was given; the model's copy is set in Update
		body = append(body, pane.render(m.logs, t, f.inner, paneHeight(f, len(body), m.pane.open), m.loc, line)...)
	}
	for len(body) < f.rows {
		body = append(body, "")
	}
	footer := m.footer(f)

	if !f.border {
		compose := func(target string) string {
			return "DeviceChain " + t.g.dot + " " + truncate(m.titleFor(target), f.inner-14, t.g.ellipsis)
		}
		head := line(compose(m.progress.target), compose(withheld))
		if rest, ok := strings.CutPrefix(head, "DeviceChain"); ok {
			head = t.bold.Render("DeviceChain") + rest
		}
		lines := append([]string{head}, body...)
		lines = append(lines, footer)
		for i := range lines {
			lines[i] = " " + lines[i]
		}
		return strings.Join(clip(lines, f.h), "\n")
	}
	g := t.g
	// The top border is plain text: it is checked whole, border and label included.
	top := func(target string) string {
		title := truncate(" DeviceChain "+g.dot+" "+m.titleFor(target)+" ", f.w-4, g.ellipsis)
		return g.tl + g.h + title + strings.Repeat(g.h, max(f.w-3-width(title), 0)) + g.tr
	}
	lines := []string{line(top(m.progress.target), top(withheld))}
	for _, l := range body {
		lines = append(lines, g.v+" "+pad(l, f.inner)+" "+g.v)
	}
	lines = append(lines, g.teeL+strings.Repeat(g.h, f.w-2)+g.teeR)
	lines = append(lines, g.v+" "+pad(footer, f.inner)+" "+g.v)
	lines = append(lines, g.bl+strings.Repeat(g.h, f.w-2)+g.br)
	return strings.Join(clip(lines, f.h), "\n")
}

// paneHeight is the rows the log pane gets under a body of bodyLen rows: what is left,
// at most five lines in the single-column layout, and one row when collapsed.
func paneHeight(f frame, bodyLen int, open bool) int {
	rows := f.rows - bodyLen
	if f.layout == terminal.SingleColumn {
		rows = min(rows, singleColumnLogRows+2) // the lines, a header and a status line
	}
	if !open || rows < 3 {
		return 1
	}
	return rows
}

// logRows is how many log lines the pane shows in the current frame, for paging.
func (m model) logRows() int {
	f := newFrame(m.width, m.height)
	return max(paneHeight(f, len(m.progressLines(f)), true)-2, 1)
}

// renderMinimal is the status block for a terminal under 60x16. Resizing changes only
// this drawing; it never cancels or restarts anything.
func (m model) renderMinimal(f frame) string {
	line := (result.Input{Redact: m.redact}).Line
	t := m.theme
	gs := m.progress.groups()
	done, total := overall(gs)
	lines := []string{t.bold.Render("DeviceChain " + strings.ToLower(m.runWord())), fmt.Sprintf("%d/%d stages", done, total)}
	for _, g := range gs {
		if g.state == groupRunning || g.state == groupFailed || g.state == groupStopped {
			_, label := rowBar(t, g, 0, 0)
			compose := func(t theme, label string) string {
				return stateWord(t, g) + " " + truncate(g.name+" "+label, max(f.w-5, 1), t.g.ellipsis)
			}
			full, bare := compose(t.plain(), label), compose(t.plain(), "")
			switch checked := line(full, bare); checked {
			case full:
				lines = append(lines, compose(t, label))
			case bare:
				lines = append(lines, compose(t, ""))
			default:
				lines = append(lines, checked)
			}
		}
	}
	now := m.nowBlock(f, done, total)
	if m.prompt != nil || m.stopping || m.summary != nil {
		lines = append(lines, now...)
	}
	return strings.Join(clip(lines, f.h), "\n")
}

// titleFor is the screen's title naming target, the run.started target context. It is
// caller-supplied text, so the line it is composed into is checked whole
// (see result.Input.Line).
func (m model) titleFor(target string) string {
	verb := "Installing"
	if m.op == installflow.OperationBootstrap {
		verb = "Creating the instance"
	}
	if target != "" {
		return verb + " " + m.theme.g.dot + " " + target
	}
	return verb
}

// runWord names the run in its verdict: the install, or the bootstrap.
func (m model) runWord() string {
	if m.op == installflow.OperationBootstrap {
		return "Bootstrap"
	}
	return "Install"
}

func clip(lines []string, h int) []string {
	if len(lines) > h {
		return lines[:h]
	}
	return lines
}
