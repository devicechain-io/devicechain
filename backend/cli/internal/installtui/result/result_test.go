// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package result

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

func levelCaps(level terminal.Level, w, h int) terminal.Capabilities {
	c := terminal.Capabilities{Eligible: true, TUI: true, Level: level, Width: w, Height: h,
		SizeKnown: true, InputTTY: true, OutputTTY: true, ErrorTTY: true, VT: true}
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

var levels = []struct {
	name  string
	level terminal.Level
}{{"L4", terminal.L4}, {"L3", terminal.L3}, {"L2", terminal.L2}, {"L1", terminal.L1}}

var sizes = [][2]int{{120, 40}, {100, 30}, {80, 24}, {60, 20}}

var sgr = regexp.MustCompile("\x1b\\[[0-9;:]*m")

func stripANSI(s string) string { return sgr.ReplaceAllString(s, "") }

func closed() <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

func managedTarget() installflow.Target {
	return installflow.Target{Mode: "managed-kind", Context: installflow.Text("kind-devicechain")}
}

func templateCommand() Command {
	return Command{Line: "dcctl install local --alert-webhook='<redacted: supply privately>' " +
		"--compact=true --yes=true", Template: true}
}

// errApply stands for an OpenTofu apply's error, with a tool diagnostic that
// quotes a credential appended to it, as a real one can.
var errApply = errors.New("applying the cluster prerequisites: exit status 1: Error: creating the " +
	"alert route: POST https://hooks.example.invalid/services/dcCanaryHOOK/T9xK2mP7vQ4 refused")

// scenarios are the result screens the goldens draw.
func scenarioFailed(redact func(string) string) Input {
	return Input{
		Summary: installflow.Summary{Outcome: installflow.RunFailed, InstallRecord: installflow.RecordApplying,
			Readiness: installflow.ReadinessNotChecked, Claim: installflow.ClaimReleased,
			FailedStep: installflow.StepInstallPrerequisites, PartialState: installflow.EffectMayHaveChanged},
		Failure: &installflow.StepFailed{Code: "install.prerequisites.failed", Retry: installflow.RetryUnknown,
			Effect: installflow.EffectMayHaveChanged},
		Target: managedTarget(), Version: "v1.11.0", Equivalent: templateCommand(),
		Err: errApply, Redact: redact, Settled: closed(),
	}
}

func scenarioStopped() Input {
	return Input{
		Summary: installflow.Summary{Outcome: installflow.RunCancelled, InstallRecord: installflow.RecordApplying,
			Readiness: installflow.ReadinessNotChecked, Claim: installflow.ClaimHeld,
			FailedStep: installflow.StepInstallPrerequisites, PartialState: installflow.EffectMayHaveChanged},
		Failure: &installflow.StepFailed{Code: installflow.ErrorToolAbandoned, Retry: installflow.RetryUnknown,
			Remedy: installflow.RemedyRerunSameCommand, Cancelled: true, Effect: installflow.EffectMayHaveChanged},
		Target: managedTarget(), Version: "v1.11.0",
		Equivalent: Command{Line: "dcctl install local --yes=true"},
		Err:        errors.New("tofu apply abandoned"), Redact: func(s string) string { return s },
		Settled: make(chan struct{}), // the abandoned apply is still running
	}
}

func scenarioReadiness() Input {
	return Input{
		Summary: installflow.Summary{Outcome: installflow.RunFailed, InstallRecord: installflow.RecordInstalled,
			Readiness: installflow.ReadinessNotReady, Claim: installflow.ClaimReleased,
			FailedStep: installflow.StepInstallRDBReady, PartialState: installflow.EffectApplied},
		Failure: &installflow.StepFailed{Code: "install.rdb-ready.failed", Retry: installflow.RetryUnknown,
			Effect: installflow.EffectNoneObserved},
		Target:  installflow.Target{Mode: "existing-context", Context: installflow.Text("prod-east")},
		Facts:   Facts{Provider: "local", KubeContext: "prod-east"},
		Version: "v1.11.0", Shell: POSIX,
		Equivalent: Command{Line: "dcctl install local --kube-context=prod-east --ha=true --yes=true"},
		Err:        errors.New("the relational store did not become ready within 15m0s: 2/3 instances ready"),
		Redact:     func(s string) string { return s }, Settled: closed(),
	}
}

func scenarioSucceeded() Input {
	return Input{
		Summary: installflow.Summary{Outcome: installflow.RunSucceeded, InstallRecord: installflow.RecordInstalled,
			Readiness: installflow.ReadinessReady, Claim: installflow.ClaimReleased,
			PartialState: installflow.EffectApplied},
		Target: installflow.Target{Mode: "managed-kind", Context: installflow.Text("kind-lab")},
		Facts: Facts{Provider: "local", Managed: true, Cluster: "lab", Compact: true,
			TLS: true, Monitoring: false, Backups: true, MaxConnections: 300},
		Version:    "v1.11.0",
		Equivalent: Command{Line: "dcctl install local --cluster=lab --compact=true --no-tls=false --yes=true"},
		Settled:    closed(),
	}
}

type scenario struct {
	name string
	in   func() Input
	keys []tea.KeyPressMsg
}

func scenarios(redact func(string) string) []scenario {
	return []scenario{
		{"failed", func() Input { return scenarioFailed(redact) }, nil},
		{"stopped", scenarioStopped, nil},
		{"readiness-timeout", scenarioReadiness, nil},
		{"succeeded", scenarioSucceeded, nil},
		{"help", func() Input { return scenarioFailed(redact) }, []tea.KeyPressMsg{{Code: '?', Text: "?"}}},
	}
}

// drive builds the screen, delivers the settle notice if the run has settled,
// and presses keys.
func drive(in Input, caps terminal.Capabilities, keys ...tea.KeyPressMsg) *Model {
	m := New(in, caps)
	if cmd := m.Init(); cmd != nil && m.settledNow() {
		m.Update(cmd())
	}
	for _, k := range keys {
		m.Update(k)
	}
	return m
}

// canaryHub is a hub whose redaction knows every canary, as the install's does
// once registerSecrets has run.
func canaryHub(t *testing.T) *installflow.Hub {
	t.Helper()
	hub, err := installflow.NewHub(installflow.Options{RunID: "r", Sink: installflow.SinkFunc(func(installflow.Envelope) {})})
	if err != nil {
		t.Fatal(err)
	}
	if err := testkit.RegisterCanaries(hub); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hub.Finish(installflow.Summary{Outcome: installflow.RunFailed}) })
	return hub
}

func TestResultGoldens(t *testing.T) {
	hub := canaryHub(t)
	for _, sc := range scenarios(hub.Redact) {
		for _, lv := range levels {
			for _, sz := range sizes {
				name := fmt.Sprintf("%s/%s-%dx%d", sc.name, lv.name, sz[0], sz[1])
				t.Run(name, func(t *testing.T) {
					view := drive(sc.in(), levelCaps(lv.level, sz[0], sz[1]), sc.keys...).render()
					text := stripANSI(view)
					if lv.level == terminal.L1 && text != view {
						t.Error("NO_COLOR drew an escape sequence")
					}
					if strings.Contains(text, "\x1b") {
						t.Error("an escape sequence other than SGR was drawn")
					}
					checkFits(t, text, sz[0], sz[1])
					testkit.AssertNoCanary(t, "the screen", []byte(view))
					requireGolden(t, name+".txt", []byte(text+"\n"))
					requireGolden(t, name+".ansi", []byte(quoteLines(view)+"\n"))
				})
			}
		}
	}
}

func checkFits(t *testing.T, view string, w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > h {
		t.Errorf("%d lines on a %d-row terminal", len(lines), h)
	}
	for i, l := range lines {
		if n := len([]rune(l)); n > w {
			t.Errorf("line %d is %d cells on a %d-column terminal: %q", i, n, w, l)
		}
	}
}

// 🔴 No screen says success before a successful verdict: every other outcome, and
// a successful one whose delivery was incomplete, is drawn as a failure.
func TestOnlyASuccessfulVerdictIsToldAsSuccess(t *testing.T) {
	for _, o := range []installflow.RunOutcome{installflow.RunSucceeded, installflow.RunFailed,
		installflow.RunCancelled, installflow.RunOutcome("bogus")} {
		for _, incomplete := range []bool{false, true} {
			in := scenarioSucceeded()
			in.Summary.Outcome, in.Summary.DeliveryIncomplete = o, incomplete
			text := stripANSI(drive(in, levelCaps(terminal.L1, 100, 30)).render())
			want := o == installflow.RunSucceeded && !incomplete
			if got := strings.Contains(text, "DONE") || strings.Contains(text, "Shared prerequisites installed"); got != want {
				t.Errorf("outcome %q incomplete=%v: success shown %v\n%s", o, incomplete, got, text)
			}
			if !want && !strings.Contains(text, "FAILED") && !strings.Contains(text, "STOPPED") {
				t.Errorf("outcome %q incomplete=%v carries no failure word\n%s", o, incomplete, text)
			}
		}
	}
}

// 🔴 THE ENGINE'S ERROR REACHES THE COMMAND UNCHANGED, whatever the screen did. A
// failure screen closed with Exit is still the same error, and still exit 1.
func TestVerdictKeepsTheEngineError(t *testing.T) {
	in := scenarioFailed(nil)
	m := drive(in, levelCaps(terminal.L4, 100, 30))
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || cmd() != (Done{Action: ActionExit}) {
		t.Fatal("Enter on the focused Exit did not leave the screen")
	}
	got := Verdict(in.Summary, true, in.Err)
	if got != in.Err {
		t.Fatalf("Verdict returned %v, want the engine's own error", got)
	}
	if in.Summary.ExitCode() != 1 {
		t.Fatal("a failure exits 0")
	}
	for _, c := range []struct {
		name     string
		sum      installflow.Summary
		finished bool
		want     error
	}{
		{"succeeded", installflow.Summary{Outcome: installflow.RunSucceeded}, true, nil},
		{"failed, no error", installflow.Summary{Outcome: installflow.RunFailed}, true, ErrIncomplete},
		{"no verdict", installflow.Summary{Outcome: installflow.RunSucceeded}, false, ErrIncomplete},
		{"delivery incomplete", installflow.Summary{Outcome: installflow.RunSucceeded, DeliveryIncomplete: true}, true, ErrIncomplete},
	} {
		if got := Verdict(c.sum, c.finished, nil); got != c.want {
			t.Errorf("%s: Verdict = %v, want %v", c.name, got, c.want)
		}
	}
	wrapped := fmt.Errorf("wrapped: %w", errApply)
	if got := Verdict(installflow.Summary{Outcome: installflow.RunSucceeded}, true, wrapped); got != wrapped {
		t.Errorf("an error with a successful-looking verdict was replaced: %v", got)
	}
}

// The cause is drawn only after the hub's redaction; with no redactor, it is not
// drawn at all.
func TestTheCauseIsRedactedOrNotShown(t *testing.T) {
	hub := canaryHub(t)
	text := stripANSI(drive(scenarioFailed(hub.Redact), levelCaps(terminal.L1, 120, 40)).render())
	if !strings.Contains(text, "alert route") || !strings.Contains(text, "[redacted]") {
		t.Errorf("the redacted cause is not shown:\n%s", text)
	}
	text = stripANSI(drive(scenarioFailed(nil), levelCaps(terminal.L1, 120, 40)).render())
	if strings.Contains(text, "alert route") || !strings.Contains(text, "prints the install's error") {
		t.Errorf("an unredacted error was drawn:\n%s", text)
	}
}

// 🔴 A canary in the engine's error, the target, the commands and the summary's
// next steps never reaches the exit summary.
func TestTheExitSummaryCarriesNoSecret(t *testing.T) {
	hub := canaryHub(t)
	for _, sc := range scenarios(hub.Redact) {
		in := sc.in()
		var all []string
		for _, c := range testkit.Canaries() {
			all = append(all, c.Value)
		}
		in.Err = fmt.Errorf("%w: %s", errApply, strings.Join(all, " "))
		in.Redact = func(s string) string { return s } // even a redactor that missed them
		var b bytes.Buffer
		if err := WriteExitSummary(&b, in); err != nil {
			t.Fatal(err)
		}
		testkit.AssertNoCanary(t, sc.name+" exit summary", b.Bytes())
		if strings.Contains(b.String(), "\x1b") {
			t.Errorf("%s: the exit summary carries an escape sequence", sc.name)
		}
	}
}

func TestExitSummaries(t *testing.T) {
	for _, sc := range scenarios(nil) {
		if sc.keys != nil {
			continue
		}
		for _, shell := range []struct {
			name  string
			shell Shell
		}{{"posix", POSIX}, {"powershell", PowerShell}} {
			in := sc.in()
			in.Shell = shell.shell
			var b bytes.Buffer
			if err := WriteExitSummary(&b, in); err != nil {
				t.Fatal(err)
			}
			requireGolden(t, "exit-summary/"+sc.name+"-"+shell.name+".txt", b.Bytes())
		}
	}
}

// Only none_observed may read as "nothing changed", on screen and in the summary.
func TestNothingChangedOnlyWhenNoneObserved(t *testing.T) {
	for _, e := range []installflow.EffectCertainty{installflow.EffectNoneObserved,
		installflow.EffectMayHaveChanged, installflow.EffectApplied, "bogus"} {
		in := scenarioFailed(nil)
		in.Summary.PartialState = e
		var b bytes.Buffer
		_ = WriteExitSummary(&b, in)
		for where, text := range map[string]string{
			"screen":  stripANSI(drive(in, levelCaps(terminal.L1, 120, 40)).render()),
			"summary": b.String(),
		} {
			if got := strings.Contains(text, "Nothing was changed"); got != (e == installflow.EffectNoneObserved) {
				t.Errorf("%s, effect %q: says nothing changed = %v", where, e, got)
			}
		}
	}
}

// An installed record followed by a readiness timeout is a failure that names it.
func TestAReadinessTimeoutAfterTheRecordSaysSo(t *testing.T) {
	in := scenarioReadiness()
	text := stripANSI(drive(in, levelCaps(terminal.L1, 120, 40)).render())
	if !strings.Contains(text, "FAILED") || !strings.Contains(text, "Install recorded; replicas still joining") {
		t.Errorf("the readiness timeout reads:\n%s", text)
	}
	var b bytes.Buffer
	_ = WriteExitSummary(&b, in)
	if !strings.Contains(b.String(), "Install recorded; replicas still joining") {
		t.Errorf("the exit summary reads:\n%s", b.String())
	}
}

// 🔴 A re-run is admitted only once the previous run has settled.
func TestReRunWaitsForTheLastRunToSettle(t *testing.T) {
	settle := make(chan struct{})
	in := scenarioFailed(nil)
	in.Settled = settle
	m := New(in, levelCaps(terminal.L1, 120, 40))
	wait := m.Init()
	if wait == nil {
		t.Fatal("no wait for the settle")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatalf("a re-run was chosen before the last run settled: %#v", cmd())
	}
	if !strings.Contains(stripANSI(m.render()), "waiting for the last run to settle") {
		t.Error("the unsettled re-run does not say why it waits")
	}
	// A settle notice the admission does not back is not enough.
	m.Update(settledMsg{})
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("a re-run was chosen on a stale settle notice")
	}
	close(settle)
	m.Update(wait())
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || cmd() != (Done{Action: ActionRerun}) {
		t.Fatal("the settled re-run was not chosen")
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd != nil {
		t.Error("the screen reported twice")
	}
}

func TestNoReRunForAProgressOnlyRunOrASuccess(t *testing.T) {
	in := scenarioFailed(nil)
	in.Settled = nil
	if strings.Contains(stripANSI(drive(in, levelCaps(terminal.L1, 120, 40)).render()), "Review for re-run") {
		t.Error("a progress-only run offered a re-run")
	}
	if strings.Contains(stripANSI(drive(scenarioSucceeded(), levelCaps(terminal.L1, 120, 40)).render()), "re-run") {
		t.Error("a success offered a re-run")
	}
}

// Help lists every key the screen answers, from the same bindings as the footer,
// and closing it returns to the screen without leaving it.
func TestHelpIsTheActiveKeys(t *testing.T) {
	m := drive(scenarioFailed(nil), levelCaps(terminal.L1, 120, 40))
	footer := stripANSI(m.footer())
	m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	help := stripANSI(m.render())
	for _, want := range []string{"Tab", "Enter", "q", "Esc"} {
		if !strings.Contains(help, want) {
			t.Errorf("help does not list %s:\n%s", want, help)
		}
	}
	if !strings.Contains(footer, "? help") {
		t.Errorf("the footer does not offer help: %q", footer)
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Error("Enter under the help overlay activated an action")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.help || strings.Contains(stripANSI(m.render()), "Help") {
		t.Error("Esc did not close help")
	}
}

// The same screen through a real Bubble Tea loop: q leaves with Exit, once.
func TestTheScreenReportsThroughTheLoop(t *testing.T) {
	r := &router{screen: New(scenarioFailed(nil), levelCaps(terminal.L4, 100, 30))}
	tm := teatest.NewTestModel(t, r, teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("FAILED")) },
		teatest.WithDuration(3*time.Second))
	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(*router)
	if len(final.done) != 1 || final.done[0].Action != ActionExit {
		t.Fatalf("reports %+v", final.done)
	}
	if _, err := io.ReadAll(tm.FinalOutput(t)); err != nil {
		t.Fatal(err)
	}
}

// router stands in for the program: it shows the screen and quits on Done.
type router struct {
	screen *Model
	done   []Done
}

func (r *router) Init() tea.Cmd { return r.screen.Init() }

func (r *router) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if d, ok := msg.(Done); ok {
		r.done = append(r.done, d)
		return r, tea.Quit
	}
	_, cmd := r.screen.Update(msg)
	return r, cmd
}

func (r *router) View() tea.View { return r.screen.View() }

// Under 60x16 the screen is a status block that still names the verdict, where it
// stopped and what to run, and still fits.
func TestTheMinimalBlockFits(t *testing.T) {
	for _, sc := range append(scenarios(nil), spacedScenarios()...) {
		for _, sz := range [][2]int{{50, 12}, {40, 8}, {59, 15}, {8, 8}, {80, 1}, {1, 1}, {1, 80}, {59, 2}, {2, 2}} {
			for _, resized := range []bool{false, true} {
				var m *Model
				if resized {
					// Reached by a resize, not from the start.
					m = drive(sc.in(), levelCaps(terminal.L4, 120, 40), sc.keys...)
					m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
				} else {
					m = drive(sc.in(), levelCaps(terminal.L4, sz[0], sz[1]), sc.keys...)
				}
				text := stripANSI(m.render())
				checkFits(t, text, sz[0], sz[1])
				if sz[0] >= 8 && !strings.Contains(text, "FAILED") && !strings.Contains(text, "STOPPED") &&
					!strings.Contains(text, "DONE") {
					t.Errorf("%s at %v names no verdict:\n%s", sc.name, sz, text)
				}
			}
		}
	}
}

// 🔴 A secret split by an escape sequence or a control byte is still redacted: the
// text is sanitised before it is matched, so removing the splitter cannot rebuild
// the secret on screen.
func TestASecretSplitByControlBytesIsRedacted(t *testing.T) {
	hub := canaryHub(t)
	splitters := []string{"\x1b[31m", "\x1b[0m", "\x1b]0;title\x07", "\x1bP1$r\x1b\\", "\x07", "\x00", "\x7f",
		"\x1b7", "\x1b(B", "\x0e"}
	// A whole C1 CSI sequence, then the invisible format characters: the bidi
	// controls, ZWSP, ZWNJ, ZWJ, word joiner, BOM, soft hyphen, LRM, RLM, Arabic
	// letter mark.
	splitters = append(splitters, string(rune(0x9b))+"31m")
	for _, r := range []rune{0x202a, 0x202e, 0x2066, 0x2069, 0x200b, 0x200c, 0x200d, 0x2060, 0xfeff,
		0x00ad, 0x200e, 0x200f, 0x061c} {
		splitters = append(splitters, string(r))
	}
	for _, c := range testkit.Canaries() {
		for _, core := range c.Cores {
			for _, sp := range splitters {
				// The registered value, split in the middle of a part that cannot
				// occur by accident.
				at := strings.Index(c.Value, core) + len(core)/2
				in := scenarioFailed(hub.Redact)
				in.Err = errors.New("applying: refused " + c.Value[:at] + sp + c.Value[at:] + " again")
				view := drive(in, levelCaps(terminal.L1, 120, 40)).render()
				testkit.AssertNoCanary(t, fmt.Sprintf("%s split by %q, on screen", c.Label, sp), []byte(view))
				testkit.AssertNoCanary(t, fmt.Sprintf("%s split by %q, as the cause", c.Label, sp), []byte(in.Cause()))
				var b bytes.Buffer
				_ = WriteExitSummary(&b, in)
				testkit.AssertNoCanary(t, fmt.Sprintf("%s split by %q, exit summary", c.Label, sp), b.Bytes())
			}
		}
	}
}

// spacedScenarios target a context whose name holds two spaces, for each shell's
// quoting. The quoted argument must be drawn byte for byte: one space fewer is
// another context.
func spacedScenarios() []scenario {
	mk := func(shell Shell, line string) func() Input {
		return func() Input {
			in := scenarioReadiness()
			in.Shell = shell
			in.Target = installflow.Target{Mode: "existing-context", Context: installflow.Text("team  prod")}
			in.Facts.KubeContext = "team  prod"
			in.Equivalent = Command{Line: line}
			return in
		}
	}
	return []scenario{
		{"spaced-posix", mk(POSIX, "dcctl install local --ha=true '--kube-context=team  prod' --yes=true"), nil},
		{"spaced-powershell", mk(PowerShell,
			"& dcctl install local --ha=true '--kube-context=team  prod' --yes=true"), nil},
	}
}

func TestCommandsKeepTheirArgumentsByteForByte(t *testing.T) {
	for _, sc := range spacedScenarios() {
		for _, sz := range [][2]int{{120, 40}, {100, 30}, {80, 24}, {60, 20}} {
			name := fmt.Sprintf("%s/L1-%dx%d", sc.name, sz[0], sz[1])
			text := drive(sc.in(), levelCaps(terminal.L1, sz[0], sz[1])).render()
			if !strings.Contains(text, "'--kube-context=team  prod'") {
				t.Errorf("%s does not draw the re-run's argument intact:\n%s", name, text)
			}
			// A short screen may leave the inspect row out; one it draws is intact.
			if strings.Contains(text, "--context") && !strings.Contains(text, "--context 'team  prod'") {
				t.Errorf("%s does not draw the inspect command's argument intact:\n%s", name, text)
			}
			checkFits(t, text, sz[0], sz[1])
			requireGolden(t, name+".txt", []byte(text+"\n"))
		}
		var b bytes.Buffer
		if err := WriteExitSummary(&b, sc.in()); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), "'--kube-context=team  prod'") {
			t.Errorf("%s exit summary:\n%s", sc.name, b.String())
		}
		requireGolden(t, "exit-summary/"+sc.name+".txt", b.Bytes())
	}
}

// Wrapping breaks only between arguments, and joining the lines back with the
// separator it removed gives the command it was given.
func TestWrapCommandBreaksOnlyBetweenArguments(t *testing.T) {
	for _, c := range []struct {
		shell Shell
		line  string
		args  []string
	}{
		{POSIX, `dcctl install local '--kube-context=a  b' "--x=c   d" 'it'\''s  here' "--y=\"q  r\"  s" --yes=true`,
			[]string{`'--kube-context=a  b'`, `"--x=c   d"`, `'it'\''s  here'`, `"--y=\"q  r\"  s"`}},
		{PowerShell, "& dcctl install local '--kube-context=a  b' 'it''s  here' \"--kube-context=a`\"  b\" \"--z=`\"q  r`\"  s\" --yes=true",
			[]string{`'--kube-context=a  b'`, `'it''s  here'`, "\"--kube-context=a`\"  b\"", "\"--z=`\"q  r`\"  s\""}},
	} {
		words := splitCommand(c.line, c.shell)
		if strings.Join(words, " ") != c.line {
			t.Errorf("shell %d: the words %q do not rebuild the line", c.shell, words)
		}
		for w := 1; w <= len(c.line)+1; w++ {
			lines := wrapCommand(words, w)
			for _, l := range lines {
				if lipgloss.Width(l) > w {
					t.Fatalf("width %d: line %q", w, l)
				}
			}
			for _, arg := range c.args {
				if len(arg) <= w && !strings.Contains(strings.Join(lines, "\n"), arg) {
					t.Errorf("shell %d, width %d broke or rewrote %s: %q", c.shell, w, arg, lines)
				}
			}
		}
	}
}

// escapedQuoteScenarios carry a double-quoted argument with an escaped double
// quote and two spaces in it, for each shell, once with the words the serialiser
// recorded and once without.
func escapedQuoteScenarios() []scenario {
	mk := func(shell Shell, line string, args []string) func() Input {
		return func() Input {
			in := scenarioReadiness()
			in.Shell = shell
			in.Equivalent = Command{Line: line}
			for _, a := range args {
				in.Equivalent.Args = append(in.Equivalent.Args, a)
			}
			return in
		}
	}
	posix := `dcctl install local --ha=true "--kube-context=a \"q\"  b" --yes=true`
	ps := "& dcctl install local --ha=true \"--kube-context=a `\"q`\"  b\" --yes=true"
	return []scenario{
		{"escaped-posix", mk(POSIX, posix, nil), nil},
		{"escaped-powershell", mk(PowerShell, ps, nil), nil},
		{"escaped-posix-args", mk(POSIX, posix, []string{"dcctl", "install", "local", "--ha=true",
			`"--kube-context=a \"q\"  b"`, "--yes=true"}), nil},
		{"escaped-powershell-args", mk(PowerShell, ps, []string{"&", "dcctl", "install", "local", "--ha=true",
			"\"--kube-context=a `\"q`\"  b\"", "--yes=true"}), nil},
	}
}

func TestEscapedQuotesStayInsideTheirArgument(t *testing.T) {
	for _, sc := range escapedQuoteScenarios() {
		want := `"--kube-context=a \"q\"  b"`
		if strings.Contains(sc.name, "powershell") {
			want = "\"--kube-context=a `\"q`\"  b\""
		}
		for _, sz := range [][2]int{{120, 40}, {60, 20}} {
			name := fmt.Sprintf("%s/L1-%dx%d", sc.name, sz[0], sz[1])
			text := drive(sc.in(), levelCaps(terminal.L1, sz[0], sz[1])).render()
			if !strings.Contains(text, want) {
				t.Errorf("%s does not draw %s intact:\n%s", name, want, text)
			}
			checkFits(t, text, sz[0], sz[1])
			requireGolden(t, name+".txt", []byte(text+"\n"))
		}
	}
}

// Recorded words that do not rebuild the line are not trusted: the line is read.
func TestRecordedWordsMustRebuildTheLine(t *testing.T) {
	c := Command{Line: "dcctl install local '--kube-context=a  b'",
		Args: []string{"dcctl", "install local '--kube-context=a", "b'"}}
	got := c.words(POSIX)
	if len(got) != 4 || got[3] != "'--kube-context=a  b'" {
		t.Errorf("words %q", got)
	}
}

// 🔴 The marker that replaces one secret can complete another; redaction runs to
// a fixed point, so no registered value is ever drawn or printed.
func TestRedactionRunsToAFixedPoint(t *testing.T) {
	hub := canaryHub(t)
	for _, s := range []string{"abcdef", "prefix[redacted]", "outer[redacted]suffix"} {
		if err := hub.AddSecret(s); err != nil {
			t.Fatal(err)
		}
	}
	in := scenarioFailed(hub.Redact)
	in.Err = errors.New("refused: outerprefixabcdefsuffix")
	// The same text arriving in a field the hub has already scrubbed once.
	in.Target = installflow.Target{Mode: "existing-context", Context: installflow.Text("outerprefix[redacted]suffix")}
	var b bytes.Buffer
	if err := WriteExitSummary(&b, in); err != nil {
		t.Fatal(err)
	}
	view := stripANSI(drive(in, levelCaps(terminal.L1, 120, 40)).render())
	for where, text := range map[string]string{"cause": in.Cause(), "screen": view, "exit summary": b.String()} {
		for _, s := range []string{"abcdef", "prefix[redacted]", "outer[redacted]suffix"} {
			if strings.Contains(text, s) {
				t.Errorf("the %s holds the registered value %q:\n%s", where, s, text)
			}
		}
	}
	// A redactor that never settles withholds the value rather than drawing it.
	grow := func(s string) string { return s + "x" }
	if got := (Input{Redact: grow}).shown("anything"); got != withheld {
		t.Errorf("an unsettled redaction drew %q", got)
	}
	if got := (Input{Err: errors.New("boom"), Redact: grow}).Cause(); strings.Contains(got, "boom") {
		t.Errorf("an unsettled cause was drawn: %q", got)
	}
}

// A wide character in a one-column frame is left out rather than overflowing it.
func TestWideRunesNeverOverflowANarrowFrame(t *testing.T) {
	in := scenarioSucceeded()
	in.Target = installflow.Target{Mode: "existing-context", Context: installflow.Text("界界")}
	in.Facts = Facts{Provider: "local", KubeContext: "界界"}
	for _, sz := range [][2]int{{1, 80}, {2, 80}, {3, 80}, {1, 1}, {8, 8}} {
		text := stripANSI(drive(in, levelCaps(terminal.L1, sz[0], sz[1])).render())
		for i, l := range strings.Split(text, "\n") {
			if lipgloss.Width(l) > sz[0] {
				t.Errorf("%v: line %d is %d cells: %q", sz, i, lipgloss.Width(l), l)
			}
		}
	}
}

// 🔴 A command the operator copies is built from the raw choice. When the raw
// value holds a character the screen cannot draw (a ZWJ, a control byte), the
// argument is a placeholder naming the problem: never the sanitised value, which
// would select another context or cluster without a word.
func TestCommandsNeverCarryASanitisedTarget(t *testing.T) {
	zwj := string(rune(0x200d))
	for _, c := range []struct {
		name string
		in   func() Input
	}{
		{"existing context, failed", func() Input {
			in := scenarioReadiness()
			in.Target = installflow.Target{Mode: "existing-context", Context: installflow.Text("team" + zwj + "prod")}
			in.Facts.KubeContext = "team" + zwj + "prod"
			in.Equivalent = Command{Line: "dcctl install local --kube-context=team" + zwj + "prod --yes=true"}
			return in
		}},
		{"managed cluster, succeeded", func() Input {
			in := scenarioSucceeded()
			in.Target = installflow.Target{Mode: "managed-kind", Context: installflow.Text("kind-lab" + zwj + "2")}
			in.Facts.Cluster = "lab" + zwj + "2"
			in.Equivalent = Command{Line: "dcctl install local --cluster=lab" + zwj + "2 --yes=true",
				Args: []string{"dcctl", "install", "local", "--cluster=lab" + zwj + "2", "--yes=true"}}
			return in
		}},
		{"existing context, control byte", func() Input {
			in := scenarioReadiness()
			in.Facts.KubeContext = "team\x07prod"
			in.Equivalent = Command{Line: "dcctl install local '--kube-context=team\x07prod' --yes=true"}
			return in
		}},
	} {
		for _, shell := range []Shell{POSIX, PowerShell} {
			in := c.in()
			in.Shell = shell
			var b bytes.Buffer
			if err := WriteExitSummary(&b, in); err != nil {
				t.Fatal(err)
			}
			screen := stripANSI(drive(in, levelCaps(terminal.L1, 120, 40)).render())
			for where, text := range map[string]string{"screen": screen, "exit summary": b.String()} {
				for _, l := range strings.Split(text, "\n") {
					if !strings.Contains(l, "dcctl ") && !strings.Contains(l, "kubectl ") ||
						strings.Contains(l, "dcctl install "+string(rune(0xb7))) {
						continue // not a command line, or the header, which only names the target
					}
					for _, bad := range []string{"teamprod", "lab2", "kind-lab2"} {
						if strings.Contains(l, bad) {
							t.Errorf("%s, %s: a command names the sanitised %q: %q", c.name, where, bad, l)
						}
					}
				}
				if !strings.Contains(text, "-contains-invisible-characters>") {
					t.Errorf("%s, %s: no placeholder names the problem:\n%s", c.name, where, text)
				}
			}
		}
	}
	// A raw value the screen can draw is used as it is.
	if got := drawableArg("team  prod", "kube-context"); got != "team  prod" {
		t.Errorf("a drawable value became %q", got)
	}
}
