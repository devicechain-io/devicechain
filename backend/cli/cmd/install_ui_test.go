// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installplain"
	"github.com/devicechain-io/dcctl/internal/installtui"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// installUIPairs are characterised runs repeated with a --ui flag. The child's streams are
// pipes, as they are under CI, a script or a redirect, so every one of them must take the
// plain path: its stdout, stderr, exit status and the options handed to the engine must
// be BYTE-IDENTICAL to the same run without the flag. Only the recorded argv differs.
var installUIPairs = []struct {
	base string
	ui   []string
}{
	{base: "kind-prompt-piped-y", ui: []string{"--ui=tui"}},
	{base: "kind-prompt-piped-n", ui: []string{"--ui", "tui", "--no-splash"}},
	{base: "kind-prompt-no-stdin", ui: []string{"--ui=auto"}},
	{base: "kind-prompt-assume-yes", ui: []string{"--ui=tui"}},
	{base: "dry-run", ui: []string{"--ui=tui"}},
	{base: "engine-error", ui: []string{"--ui=tui"}},
	{base: "preflight-fail-undersized-docker", ui: []string{"--ui=tui"}},
	{base: "plan-dev", ui: []string{"--ui=auto"}},
	{base: "plan-compact-ha", ui: []string{"--ui=plain"}},
}

// installUIScenarios derives one scenario per pair from its base.
func installUIScenarios() []installScenario {
	var out []installScenario
	for _, p := range installUIPairs {
		for _, sc := range installScenarios {
			if sc.name == p.base {
				sc.name = "ui-" + p.base
				sc.args = append(append([]string(nil), sc.args...), p.ui...)
				out = append(out, sc)
			}
		}
	}
	return out
}

func TestInstallUIOffATerminalIsByteIdentical(t *testing.T) {
	derived := installUIScenarios()
	if len(derived) != len(installUIPairs) {
		t.Fatalf("%d of %d --ui pairs found their base scenario", len(derived), len(installUIPairs))
	}
	for i, sc := range derived {
		pair := installUIPairs[i]
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			var base installScenario
			for _, b := range installScenarios {
				if b.name == pair.base {
					base = b
				}
			}
			got, want := runInstallChild(t, sc), runInstallChild(t, base)
			if !bytes.Equal(dropArgsLine(t, got), dropArgsLine(t, want)) {
				t.Fatalf("with %v the run is not byte-identical to %s:\n--- with --ui ---\n%s\n--- without ---\n%s",
					pair.ui, pair.base, got, want)
			}
			if bytes.Contains(got, []byte("\x1b[?")) {
				t.Fatalf("a run off a terminal wrote a terminal mode sequence:\n%q", got)
			}
		})
	}
}

// fakeTUI stands in for the terminal UI, recording what it was built with.
type fakeTUI struct {
	installPresenter
	caps terminal.Capabilities
}

// TestInstallPresenterForWiresTheSelection checks the caller, not just the table: the
// flags the command parsed reach the selection, and an eligible terminal gets the UI.
func TestInstallPresenterForWiresTheSelection(t *testing.T) {
	saveUI, saveSplash, saveDev, saveProbe, saveNew := installUI, installNoSplash, installDev, probeInstallTerminal, newInstallTUI
	t.Cleanup(func() {
		installUI, installNoSplash, installDev, probeInstallTerminal, newInstallTUI = saveUI, saveSplash, saveDev, saveProbe, saveNew
	})
	probeInstallTerminal = eligibleTerminal
	newInstallTUI = func(c terminal.Capabilities, _ *installtui.Wizard) installPresenter { return fakeTUI{caps: c} }

	for _, c := range []struct {
		ui                         string
		noSplash, dev, yes, dryRun bool
		tui, progressOnly, splash  bool
	}{
		{ui: "plain"},
		{ui: "tui", tui: true, splash: true},
		{ui: "tui", noSplash: true, tui: true},
		{ui: "tui", yes: true, tui: true, progressOnly: true},
		{ui: "tui", dev: true, yes: true, tui: true, progressOnly: true},
		{ui: "tui", dryRun: true},
		{ui: "auto", tui: true, splash: true},
		{ui: "auto", dev: true, yes: true},
	} {
		installUI, installNoSplash, installDev = c.ui, c.noSplash, c.dev
		p, err := installPresenterFor(bootstrap.InstallOptions{Options: bootstrap.Options{AssumeYes: c.yes, DryRun: c.dryRun}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		f, isTUI := p.(fakeTUI)
		if isTUI != c.tui || f.caps.ProgressOnly != c.progressOnly || (f.caps.Splash != terminal.NoArt) != c.splash {
			t.Errorf("%+v: got %T %+v", c, p, f.caps)
		}
		if !c.tui {
			if _, plain := p.(installplain.Presenter); !plain {
				t.Errorf("%+v: got %T, want the plain presenter", c, p)
			}
		}
	}
	installUI = "fancy"
	if _, err := installPresenterFor(bootstrap.InstallOptions{}, nil); err == nil {
		t.Fatal("an unknown --ui reached a presenter")
	}
}

// The terminal UI the command builds owns the process's streams while its screen is up.
func TestTheCommandsTerminalUIOwnsTheProcessStreams(t *testing.T) {
	wiz := &installtui.Wizard{}
	p, ok := newInstallTUI(terminal.Capabilities{TUI: true}, wiz).(installtui.Presenter)
	if !ok {
		t.Fatalf("newInstallTUI built %T", p)
	}
	if !p.OwnProcessStreams || p.Stdin != os.Stdin || p.Stdout != os.Stdout || p.Stderr != os.Stderr || p.Wizard != wiz {
		t.Fatalf("the command's terminal UI: %+v", p)
	}
}

func dropArgsLine(t *testing.T, b []byte) []byte {
	t.Helper()
	first, rest, ok := bytes.Cut(b, []byte("\n"))
	if !ok || !strings.HasPrefix(string(first), "args: ") {
		t.Fatalf("no args line in %q", b)
	}
	return rest
}

func TestParseInstallUI(t *testing.T) {
	for _, v := range []string{"plain", "tui", "auto"} {
		if ui, err := parseInstallUI(v); err != nil || string(ui) != v {
			t.Errorf("parseInstallUI(%q) = %q, %v", v, ui, err)
		}
	}
	for _, v := range []string{"", "TUI", "fancy", "plain "} {
		if _, err := parseInstallUI(v); err == nil {
			t.Errorf("parseInstallUI(%q) accepted", v)
		}
	}
}

// eligibleTerminal is an interactive truecolor terminal at 120x40.
func eligibleTerminal() terminal.Inputs {
	return terminal.Inputs{
		InputTTY: true, OutputTTY: true, ErrorTTY: true, GOOS: "linux",
		Env:   map[string]string{"TERM": "xterm-256color", "LANG": "en_US.UTF-8"},
		Width: 120, Height: 40, SizeKnown: true, Profile: colorprofile.TrueColor,
	}
}

func TestSelectInstallPresenter(t *testing.T) {
	changed := func(mut func(*terminal.Inputs)) func() terminal.Inputs {
		return func() terminal.Inputs { in := eligibleTerminal(); mut(&in); return in }
	}
	cases := []struct {
		name         string
		req          installUIRequest
		probe        func() terminal.Inputs
		tui          bool
		progressOnly bool
		splash       bool
	}{
		{name: "plain on a terminal", req: installUIRequest{UI: terminal.Plain}, probe: eligibleTerminal},
		{name: "tui on a terminal", req: installUIRequest{UI: terminal.TUI}, probe: eligibleTerminal, tui: true, splash: true},
		{name: "tui --no-splash", req: installUIRequest{UI: terminal.TUI, NoSplash: true}, probe: eligibleTerminal, tui: true},
		{name: "auto on a terminal", req: installUIRequest{UI: terminal.Auto}, probe: eligibleTerminal, tui: true, splash: true},
		{name: "tui --yes is progress-only", req: installUIRequest{UI: terminal.TUI, Yes: true}, probe: eligibleTerminal, tui: true, progressOnly: true},
		{name: "tui --dev is progress-only", req: installUIRequest{UI: terminal.TUI, Dev: true, Yes: true}, probe: eligibleTerminal, tui: true, progressOnly: true},
		{name: "auto --yes stays plain", req: installUIRequest{UI: terminal.Auto, Yes: true}, probe: eligibleTerminal},
		{name: "auto --dev stays plain", req: installUIRequest{UI: terminal.Auto, Dev: true}, probe: eligibleTerminal},
		{name: "tui --dry-run", req: installUIRequest{UI: terminal.TUI, DryRun: true}, probe: eligibleTerminal},
		{name: "tui stdin redirected", req: installUIRequest{UI: terminal.TUI}, probe: changed(func(in *terminal.Inputs) { in.InputTTY = false })},
		{name: "tui stdout redirected", req: installUIRequest{UI: terminal.TUI}, probe: changed(func(in *terminal.Inputs) { in.OutputTTY = false })},
		{name: "tui stderr redirected", req: installUIRequest{UI: terminal.TUI}, probe: changed(func(in *terminal.Inputs) { in.ErrorTTY = false })},
		{name: "tui in CI", req: installUIRequest{UI: terminal.TUI}, probe: changed(func(in *terminal.Inputs) { in.Env["CI"] = "true" })},
		{name: "tui TERM=dumb", req: installUIRequest{UI: terminal.TUI}, probe: changed(func(in *terminal.Inputs) { in.Env["TERM"] = "dumb" })},
		{name: "tui legacy Windows console", req: installUIRequest{UI: terminal.TUI}, probe: changed(func(in *terminal.Inputs) { in.GOOS = "windows" })},
		{name: "tui modern Windows console", req: installUIRequest{UI: terminal.TUI}, probe: changed(func(in *terminal.Inputs) {
			in.GOOS, in.WindowsVT, in.WindowsUnicode = "windows", true, true
		}), tui: true, splash: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			probed := false
			caps, tui := selectInstallPresenter(c.req, func() terminal.Inputs { probed = true; return c.probe() })
			if tui != c.tui || caps.ProgressOnly != c.progressOnly || (caps.Splash != terminal.NoArt) != c.splash {
				t.Fatalf("tui=%v progressOnly=%v splash=%v, want %v %v %v",
					tui, caps.ProgressOnly, caps.Splash, c.tui, c.progressOnly, c.splash)
			}
			if c.req.UI == terminal.Plain && probed {
				t.Fatal("--ui=plain probed the terminal")
			}
		})
	}
}
