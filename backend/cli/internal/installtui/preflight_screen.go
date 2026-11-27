// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
)

// The preflight screen's words.
const (
	preflightTitle   = "Preflight"
	preflightRunning = "working: the read-only checks are running"
	preflightAllPass = "Preflight: all checks passed."
	// preflightBlocked is said whenever Continue is unavailable because of a failure.
	// There is no way past a failure on this screen: fix it, then Recheck.
	preflightBlocked  = "Continue is unavailable while any check fails. Fix each failure, then Recheck."
	preflightSkipped  = "--skip-preflight supplied: the checks were not run, here or by the install."
	preflightNoRunner = "the preflight checks are not available to this screen"
	verdictColumn     = 11 // the widest verdict word, NOT CHECKED
)

// What each footprint means, shown before a switch. The words are the flags' own.
const (
	consequencesCompact = "Compact is the small-footprint preset for the cluster and every instance on it: " +
		"smaller volumes, lowered JetStream/KV ceilings and scheduling requests, no monitoring stack unless " +
		"--no-monitoring=false keeps it, and, unless --no-tls=false, no cert-manager and therefore no database " +
		"backups. The cluster choices show what this plan keeps."
	consequencesStandard = "Standard is the full-size footprint. A local kind cluster then needs Docker to " +
		"have at least 4 CPUs and 8 GiB of memory."
	consequencesRecheck = "The plan changes to this footprint and every check runs again for it."
)

// The preflight screen's actions.
type pfAction uint8

const (
	actContinue pfAction = iota
	actRecheck
	actFootprint
	actContext
	actExit
)

// checkMsg is one step of a preflight run on the screen: an observation, or the end.
// gen is the run it belongs to; a message from a run that has been replaced is dropped.
type checkMsg struct {
	gen  int
	obs  *installflow.PreflightObservation
	done bool
	res  installflow.PreflightResult
	err  error
	next <-chan checkMsg
}

// preflightState is the preflight screen.
type preflightState struct {
	started bool
	gen     int
	running bool
	// finished: the current run has ended (with a result or an error).
	finished bool
	rows     []installflow.PreflightObservation
	summary  *installflow.PreflightObservation
	result   installflow.PreflightResult
	err      string
	// focus is the index in rows of the focused check, or -1.
	focus   int
	action  pfAction
	confirm bool
	// stop cancels the run in flight, when one is: a run whose plan has been replaced
	// is stopped, not left to finish unseen.
	stop context.CancelFunc
}

func newPreflightState() preflightState {
	return preflightState{focus: -1, action: actRecheck}
}

func (s *preflightState) checkCount() int {
	n := 0
	for _, r := range s.rows {
		if r.Observation == installflow.ObserveCheck {
			n++
		}
	}
	return n
}

// failed reports whether anything the run said is a failure: a check's verdict, the
// runner's count, or the closing tally. Each is enough on its own.
func (s *preflightState) failed() bool {
	if s.result.Fails > 0 || (s.summary != nil && s.summary.Fails > 0) {
		return true
	}
	for _, r := range s.rows {
		if r.Observation == installflow.ObserveCheck && r.Verdict == installflow.CheckFail {
			return true
		}
	}
	return false
}

// canContinue is the one gate on Continue. 🔴 A FAILURE BLOCKS IT, AND NOTHING ON THIS
// SCREEN BYPASSES THAT: not a warning count, not a recheck in progress, not a runner that
// errored or was never given. --skip-preflight is the command line's own, explicit way
// past the checks, and it is the only one.
func (p *prerun) canContinue() bool {
	// A --skip-preflight run is finished at once, with nothing failed.
	s := &p.pf
	return s.finished && !s.running && s.err == "" && !s.failed()
}

// actions is the screen's action row as it stands: each action and whether it can be
// taken now.
func (p *prerun) actions() ([]pfAction, []bool) {
	acts := []pfAction{actContinue}
	if !p.facts.SkipPreflight {
		acts = append(acts, actRecheck)
	}
	if p.replan != nil {
		acts = append(acts, actFootprint)
		if p.contexts != nil {
			acts = append(acts, actContext)
		}
	}
	acts = append(acts, actExit)
	enabled := make([]bool, len(acts))
	for i, a := range acts {
		switch a {
		case actContinue:
			enabled[i] = p.canContinue()
		case actRecheck, actFootprint, actContext:
			enabled[i] = !p.pf.running
		default:
			enabled[i] = true
		}
	}
	return acts, enabled
}

func actionLabel(a pfAction, compact bool) string {
	switch a {
	case actContinue:
		return "Continue"
	case actRecheck:
		return "Recheck"
	case actFootprint:
		return "Switch to " + footprintWord(!compact)
	case actContext:
		return "Choose another context"
	}
	return "Exit"
}

// settleAction moves the action focus off anything that cannot be taken now, to the
// first action that can, never to Continue: a key typed while the checks ran must not
// be what starts the install.
func (p *prerun) settleAction() {
	acts, enabled := p.actions()
	for i, a := range acts {
		if a == p.pf.action && enabled[i] {
			return
		}
	}
	for i, a := range acts {
		if enabled[i] && a != actContinue {
			p.pf.action = a
			return
		}
	}
}

func (p *prerun) moveAction(delta int) {
	acts, enabled := p.actions()
	cur := 0
	for i, a := range acts {
		if a == p.pf.action {
			cur = i
		}
	}
	for range acts {
		cur = (cur + delta + len(acts)) % len(acts)
		if enabled[cur] {
			p.pf.action = acts[cur]
			return
		}
	}
}

func (p *prerun) moveFocus(delta int) {
	s := &p.pf
	var checks []int
	at := -1
	for i, r := range s.rows {
		if r.Observation == installflow.ObserveCheck {
			if i == s.focus {
				at = len(checks)
			}
			checks = append(checks, i)
		}
	}
	if len(checks) == 0 {
		return
	}
	switch {
	case at < 0 && delta > 0:
		at = 0
	case at < 0:
		at = len(checks) - 1
	default:
		at = min(max(at+delta, 0), len(checks)-1)
	}
	s.focus = checks[at]
}

func (p *prerun) preflightKey(k tea.KeyPressMsg) (tea.Cmd, prerunOutcome) {
	s := &p.pf
	if s.confirm {
		switch {
		case key.Matches(k, p.keys.confirm):
			s.confirm = false
			return p.switchFootprint(), stay
		case key.Matches(k, p.keys.keep):
			s.confirm = false
		}
		return nil, stay
	}
	switch {
	case key.Matches(k, p.keys.up):
		p.moveFocus(-1)
	case key.Matches(k, p.keys.down):
		p.moveFocus(1)
	case key.Matches(k, p.keys.next):
		p.moveAction(1)
	case key.Matches(k, p.keys.prev):
		p.moveAction(-1)
	case key.Matches(k, p.keys.exit):
		s.action = actExit
	case key.Matches(k, p.keys.back):
		p.screen = screenWelcome
	case key.Matches(k, p.keys.enter):
		acts, enabled := p.actions()
		for i, a := range acts {
			if a != s.action || !enabled[i] {
				continue
			}
			switch a {
			case actContinue:
				// The cluster choices come next; the run starts from there.
				p.screen = screenChoices
				p.ch.focus = 0
			case actRecheck:
				return p.startChecks(), stay
			case actFootprint:
				p.refusal = ""
				s.confirm = true
			case actContext:
				p.openPicker()
			case actExit:
				return nil, leave
			}
		}
	}
	return nil, stay
}

// switchFootprint changes the plan to the other footprint and runs every check again
// for it. A refusal leaves the plan, and the results on screen, as they were.
func (p *prerun) switchFootprint() tea.Cmd {
	want := !p.facts.Compact
	return p.applyEdit(func(e *Edits) { e.Compact = &want })
}

// applyEdit makes one change to the plan: the edits so far with change applied,
// resolved again through the command's own resolver. A refusal leaves the plan, and the
// preflight on screen, exactly as they were.
//
// 🔴 A CHANGED PLAN HAS NO PREFLIGHT UNTIL ITS OWN HAS RUN. The results on screen were
// taken for the plan being replaced (another cluster, another footprint, another
// topology), so they are dropped here, at the one place a plan changes, and the checks
// start again for the new one. Continue stays unavailable until they have passed.
func (p *prerun) applyEdit(change func(*Edits)) tea.Cmd {
	if p.replan == nil {
		return nil
	}
	next := p.edits.clone()
	change(&next)
	r, err := p.replan(next)
	if err != nil {
		p.refusal = p.text(err.Error())
		return nil
	}
	p.refusal = ""
	p.edits, p.facts, p.settings = next, r.Facts, r.Settings
	p.engine, p.runner = r.Engine, r.Preflight
	return p.startChecks()
}

// startChecks starts a fresh preflight run for the plan as it stands. The rows of the
// previous run are cleared at once: a result never outlives the plan it was taken for.
func (p *prerun) startChecks() tea.Cmd {
	s := &p.pf
	if s.stop != nil {
		s.stop()
		s.stop = nil
	}
	s.started = true
	s.gen++
	s.rows, s.summary, s.result, s.err = nil, nil, installflow.PreflightResult{}, ""
	s.focus, s.finished = -1, false
	if p.facts.SkipPreflight {
		s.running, s.finished = false, true
		p.settleAction()
		return nil
	}
	if p.runner == nil {
		s.running, s.finished, s.err = false, true, preflightNoRunner
		p.settleAction()
		return nil
	}
	// Recheck keeps the focus while the checks run, unavailable: an Enter pressed then
	// does nothing at all.
	s.running, s.action = true, actRecheck
	ctx, stop := context.WithCancel(p.ctx)
	s.stop = stop
	p.checks.Add(1)
	// The screen runs the bound form, under its own context: a replaced plan's
	// checks, the doctor's host commands included, end when this run is replaced.
	return runChecks(ctx, s.gen, screenRunner(p.runner), p.checks.Done)
}

// contextBound is a preflight that can also run with its commands bound to the
// context it is given, so that ending the context ends them. The install's own
// preflight stage runs the unbound form, which prints what it always printed even
// when the run is interrupted; the screen, which an Exit or an interrupt abandons,
// runs the bound one.
type contextBound interface {
	BoundToContext() installflow.PreflightRunner
}

// screenRunner is the form of r the screen runs.
func screenRunner(r installflow.PreflightRunner) installflow.PreflightRunner {
	if b, ok := r.(contextBound); ok {
		return b.BoundToContext()
	}
	return r
}

// runChecks runs the preflight on its own goroutine and streams what it observes back
// to the program, one message at a time. The checks are the doctor's, read-only; the
// screen only draws what they say.
func runChecks(ctx context.Context, gen int, runner installflow.PreflightRunner, done func()) tea.Cmd {
	ch := make(chan checkMsg, 16)
	go func() {
		defer done()
		defer close(ch)
		send := func(m checkMsg) bool {
			select {
			case ch <- m:
				return true
			case <-ctx.Done():
				return false
			}
		}
		res, err := func() (res installflow.PreflightResult, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = errors.New("the preflight checks stopped unexpectedly")
				}
			}()
			return runner.RunPreflight(ctx, func(o installflow.PreflightObservation) {
				send(checkMsg{gen: gen, obs: &o})
			})
		}()
		send(checkMsg{gen: gen, done: true, res: res, err: err})
	}()
	return waitCheck(ch)
}

func waitCheck(ch <-chan checkMsg) tea.Cmd {
	return func() tea.Msg {
		m, ok := <-ch
		if !ok {
			return nil
		}
		m.next = ch
		return m
	}
}

func (p *prerun) checkResult(m checkMsg) tea.Cmd {
	s := &p.pf
	var next tea.Cmd
	if !m.done && m.next != nil {
		next = waitCheck(m.next)
	}
	if m.gen != s.gen {
		return next // a run that has been replaced: drain it, show none of it
	}
	switch {
	case m.done:
		s.running, s.finished = false, true
		if s.stop != nil {
			s.stop()
			s.stop = nil
		}
		s.result = m.res
		if m.err != nil {
			s.err = p.text(m.err.Error())
		}
		p.focusWorst()
		s.action = actRecheck
		p.settleAction()
	case m.obs.Observation == installflow.ObserveSummary:
		o := *m.obs
		s.summary = &o
	default:
		s.rows = append(s.rows, *m.obs)
	}
	return next
}

// focusWorst focuses the first failure, else the first warning, else the first check
// whose state is not known, so the fix that matters most is open without a key press.
func (p *prerun) focusWorst() {
	s := &p.pf
	for _, want := range []installflow.CheckVerdict{installflow.CheckFail, installflow.CheckWarn,
		installflow.CheckUnknown, installflow.CheckNotChecked} {
		for i, r := range s.rows {
			if r.Observation == installflow.ObserveCheck && r.Verdict == want {
				s.focus = i
				return
			}
		}
	}
}

// ---- view ---------------------------------------------------------------------

// verdictWord is a verdict as the screen shows it: always a word, so no state depends
// on colour.
func verdictWord(v installflow.CheckVerdict) string {
	switch v {
	case installflow.CheckPass:
		return "PASS"
	case installflow.CheckInfo:
		return "INFO"
	case installflow.CheckWarn:
		return "WARN"
	case installflow.CheckFail:
		return "FAIL"
	case installflow.CheckUnknown:
		return "UNKNOWN"
	case installflow.CheckNotNeeded:
		return "NOT NEEDED"
	case installflow.CheckNotChecked:
		return "NOT CHECKED"
	}
	// A verdict this screen has no word for is shown by its own name, never as a pass.
	return strings.ToUpper(string(v))
}

func (p *prerun) verdictStyle(v installflow.CheckVerdict) lipgloss.Style {
	t := p.theme
	switch v {
	case installflow.CheckPass:
		return t.OK
	case installflow.CheckFail:
		return t.Fail
	case installflow.CheckWarn, installflow.CheckUnknown, installflow.CheckNotChecked:
		return t.Warn
	}
	return t.Dim
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// tally is the closing line: the run's counts, never fewer failures than the rows show.
func (p *prerun) tally() (string, lipgloss.Style) {
	s := &p.pf
	fails, warns := s.result.Fails, s.result.Warns
	if s.summary != nil {
		fails, warns = max(fails, s.summary.Fails), max(warns, s.summary.Warns)
	}
	var rowFails, rowWarns, unknown int
	for _, r := range s.rows {
		switch {
		case r.Observation != installflow.ObserveCheck:
		case r.Verdict == installflow.CheckFail:
			rowFails++
		case r.Verdict == installflow.CheckWarn:
			rowWarns++
		case r.Verdict == installflow.CheckUnknown || r.Verdict == installflow.CheckNotChecked:
			unknown++
		}
	}
	fails, warns = max(fails, rowFails), max(warns, rowWarns)
	// A check whose state is not known is never counted as passed.
	var open []string
	if warns > 0 {
		open = append(open, plural(warns, "warning", "warnings"))
	}
	if unknown > 0 {
		open = append(open, plural(unknown, "check", "checks")+" not determined")
	}
	switch {
	case fails > 0:
		return fmt.Sprintf("Preflight: %s, %s. %s", plural(fails, "failure", "failures"),
			plural(warns, "warning", "warnings"), preflightBlocked), p.theme.Fail
	case len(open) > 0:
		return "Preflight: ready, with " + strings.Join(open, " and ") + ". Review them before you continue.",
			p.theme.Warn
	}
	return preflightAllPass, p.theme.OK
}

// rowLines draws one row: a section heading, or a check with its verdict word, and, when
// it is focused, its fix text in full.
func (p *prerun) rowLines(i int, r installflow.PreflightObservation, w int) []string {
	t := p.theme
	if r.Observation == installflow.ObserveSection {
		return []string{t.Bold.Render(truncCells(p.text(installplain.SectionTitle(r.Section)), w, t.Glyphs.Ellipsis))}
	}
	focused := i == p.pf.focus
	mark := "  "
	if focused {
		mark = t.Glyphs.Focus + " "
	}
	word := verdictWord(r.Verdict)
	styled := p.verdictStyle(r.Verdict).Render(word)
	if focused {
		styled = p.theme.Focus.Render(word)
	}
	lead := mark + styled + strings.Repeat(" ", max(verdictColumn-cellWidth(word), 0)) + " "
	text := p.safe(r.Text) + p.safe(r.Highlight)
	lines := hang(lead, text, w)
	if focused && !r.Fix.IsZero() {
		indent := strings.Repeat(" ", 2+verdictColumn+1)
		lines = append(lines, hang(indent+"fix: ", p.safe(r.Fix), w)...)
	}
	return lines
}

func (p *prerun) preflightLines(w, rows int) []string {
	t := p.theme
	s := &p.pf
	head := []string{t.Bold.Render(preflightTitle) + " " + t.Glyphs.Dot + " read-only checks " + t.Glyphs.Dot +
		" footprint " + footprintWord(p.facts.Compact), ""}

	// The list: every section and check, scrolled so the focused row and its fix show.
	var list []string
	focusFrom, focusTo := -1, -1
	switch {
	case p.facts.SkipPreflight:
		word := verdictWord(installflow.CheckNotChecked)
		list = hang("  "+t.Warn.Render(word)+" ", preflightSkipped, w)
	default:
		for i, r := range s.rows {
			lines := p.rowLines(i, r, w)
			if i == s.focus {
				focusFrom, focusTo = len(list), len(list)+len(lines)-1
			}
			list = append(list, lines...)
		}
	}

	var tail []string
	switch {
	case p.facts.SkipPreflight:
	case s.running:
		tail = append(tail, "", t.Dim.Render(truncCells(preflightRunning+" ("+plural(s.checkCount(), "check", "checks")+" so far)", w, t.Glyphs.Ellipsis)))
	case s.err != "":
		tail = append(tail, "")
		for _, l := range wrap("Preflight could not finish: "+s.err+". "+preflightBlocked, w) {
			tail = append(tail, t.Fail.Render(l))
		}
	case s.finished:
		msg, st := p.tally()
		tail = append(tail, "")
		for _, l := range wrap(msg, w) {
			tail = append(tail, st.Render(l))
		}
	}
	tail = append(tail, p.refusalLines(w)...)
	tail = append(tail, "")
	if s.confirm {
		tail = append(tail, p.confirmLines(w)...)
	} else {
		acts, enabled := p.actions()
		labels := make([]string, len(acts))
		focus := -1
		for i, a := range acts {
			labels[i] = actionLabel(a, p.facts.Compact)
			if a == s.action {
				focus = i
			}
		}
		tail = append(tail, p.actionBar(labels, enabled, focus, w)...)
	}

	return stack(head, list, tail, rows, focusFrom, focusTo, t.Glyphs.Ellipsis)
}

func (p *prerun) confirmLines(w int) []string {
	t := p.theme
	want := !p.facts.Compact
	words := consequencesStandard
	if want {
		words = consequencesCompact
	}
	// The question comes last, beside the keys that answer it: on a short terminal the
	// explanation gives way before it does.
	lines := wrap(words, w)
	lines = append(lines, wrap(consequencesRecheck, w)...)
	return append(lines, t.Bold.Render("Switch to "+footprintWord(want)+"?"))
}

// scroll shows list in room lines, keeping lines [from, to] (the focused row and its
// fix) in view; with no focus (to < 0) it shows the top. Lines cut off above or below
// are counted, exactly, on marker lines of their own.
//
// 🔴 A MARKER NEVER TAKES THE PLACE OF A ROW THAT IS SHOWN, least of all the focused
// one, whose verdict is the thing on screen. When the room is too small for the focused
// row and a marker each side, the markers become one line ("… 3 above, 14 below"); with
// room for one line only, that line is the focused row's first.
func scroll(list []string, room, from, to int, ellipsis string) []string {
	if room <= 0 {
		return nil
	}
	if len(list) <= room {
		return list
	}
	if to < 0 {
		from, to = 0, 0
	}
	start, end := from, to+1 // the shown rows are list[start:end]
	fits := func(s, e int) bool { return e-s+boolInt(s > 0)+boolInt(e < len(list)) <= room }
	// Grow the window round the focused block, downwards first, while it and its
	// markers fit.
	for {
		if end < len(list) && fits(start, end+1) {
			end++
			continue
		}
		if start > 0 && fits(start-1, end) {
			start--
			continue
		}
		break
	}
	// A focused block too tall for the room loses its last lines, never its first.
	for end-start > 1 && !fits(start, end) {
		end--
	}
	above, below := start, len(list)-end
	rows := list[start:end]
	switch {
	case fits(start, end):
		var out []string
		if above > 0 {
			out = append(out, ellipsis+" "+plural(above, "line", "lines")+" above")
		}
		out = append(out, rows...)
		if below > 0 {
			out = append(out, ellipsis+" "+plural(below, "line", "lines")+" below")
		}
		return out
	case room >= 2:
		// One row and one combined marker.
		return []string{rows[0], ellipsis + " " + plural(above, "line", "lines") + " above, " +
			fmt.Sprintf("%d below", below)}
	}
	return rows[:1]
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
