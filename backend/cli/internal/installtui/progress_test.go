// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// t0 is when every scripted run starts.
var t0 = time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

// levelCaps is a session at one rung of the capability ladder, at one size.
func levelCaps(level terminal.Level, w, h int) terminal.Capabilities {
	c := terminal.Capabilities{Eligible: true, TUI: true, Level: level, Width: w, Height: h,
		SizeKnown: true, Layout: terminal.LayoutFor(w, h), VT: true}
	switch level {
	case terminal.L4:
		c.ColorDepth, c.Unicode = colorprofile.TrueColor, true
	case terminal.L3:
		c.ColorDepth, c.Unicode = colorprofile.ANSI256, true
	case terminal.L2:
		c.ColorDepth = colorprofile.ANSI // and Unicode not verified
	case terminal.L1:
		c.ColorDepth, c.Unicode, c.NoColor = colorprofile.ASCII, true, true
	}
	return c
}

// testHub is a real hub whose log ring the screen reads, on a manual clock.
func testHub(t *testing.T, lines int) *installflow.Hub {
	t.Helper()
	clock := testkit.NewManualClock(t0)
	h, err := installflow.NewHub(installflow.Options{Operation: installflow.OperationInstall,
		RunID: "run-test", Clock: clock, Sink: installflow.SinkFunc(func(installflow.Envelope) {})})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Emit(installflow.Event{Payload: installflow.RunStarted{
		Target: installflow.Target{Mode: "kind-create"}, Manifest: []installflow.ManifestStep{{ID: "a"}}}}); err != nil {
		t.Fatal(err)
	}
	tofu, _ := h.LogWriter(installflow.StepInstallPrerequisites, "tofu", installflow.LogInfo)
	k8s, _ := h.LogWriter(installflow.StepInstallRDBReady, "k8s", installflow.LogWarn)
	for i := range lines {
		clock.Advance(time.Second)
		w := io.Writer(tofu)
		if i%7 == 6 {
			w = k8s
		}
		fmt.Fprintf(w, "line %04d helm_release.cert_manager: creating, a line long enough to be cut at narrow widths\n", i)
	}
	return h
}

// newTestModel is the screen with a fixed clock, UTC log times and no redraw beat.
func newTestModel(caps terminal.Capabilities, logs logSource, now time.Time) model {
	m := newModel(caps, &interrupter{cancel: func() {}, force: func() {}}, func() {}, logs)
	m.now = func() time.Time { return now }
	m.loc, m.every = time.UTC, 0
	return m
}

func env(at time.Time, step installflow.StepID, attempt int, p installflow.Payload) installflow.Envelope {
	return installflow.Envelope{Time: at, StepID: step, Attempt: attempt, Kind: p.Kind(), Payload: p}
}

func total(n int64) *int64 { return &n }

func manifestAll() installflow.RunStarted {
	var ms []installflow.ManifestStep
	for _, id := range installflow.InstallSteps() {
		ms = append(ms, installflow.ManifestStep{ID: id, Optional: id == installflow.StepInstallPrepare})
	}
	return installflow.RunStarted{Target: installflow.Target{Mode: "kind-create",
		Context: installflow.Text("kind-devicechain")}, Manifest: ms}
}

// script builds a run's events, one stage at a time, a second apart.
type script struct {
	at   time.Duration
	envs []installflow.Envelope
}

func (s *script) add(step installflow.StepID, attempt int, p installflow.Payload) {
	s.at += time.Second
	s.envs = append(s.envs, env(at(s.at), step, attempt, p))
}

func (s *script) done(ids ...installflow.StepID) {
	for _, id := range ids {
		s.add(id, 1, installflow.StepStarted{Message: installflow.MessageCode(id)})
		s.add(id, 1, installflow.StepFinished{Outcome: installflow.StepSucceeded})
	}
}

func measuredProgress(metric string, unit installflow.Unit, cur, of int64) installflow.StepProgress {
	return installflow.StepProgress{MetricID: metric, Mode: installflow.ProgressMeasured, Current: cur,
		Total: total(of), Unit: unit, Source: "test"}
}

// The scenarios the goldens draw.
func scenarioApplying() (envs []installflow.Envelope, now time.Time, keys []tea.KeyPressMsg) {
	s := &script{}
	s.envs = append(s.envs, env(t0, "", 0, manifestAll()))
	s.done(installflow.StepInstallInputs, installflow.StepInstallPreflight, installflow.StepInstallCluster,
		installflow.StepInstallClaim, installflow.StepInstallInspect, installflow.StepInstallCredentials)
	s.add(installflow.StepInstallPrepare, 1, installflow.StepStarted{Message: "install.prepare"})
	s.add(installflow.StepInstallPrepare, 1, installflow.StepFinished{Outcome: installflow.StepNotApplicable})
	s.done(installflow.StepInstallWriteIntent)
	s.add(installflow.StepInstallOperator, 1, installflow.StepStarted{Message: "install.operator"})
	s.add(installflow.StepInstallOperator, 1, measuredProgress("kubernetes.deployments", installflow.UnitDeployments, 1, 1))
	s.add(installflow.StepInstallOperator, 1, installflow.StepFinished{Outcome: installflow.StepSucceeded})
	s.add(installflow.StepInstallPrerequisites, 1, installflow.StepStarted{Message: "install.prerequisites"})
	s.at = 6*time.Minute + 39*time.Second
	s.add(installflow.StepInstallPrerequisites, 1, measuredProgress("opentofu.actions", installflow.UnitActions, 41, 63))
	return s.envs, at(6*time.Minute + 42*time.Second), nil
}

func scenarioReadiness() (envs []installflow.Envelope, now time.Time, keys []tea.KeyPressMsg) {
	s := &script{}
	s.envs = append(s.envs, env(t0, "", 0, manifestAll()))
	ids := installflow.InstallSteps()
	s.done(ids[:12]...) // through install.record
	s.at = 7*time.Minute + 49*time.Second
	s.add(installflow.StepInstallRDBReady, 1, installflow.StepStarted{Message: "install.rdb-ready",
		BudgetMS: (15 * time.Minute).Milliseconds()})
	s.at = 11 * time.Minute
	s.add(installflow.StepInstallRDBReady, 1, measuredProgress("cloudnativepg.instances", installflow.UnitReplicas, 2, 3))
	// The operator opens the pane, then pauses it a page up.
	return s.envs, at(11*time.Minute + 2*time.Second), []tea.KeyPressMsg{{Code: 'l', Text: "l"}, {Code: tea.KeyPgUp}}
}

func scenarioQuiet() (envs []installflow.Envelope, now time.Time, keys []tea.KeyPressMsg) {
	s := &script{}
	s.envs = append(s.envs, env(t0, "", 0, manifestAll()))
	ids := installflow.InstallSteps()
	s.done(ids[:9]...) // through install.operator
	s.add(installflow.StepInstallPrerequisites, 1, installflow.StepStarted{Message: "install.prerequisites"})
	s.add(installflow.StepInstallPrerequisites, 1, installflow.StepFailed{Code: "install.prerequisites.cnpg-admission",
		Retry: installflow.RetryTransient, Effect: installflow.EffectMayHaveChanged})
	s.add(installflow.StepInstallPrerequisites, 2, installflow.StepStarted{Message: "install.prerequisites"})
	s.add(installflow.StepInstallPrerequisites, 2, installflow.StepProgress{MetricID: "opentofu.actions",
		Mode: installflow.ProgressIndeterminate, Current: 37, Unit: installflow.UnitActions, Source: "opentofu"})
	return s.envs, at(s.at + 52*time.Second), nil
}

var scenarios = []struct {
	name string
	make func() ([]installflow.Envelope, time.Time, []tea.KeyPressMsg)
}{
	{"applying", scenarioApplying},
	{"readiness-logs-paused", scenarioReadiness},
	{"quiet-retry-indeterminate", scenarioQuiet},
}

var levels = []struct {
	name  string
	level terminal.Level
}{{"L4", terminal.L4}, {"L3", terminal.L3}, {"L2", terminal.L2}, {"L1", terminal.L1}}

var sizes = [][2]int{{120, 40}, {100, 30}, {80, 24}, {60, 20}}

// drive plays events and keys through a real Bubble Tea loop (teatest) and returns the
// final model.
func drive(t *testing.T, m model, w, h int, envs []installflow.Envelope, keys []tea.KeyPressMsg) model {
	t.Helper()
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(w, h))
	for _, e := range envs {
		tm.Send(eventMsg{env: e})
	}
	for _, k := range keys {
		tm.Send(k)
	}
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	return tm.FinalModel(t, teatest.WithFinalTimeout(10*time.Second)).(model)
}

var logLine = regexp.MustCompile(`line [0-9]{4}`)

var sgr = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")

func stripANSI(s string) string { return sgr.ReplaceAllString(s, "") }

// TestProgressGoldens draws every scenario at every capability level and every size,
// as escape-stripped text and as raw ANSI. Update with -update, locally, never in CI.
func TestProgressGoldens(t *testing.T) {
	for _, sc := range scenarios {
		for _, lv := range levels {
			for _, sz := range sizes {
				name := fmt.Sprintf("%s/%s-%dx%d", sc.name, lv.name, sz[0], sz[1])
				t.Run(name, func(t *testing.T) {
					envs, now, keys := sc.make()
					hub := testHub(t, 312)
					m := drive(t, newTestModel(levelCaps(lv.level, sz[0], sz[1]), hub, now), sz[0], sz[1], envs, keys)
					view := m.render()
					checkFits(t, view, sz[0], sz[1])
					text := stripANSI(view)
					if lv.level == terminal.L1 && text != view {
						t.Errorf("%s drew escape sequences", lv.name)
					}
					requireGolden(t, "progress/"+name+".txt", []byte(text+"\n"))
					requireGolden(t, "progress/"+name+".ansi", []byte(quoteLines(view)+"\n"))
				})
			}
		}
	}
}

// checkFits requires the frame to fill the terminal exactly: no line wider than it,
// and exactly its height in lines.
func checkFits(t *testing.T, view string, w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != h {
		t.Errorf("%d lines on a %d-row terminal", len(lines), h)
	}
	for i, l := range lines {
		if lw := width(l); lw > w {
			t.Errorf("line %d is %d cells on a %d-column terminal: %q", i, lw, w, stripANSI(l))
		}
	}
}

// view renders the scenario's screen as text at a level and size.
func viewOf(t *testing.T, level terminal.Level, w, h int, envs []installflow.Envelope, now time.Time, logs logSource) string {
	t.Helper()
	m := newTestModel(levelCaps(level, w, h), logs, now)
	for _, e := range envs {
		next, _ := m.Update(eventMsg{env: e})
		m = next.(model)
	}
	return stripANSI(m.render())
}

// rowOf is the line drawing a display stage.
func rowOf(t *testing.T, view, name string) string {
	t.Helper()
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, name) {
			return l
		}
	}
	t.Fatalf("no row %q in:\n%s", name, view)
	return ""
}

func countCells(s, glyph string) int { return strings.Count(s, glyph) }

// 🔴 MUTANT "fake 100%": a stage's bar is full only once the stage has finished. A
// count at its total is not success: the stage has not returned, and its gates have
// not passed.
func TestARunningBarIsNeverFullBeforeItsStageFinishes(t *testing.T) {
	for _, lv := range levels {
		for _, sz := range sizes {
			t.Run(fmt.Sprintf("%s-%dx%d", lv.name, sz[0], sz[1]), func(t *testing.T) {
				f := newFrame(sz[0], sz[1])
				g := newTheme(levelCaps(lv.level, sz[0], sz[1])).g
				s := &script{}
				s.envs = append(s.envs, env(t0, "", 0, manifestAll()))
				s.done(installflow.InstallSteps()[:9]...)
				s.add(installflow.StepInstallPrerequisites, 1, installflow.StepStarted{Message: "install.prerequisites"})
				s.add(installflow.StepInstallPrerequisites, 1, measuredProgress("opentofu.actions", installflow.UnitActions, 63, 63))
				view := viewOf(t, lv.level, sz[0], sz[1], s.envs, at(s.at), nil)
				row := rowOf(t, view, "Cluster prerequisites")
				if n := countCells(row, g.full); n >= f.barW {
					t.Errorf("a running stage at 63/63 drew %d of %d cells full:\n%s", n, f.barW, row)
				}
				if !strings.Contains(row, "RUN") || !strings.Contains(row, "63/63") {
					t.Errorf("the row should say RUN and 63/63:\n%s", row)
				}
				overallRow := rowOf(t, view, "Overall")
				if !strings.Contains(overallRow, "3/6 stages") {
					t.Errorf("only closed stages count: %s", overallRow)
				}

				s.add(installflow.StepInstallPrerequisites, 1, installflow.StepFinished{Outcome: installflow.StepSucceeded})
				view = viewOf(t, lv.level, sz[0], sz[1], s.envs, at(s.at), nil)
				row = rowOf(t, view, "Cluster prerequisites")
				if n := countCells(row, g.full); n != f.barW || !strings.Contains(row, "DONE") {
					t.Errorf("a finished stage drew %d of %d cells:\n%s", n, f.barW, row)
				}
				if strings.Contains(view, "succeeded") || strings.Contains(view, "Install succeeded") {
					t.Errorf("no verdict before run.summary:\n%s", view)
				}
			})
		}
	}
}

// 🔴 MUTANT "fake 100%", overall: every stage closed is not a finished run. The screen
// says Finalizing until the summary, and only the summary's word is the verdict.
func TestEveryStageClosedIsFinalizingNotSuccess(t *testing.T) {
	s := &script{}
	s.envs = append(s.envs, env(t0, "", 0, manifestAll()))
	s.done(installflow.InstallSteps()[:13]...)
	s.add(installflow.StepInstallSummary, 1, installflow.StepStarted{Message: "install.summary"})
	view := viewOf(t, terminal.L1, 100, 30, s.envs, at(s.at), nil)
	if !strings.Contains(view, "6/6 stages") || !strings.Contains(view, "Finalizing") {
		t.Fatalf("want 6/6 stages and Finalizing:\n%s", view)
	}
	if strings.Contains(view, "succeeded") {
		t.Fatalf("success shown before run.summary:\n%s", view)
	}
}

// 🔴 MUTANT "a value above its total": a snapshot that over-counts is drawn at its
// total, and its bar never runs past its cells.
func TestAValueIsNeverShownAboveItsTotal(t *testing.T) {
	for _, lv := range levels {
		for _, sz := range sizes {
			t.Run(fmt.Sprintf("%s-%dx%d", lv.name, sz[0], sz[1]), func(t *testing.T) {
				f := newFrame(sz[0], sz[1])
				s := &script{}
				s.envs = append(s.envs, env(t0, "", 0, manifestAll()))
				s.done(installflow.InstallSteps()[:12]...)
				s.add(installflow.StepInstallRDBReady, 1, installflow.StepStarted{Message: "install.rdb-ready"})
				s.add(installflow.StepInstallRDBReady, 1, measuredProgress("cloudnativepg.instances", installflow.UnitReplicas, 9, 5))
				view := viewOf(t, lv.level, sz[0], sz[1], s.envs, at(s.at), nil)
				if strings.Contains(view, "9/5") || strings.Contains(view, "9 of 5") {
					t.Fatalf("a value above its total:\n%s", view)
				}
				row := rowOf(t, view, "Relational store ready")
				if !strings.Contains(row, "5/5") {
					t.Errorf("want the value held at its total, 5/5:\n%s", row)
				}
				g := newTheme(levelCaps(lv.level, sz[0], sz[1])).g
				if n := countCells(row, g.full) + countCells(row, g.empty); n != f.barW {
					t.Errorf("the bar is %d cells, want %d:\n%s", n, f.barW, row)
				}
			})
		}
	}
}

// 🔴 MUTANT "an indeterminate bar drawn with a number": with no total there is no
// fraction, no percentage and no count to draw against nothing.
func TestIndeterminateWorkCarriesNoNumber(t *testing.T) {
	envs, now, _ := scenarioQuiet()
	for _, lv := range levels {
		for _, sz := range sizes {
			t.Run(fmt.Sprintf("%s-%dx%d", lv.name, sz[0], sz[1]), func(t *testing.T) {
				view := viewOf(t, lv.level, sz[0], sz[1], envs, now, nil)
				row := rowOf(t, view, "Cluster prerequisites")
				name := strings.Index(row, "Cluster prerequisites") + len("Cluster prerequisites")
				if regexp.MustCompile(`[0-9%]`).MatchString(row[name:]) {
					t.Errorf("an indeterminate bar carries a number:\n%s", row)
				}
				if strings.Contains(view, "37") {
					t.Errorf("the indeterminate count was drawn:\n%s", view)
				}
				if lv.level <= terminal.L2 && !strings.Contains(row, "working") {
					t.Errorf("without motion indeterminate work is the word working:\n%s", row)
				}
				if !strings.Contains(view, "total unknown") {
					t.Errorf("the missing total should be said:\n%s", view)
				}
			})
		}
	}
}

// 🔴 MUTANT "log overflow", the pane: a ring full of a flood still draws in the rows the
// pane was given, every frame the terminal's size.
func TestTheLogPaneIsBoundedByTheScreen(t *testing.T) {
	hub := testHub(t, 5000) // far past the ring's 2,000 lines
	envs, now, _ := scenarioApplying()
	for _, sz := range sizes {
		for _, open := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dx%d-open=%v", sz[0], sz[1], open), func(t *testing.T) {
				m := newTestModel(levelCaps(terminal.L4, sz[0], sz[1]), hub, now)
				m.pane.open = open
				for _, e := range envs {
					next, _ := m.Update(eventMsg{env: e})
					m = next.(model)
				}
				view := m.render()
				checkFits(t, view, sz[0], sz[1])
				if n := len(logLine.FindAllString(stripANSI(view), -1)); open && (n == 0 || n > sz[1]) || !open && n != 0 {
					t.Errorf("open=%v drew %d log lines on a %d-row terminal", open, n, sz[1])
				}
				if f := newFrame(sz[0], sz[1]); f.layout == terminal.SingleColumn && open {
					if n := len(logLine.FindAllString(stripANSI(view), -1)); n > singleColumnLogRows {
						t.Errorf("the single-column pane drew %d lines, want at most %d", n, singleColumnLogRows)
					}
				}
				if !strings.Contains(stripANSI(view), "3000 aged out") && open {
					t.Errorf("the lines that aged out are not counted:\n%s", stripANSI(view))
				}
			})
		}
	}
}

// 🔴 MUTANT "log overflow", the engine: a flood of output never waits for the screen,
// even one that has stopped taking events altogether, and the screen can still read
// the ring while it happens.
func TestALogFloodNeverWaitsForTheScreen(t *testing.T) {
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	hub, err := installflow.NewHub(installflow.Options{Operation: installflow.OperationInstall,
		Sink: installflow.SinkFunc(func(installflow.Envelope) { <-stuck })})
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.Emit(installflow.Event{Payload: installflow.RunStarted{
		Target: installflow.Target{Mode: "kind-create"}, Manifest: []installflow.ManifestStep{{ID: "a"}}}}); err != nil {
		t.Fatal(err)
	}
	lw, _ := hub.LogWriter("", "tool", installflow.LogInfo)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 50000 {
			fmt.Fprintf(lw, "flood %d\n", i)
		}
	}()
	m := newTestModel(levelCaps(terminal.L4, 120, 40), hub, t0)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the writer waited on a screen that takes nothing")
	}
	view := stripANSI(m.render())
	if !strings.Contains(view, "flood 49999") {
		t.Errorf("the pane does not follow the newest line:\n%s", view)
	}
	if hub.Stats().DeliveryFailed {
		// Logs are never control events: a flood must not stall delivery and so
		// cancel the run it is the output of.
		t.Error("a log flood tripped the hub's delivery failure")
	}
	if w := hub.LogWindow(0, 1, true); w.End-w.First > installflow.DefaultLogLines {
		t.Errorf("the ring holds %d lines", w.End-w.First)
	}
}

// PgUp pauses the pane where it is; new lines are counted, not drawn; f follows again.
func TestPauseHoldsThePaneAndFollowResumes(t *testing.T) {
	clock := testkit.NewManualClock(t0)
	hub, err := installflow.NewHub(installflow.Options{Operation: installflow.OperationInstall, Clock: clock,
		Sink: installflow.SinkFunc(func(installflow.Envelope) {})})
	if err != nil {
		t.Fatal(err)
	}
	_ = hub.Emit(installflow.Event{Payload: installflow.RunStarted{
		Target: installflow.Target{Mode: "kind-create"}, Manifest: []installflow.ManifestStep{{ID: "a"}}}})
	lw, _ := hub.LogWriter("", "tool", installflow.LogInfo)
	write := func(from, to int) {
		for i := from; i < to; i++ {
			fmt.Fprintf(lw, "entry-%03d\n", i)
		}
	}
	write(0, 100)
	m := newTestModel(levelCaps(terminal.L1, 120, 40), hub, t0)
	press := func(k tea.KeyPressMsg) {
		next, _ := m.Update(k)
		m = next.(model)
	}
	if v := stripANSI(m.render()); !strings.Contains(v, "entry-099") || !strings.Contains(v, "following") {
		t.Fatalf("following shows the newest line:\n%s", v)
	}
	press(tea.KeyPressMsg{Code: tea.KeyPgUp})
	paused := stripANSI(m.render())
	if strings.Contains(paused, "entry-099") || !strings.Contains(paused, "paused") {
		t.Fatalf("PgUp should page back and pause:\n%s", paused)
	}
	entries := regexp.MustCompile(`entry-\d{3}`)
	held := strings.Join(entries.FindAllString(paused, -1), ",")
	write(100, 150)
	v := stripANSI(m.render())
	if got := strings.Join(entries.FindAllString(v, -1), ","); got != held {
		t.Fatalf("a paused pane must hold its lines: had %s, now %s", held, got)
	}
	if !strings.Contains(v, "77 newer") { // the 27 below the page before, and the 50 new ones
		t.Fatalf("a paused pane counts the lines below it:\n%s", v)
	}
	press(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if v := stripANSI(m.render()); !strings.Contains(v, "entry-149") || !strings.Contains(v, "following") {
		t.Fatalf("f should follow again:\n%s", v)
	}
	press(tea.KeyPressMsg{Code: tea.KeyPgUp})
	press(tea.KeyPressMsg{Code: tea.KeyEnd})
	if v := stripANSI(m.render()); !strings.Contains(v, "entry-149") {
		t.Fatalf("End should follow again:\n%s", v)
	}
	press(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if v := stripANSI(m.render()); strings.Contains(v, "entry-") || !strings.Contains(v, "collapsed") {
		t.Fatalf("l should collapse the pane:\n%s", v)
	}
}

// A retry is a new attempt: attempt 1's count is never shown against attempt 2.
func TestARetryStartsItsBarAgain(t *testing.T) {
	s := &script{}
	s.envs = append(s.envs, env(t0, "", 0, manifestAll()))
	s.done(installflow.InstallSteps()[:9]...)
	s.add(installflow.StepInstallPrerequisites, 1, installflow.StepStarted{Message: "install.prerequisites"})
	s.add(installflow.StepInstallPrerequisites, 1, measuredProgress("opentofu.actions", installflow.UnitActions, 40, 63))
	s.add(installflow.StepInstallPrerequisites, 1, installflow.StepFailed{Code: "install.prerequisites.cnpg-admission",
		Retry: installflow.RetryTransient, Effect: installflow.EffectMayHaveChanged})
	s.add(installflow.StepInstallPrerequisites, 2, installflow.StepStarted{Message: "install.prerequisites"})
	// A late snapshot of attempt 1 must not land in attempt 2.
	s.add(installflow.StepInstallPrerequisites, 1, measuredProgress("opentofu.actions", installflow.UnitActions, 41, 63))
	view := viewOf(t, terminal.L1, 100, 30, s.envs, at(s.at), nil)
	if strings.Contains(view, "/63") || strings.Contains(view, "of 63") {
		t.Fatalf("attempt 1's count reached attempt 2:\n%s", view)
	}
	if row := rowOf(t, view, "Cluster prerequisites"); !strings.Contains(row, "RUN") {
		t.Fatalf("attempt 2 is running:\n%s", row)
	}
	if !strings.Contains(view, "attempt 2") {
		t.Fatalf("the attempt is shown:\n%s", view)
	}
}

// A stage group none of whose stages applies is hidden and not counted.
func TestAGroupThatDoesNotApplyIsNotCounted(t *testing.T) {
	s := &script{}
	s.envs = append(s.envs, env(t0, "", 0, manifestAll()))
	s.done(installflow.InstallSteps()[:12]...)
	s.add(installflow.StepInstallRDBReady, 1, installflow.StepStarted{Message: "install.rdb-ready"})
	s.add(installflow.StepInstallRDBReady, 1, installflow.StepFinished{Outcome: installflow.StepNotApplicable})
	view := viewOf(t, terminal.L1, 100, 30, s.envs, at(s.at), nil)
	if strings.Contains(view, "Relational store ready") || !strings.Contains(view, "5/5 stages") {
		t.Fatalf("a not-applicable group is hidden and uncounted:\n%s", view)
	}
}

// The budget and the wait are shown for a stage with a limit, and a long silence says
// the work is still running, never that it hung.
func TestBudgetsAndSilence(t *testing.T) {
	envs, now, _ := scenarioReadiness()
	view := viewOf(t, terminal.L1, 120, 40, envs, now, nil)
	for _, want := range []string{"limit 15m", "waited 3m12s", "2 of 3 replicas", "Remaining: unknown", "Elapsed 11:02"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q:\n%s", want, view)
		}
	}
	quiet, qnow, _ := scenarioQuiet()
	view = viewOf(t, terminal.L1, 120, 40, quiet, qnow, nil)
	if !strings.Contains(view, "work is still running") || strings.Contains(strings.ToLower(view), "hung") {
		t.Errorf("a silence of 52s:\n%s", view)
	}
}

// Every state is a word, so the screen means the same with no colour at all.
func TestEveryStateIsAWord(t *testing.T) {
	s := &script{}
	s.envs = append(s.envs, env(t0, "", 0, manifestAll()))
	s.done(installflow.InstallSteps()[:3]...)
	s.add(installflow.StepInstallClaim, 1, installflow.StepStarted{Message: "install.claim"})
	view := viewOf(t, terminal.L1, 100, 30, s.envs, at(s.at), nil)
	for name, word := range map[string]string{"Resolve target / cluster": "DONE",
		"Identity, refusals and claim": "RUN", "Cluster prerequisites": "WAIT"} {
		if row := rowOf(t, view, name); !strings.HasPrefix(strings.TrimLeft(row, "| │"), word) {
			t.Errorf("%s: want %s:\n%s", name, word, row)
		}
	}
	s.add(installflow.StepInstallClaim, 1, installflow.StepFailed{Code: "install.claim.held",
		Retry: installflow.RetryPermanent, Effect: installflow.EffectNoneObserved})
	view = viewOf(t, terminal.L1, 100, 30, s.envs, at(s.at), nil)
	if row := rowOf(t, view, "Identity, refusals and claim"); !strings.Contains(row, "FAIL") {
		t.Errorf("a failed stage says FAIL:\n%s", row)
	}
}
