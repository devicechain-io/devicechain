// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// bootstrapUIPairs are characterised bootstrap runs repeated with a --ui flag. The child's
// streams are pipes, as under CI, a script or a redirect, so every one must take the plain
// path: stdout, stderr and exit status BYTE-IDENTICAL to the same run without the flag,
// exactly as install's --ui pairs are. Only the recorded argv differs.
var bootstrapUIPairs = []struct {
	base string
	ui   []string
}{
	{base: "refuse-unknown-area", ui: []string{"--ui=tui"}},
	{base: "refuse-instance-name", ui: []string{"--ui", "tui"}},
	{base: "preflight-small-host", ui: []string{"--ui=tui"}},
	{base: "preflight-pass", ui: []string{"--ui=auto"}},
	// The passphrase's sources, in their order, with the terminal UI asked for and no
	// terminal to give it: a piped stdin is still never read for a passphrase.
	{base: "escrow-no-source-no-terminal", ui: []string{"--ui=tui"}},
	{base: "escrow-file", ui: []string{"--ui=tui"}},
	{base: "escrow-file-before-env", ui: []string{"--ui=auto"}},
	{base: "escrow-env-empty", ui: []string{"--ui=tui"}},
	{base: "escrow-env", ui: []string{"--ui=tui"}},
	{base: "escrow-dry-run", ui: []string{"--ui=tui"}},
	{base: "dev", ui: []string{"--ui=tui"}},
	{base: "dev-escrow-kept", ui: []string{"--ui=auto"}},
	{base: "kube-context-not-found", ui: []string{"--ui=plain"}},
	{base: "preflight-pass", ui: []string{"--ui=plain"}},
}

// bootstrapUIScenarios derives one scenario per pair from its base.
func bootstrapUIScenarios() []installScenario {
	var out []installScenario
	for _, p := range bootstrapUIPairs {
		for _, sc := range bootstrapScenarios {
			if sc.name == p.base {
				sc.name = "bootstrap-ui-" + p.base + "-" + strings.TrimPrefix(strings.Join(p.ui, ""), "--ui")
				sc.args = append(append([]string(nil), sc.args...), p.ui...)
				out = append(out, sc)
			}
		}
	}
	return out
}

// 🔴 --ui=plain IS TODAY'S BOOTSTRAP, BYTE FOR BYTE, and --ui=tui or auto off an
// interactive terminal is too: no escape sequence, no new prompt, the same exit status.
func TestBootstrapUIOffATerminalIsByteIdentical(t *testing.T) {
	derived := bootstrapUIScenarios()
	if len(derived) != len(bootstrapUIPairs) {
		t.Fatalf("%d of %d --ui pairs found their base scenario", len(derived), len(bootstrapUIPairs))
	}
	for i, sc := range derived {
		pair := bootstrapUIPairs[i]
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			var base installScenario
			for _, b := range bootstrapScenarios {
				if b.name == pair.base {
					base = b
				}
			}
			got, want := runInstallChild(t, sc), runInstallChild(t, base)
			if !bytes.Equal(dropArgsLine(t, got), dropArgsLine(t, want)) {
				t.Fatalf("with %v the bootstrap is not byte-identical to %s:\n--- with --ui ---\n%s\n--- without ---\n%s",
					pair.ui, pair.base, got, want)
			}
			if bytes.Contains(got, []byte("\x1b[?")) {
				t.Fatalf("a bootstrap off a terminal wrote a terminal mode sequence:\n%q", got)
			}
		})
	}
}

// An unknown --ui is refused before anything is printed or touched, with install's words.
func TestBootstrapRefusesAnUnknownUI(t *testing.T) {
	save := bootstrapUI
	t.Cleanup(func() { bootstrapUI = save })
	bootstrapUI = "fancy"
	err := bootstrapCmd.RunE(bootstrapCmd, []string{"local", "acme"})
	if err == nil || err.Error() != `--ui must be plain, tui or auto (got "fancy")` {
		t.Fatalf("an unknown --ui: %v", err)
	}
}

// fakeBootstrapTUI stands in for the terminal UI, recording what it was built with.
type fakeBootstrapTUI struct {
	caps  terminal.Capabilities
	scr   *installtui.BootstrapScreens
	req   installflow.Request
	ran   installflow.Engine
	calls *int
}

func (f *fakeBootstrapTUI) Run(_ context.Context, e installflow.Engine, req installflow.Request, _ installflow.PreflightRunner) error {
	*f.calls++
	f.ran, f.req = e, req
	return nil
}

// TestRunBootstrapEngineWiresTheSelection checks the caller, not just the table: the
// bootstrap's own --yes, --dev and --dry-run reach install's selection, an eligible
// terminal gets the instance screens (progress-only with --yes or --dev, never a logo),
// and the engine under the terminal UI knows a person is at the terminal to be asked
// for the passphrase.
func TestRunBootstrapEngineWiresTheSelection(t *testing.T) {
	saveProbe, saveNew, saveDev := probeInstallTerminal, newBootstrapTUI, bootstrapDev
	t.Cleanup(func() { probeInstallTerminal, newBootstrapTUI, bootstrapDev = saveProbe, saveNew, saveDev })
	probeInstallTerminal = eligibleTerminal
	var fake *fakeBootstrapTUI
	calls := 0
	newBootstrapTUI = func(c terminal.Capabilities, scr *installtui.BootstrapScreens) installPresenter {
		fake = &fakeBootstrapTUI{caps: c, scr: scr, calls: &calls}
		return fake
	}
	for _, c := range []struct {
		ui                  terminal.UI
		yes, dev, dryRun    bool
		tui, progressOnly   bool
		screensProgressOnly bool
	}{
		{ui: terminal.TUI, tui: true},
		{ui: terminal.Auto, tui: true},
		{ui: terminal.TUI, yes: true, tui: true, progressOnly: true},
		{ui: terminal.TUI, dev: true, yes: true, tui: true, progressOnly: true},
		{ui: terminal.Auto, yes: true},
		{ui: terminal.Auto, dev: true, yes: true},
		{ui: terminal.TUI, dryRun: true},
		{ui: terminal.Plain},
	} {
		fake, calls, bootstrapDev = nil, 0, c.dev
		stop := errors.New("the plain presenter ran the engine")
		e := &bootstrap.BootstrapEngine{Instance: "acme", AssumeYes: c.yes, DryRun: c.dryRun, SkipPreflight: true,
			Escrow: bootstrap.EscrowFlags{NoEscrow: true, DryRun: c.dryRun},
			Target: func(context.Context, bootstrap.Console, bootstrap.EscrowPlan) (*bootstrap.State, error) {
				return nil, stop
			}}
		var askedProgressOnly *bool
		err := runBootstrapEngine(context.Background(), c.ui, e, nil, func(progressOnly bool) *installtui.BootstrapScreens {
			askedProgressOnly = &progressOnly
			return &installtui.BootstrapScreens{}
		})
		if !c.tui {
			if calls != 0 || !errors.Is(err, stop) {
				t.Errorf("%+v: the terminal UI was chosen, or the plain presenter did not run (%v)", c, err)
			}
			continue
		}
		if err != nil || calls != 1 || fake == nil {
			t.Fatalf("%+v: the terminal UI did not run: %v", c, err)
		}
		if fake.caps.ProgressOnly != c.progressOnly || fake.caps.Splash != terminal.NoArt || fake.caps.Animate {
			t.Errorf("%+v: caps %+v", c, fake.caps)
		}
		if askedProgressOnly == nil || *askedProgressOnly != c.progressOnly {
			t.Errorf("%+v: the screens were built for progress-only %v", c, askedProgressOnly)
		}
		if fake.ran != installflow.Engine(e) || fake.req != (installflow.Request{Operation: installflow.OperationBootstrap,
			AssumeYes: c.yes, DryRun: c.dryRun}) {
			t.Errorf("%+v: the presenter was handed %v, %+v", c, fake.ran, fake.req)
		}
		if e.PersonAtTerminal == nil || !e.PersonAtTerminal() {
			t.Errorf("%+v: under the terminal UI the engine does not know a person is at the terminal", c)
		}
	}
}

// The command's terminal UI owns the process's streams and runs the instance screens.
func TestTheBootstrapTerminalUIOwnsTheProcessStreams(t *testing.T) {
	scr := &installtui.BootstrapScreens{}
	p, ok := newBootstrapTUI(terminal.Capabilities{TUI: true}, scr).(installtui.Presenter)
	if !ok {
		t.Fatalf("newBootstrapTUI built %T", p)
	}
	if !p.OwnProcessStreams || p.Stdin != os.Stdin || p.Stdout != os.Stdout || p.Stderr != os.Stderr ||
		p.Bootstrap != scr || p.Continues || p.Wizard != nil {
		t.Fatalf("the command's bootstrap terminal UI: %+v", p)
	}
}

// resetBootstrapFlags puts every bootstrap flag back to its default once a test parsed some.
func resetBootstrapFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if _, _, err := setupBootstrapRun([]string{"dcctl", "bootstrap", "local", "reset"}, nil); err != nil {
			t.Fatal(err)
		}
	})
}

// 🔴 THE INSTANCE SCREENS START FROM THE COMMAND LINE AND DROP NOTHING OF IT. What they
// choose is seeded from the flags and labelled as such; every flag they do not choose is
// carried into the run exactly as given; and the run a set of choices describes is built
// through the bootstrap command's own flags, as that command line would build it.
func TestTheBootstrapScreensAreTheCommandLines(t *testing.T) {
	resetBootstrapFlags(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(bootstrap.EscrowPassphraseEnv, "")
	os.Unsetenv(bootstrap.EscrowPassphraseEnv)
	pass := filepath.Join(t.TempDir(), "pass.txt")
	words := []string{"dcctl", "bootstrap", "local", "acme", "--kube-context=prod", "--host=iot.example.com",
		"--skip-preflight", "--trusted-proxies=10.0.0.0/8,192.168.0.0/16", "--escrow-passphrase-file=" + pass,
		"--ui=tui"}
	if _, _, err := bootstrapRunFromWords(bootstrapCmd, words, nil); err != nil {
		t.Fatal(err)
	}
	rec := &instanceFactsRecorder{}
	scr := bootstrapScreensFor(bootstrapCmd, []string{"local", "acme"}, rec, false)
	in, esc := scr.Setup.Instance, scr.Setup.Escrow
	if in.Name != "acme" || in.Host != "iot.example.com" || !in.FromFlag["host"] || !in.FromFlag["name"] ||
		in.FromFlag["profile"] || in.ValidateName == nil || in.ValidateAreas == nil {
		t.Fatalf("the instance seed: %+v", in)
	}
	if esc.Mode != installtui.EscrowWrite || esc.PathFromFlag || esc.Passphrase != installtui.PassphraseFromFile ||
		esc.ValidatePath == nil || esc.DefaultPath == nil {
		t.Fatalf("the escrow seed: %+v", esc)
	}
	if scr.Setup.Target != "context prod" || scr.Handover != nil || scr.Plan == nil || scr.Facts == nil {
		t.Fatalf("the screens: target %q, hand-over %v", scr.Setup.Target, scr.Handover)
	}

	// The person changes the host; nothing else.
	planned := installtui.BootstrapPlanned{
		Instance: installtui.InstanceChoices{Name: "acme", Profile: "default", Host: "other.example.com", ObjectStore: "auto",
			Given: map[string]bool{"name": true, "host": true}},
		Escrow: installtui.EscrowChoice{Mode: installtui.EscrowWrite, Passphrase: installtui.PassphraseFromFile},
	}
	plan, err := scr.Plan(planned)
	if err != nil {
		t.Fatal(err)
	}
	e := plan.Engine.(*bootstrap.BootstrapEngine)
	if e.Instance != "acme" || e.KubeContext != "prod" || !e.SkipPreflight || e.Escrow.PassphraseFile != pass ||
		e.AssumeYes || e.DryRun {
		t.Fatalf("the run: %+v", e)
	}
	if e.PersonAtTerminal == nil || !e.PersonAtTerminal() {
		t.Fatal("the run does not know a person is at the terminal")
	}
	if bootstrapHost != "other.example.com" || !slices.Equal(bootstrapTrustedProxies, []string{"10.0.0.0/8", "192.168.0.0/16"}) {
		t.Fatalf("the flags the run was built with: host %q, proxies %v", bootstrapHost, bootstrapTrustedProxies)
	}
	for _, want := range []string{"dcctl bootstrap local acme --kube-context=prod", "--host=other.example.com",
		"--skip-preflight=true", "--trusted-proxies=10.0.0.0/8", "--trusted-proxies=192.168.0.0/16",
		"--escrow-passphrase-file=<file holding the escrow passphrase>", "--yes"} {
		if !strings.Contains(plan.Command, want) {
			t.Errorf("the equivalent command has no %q: %s", want, plan.Command)
		}
	}
	if strings.Contains(plan.Command, pass) || strings.Contains(plan.Command, "--ui") {
		t.Errorf("the equivalent command names the passphrase file or a presentation flag: %s", plan.Command)
	}
	var also string
	for _, r := range plan.Review.Rows {
		if r.Label == "Also" {
			also = r.Value
		}
		if r.Label == "Escrow" && !strings.Contains(r.Value, "--escrow-passphrase-file; nothing is asked") {
			t.Errorf("the review's escrow: %q", r.Value)
		}
	}
	if !strings.HasPrefix(also, "--escrow-passphrase-file, --skip-preflight, --trusted-proxies") {
		t.Errorf("the review does not list the carried flags: %q", also)
	}

	// A choice that contradicts a carried flag is refused on the review, before its approval.
	planned.Escrow = installtui.EscrowChoice{Mode: installtui.EscrowNone}
	if _, err := scr.Plan(planned); err == nil || !strings.Contains(err.Error(), "--escrow-passphrase-file") {
		t.Fatalf("no escrow with a passphrase file was planned: %v", err)
	}
}

// --restore-root-key and --no-escrow seed the escrow screen as they were given.
func TestTheEscrowSeedFollowsTheCommandLine(t *testing.T) {
	resetBootstrapFlags(t)
	for _, c := range []struct {
		flag string
		mode installtui.EscrowMode
	}{
		{"--no-escrow", installtui.EscrowNone},
		{"--restore-root-key=old.escrow", installtui.EscrowRestore},
		{"--escrow-file=/keep/acme.escrow", installtui.EscrowWrite},
	} {
		if _, _, err := bootstrapRunFromWords(bootstrapCmd, []string{"dcctl", "bootstrap", "local", "acme", c.flag}, nil); err != nil {
			t.Fatal(err)
		}
		esc := bootstrapScreensFor(bootstrapCmd, []string{"local", "acme"}, &instanceFactsRecorder{}, false).Setup.Escrow
		if esc.Mode != c.mode {
			t.Errorf("%s: mode %q", c.flag, esc.Mode)
		}
		if c.mode == installtui.EscrowRestore && esc.RestoreFrom != "old.escrow" {
			t.Errorf("%s: restores from %q", c.flag, esc.RestoreFrom)
		}
		if c.mode == installtui.EscrowWrite && (!esc.PathFromFlag || esc.Path != "/keep/acme.escrow") {
			t.Errorf("%s: path %q from flag %v", c.flag, esc.Path, esc.PathFromFlag)
		}
	}
}

// A progress-only run has no screens before it: no plan, only the seeds that name it.
func TestAProgressOnlyBootstrapHasNoPlanOfItsOwn(t *testing.T) {
	resetBootstrapFlags(t)
	if _, _, err := bootstrapRunFromWords(bootstrapCmd, []string{"dcctl", "bootstrap", "local", "acme", "--yes", "--no-escrow"}, nil); err != nil {
		t.Fatal(err)
	}
	scr := bootstrapScreensFor(bootstrapCmd, []string{"local", "acme"}, &instanceFactsRecorder{}, true)
	if scr.Plan != nil || scr.Setup.Instance.Name != "acme" || scr.Facts == nil {
		t.Fatalf("a progress-only run's screens: plan %v, name %q", scr.Plan != nil, scr.Setup.Instance.Name)
	}
}

// 🔴 A PATH THE ESCROW SCREEN ACCEPTS IS ONE THE RUN ACCEPTS. Both journeys check it with
// the bootstrap's own rule: inside the instance's state directory (which destroy deletes)
// is refused before the approval, so setup can never exit 4 over a path that changed
// nothing. Setup, which builds a new instance, still refuses an existing file as well.
func TestTheEscrowScreensUseTheEnginesPathRule(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	inside := filepath.Join(home, ".devicechain", "instances", "acme", "acme.escrow")
	existing := filepath.Join(t.TempDir(), "kept.escrow")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	setupRule := setupInstanceScreens(setupTarget{Cluster: "devicechain"}).Setup.Escrow.ValidatePath
	if err := setupRule("acme", inside); err == nil || !strings.Contains(err.Error(), "dcctl destroy") {
		t.Fatalf("setup accepted an escrow path inside the instance's state directory: %v", err)
	}
	if err := setupRule("acme", existing); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("setup accepted an existing escrow file: %v", err)
	}
	if err := setupRule("acme", filepath.Join(home, "escrow", "acme.escrow")); err != nil {
		t.Fatalf("setup refused a good path: %v", err)
	}
	// The same rule names the instance it is asked about: the same path is fine for another.
	if err := setupRule("beta", inside); err != nil {
		t.Fatalf("another instance's state directory was refused for beta: %v", err)
	}
	bootstrapRule := escrowPathRule(false)
	if err := bootstrapRule("acme", inside); err == nil || !strings.Contains(err.Error(), "dcctl destroy") {
		t.Fatalf("bootstrap accepted an escrow path inside the instance's state directory: %v", err)
	}
	// A re-run checks an artifact already there against its key, so the screen does not
	// refuse it, exactly as the plain bootstrap does not.
	if err := bootstrapRule("acme", existing); err != nil {
		t.Fatalf("bootstrap refused an existing artifact a re-run would check: %v", err)
	}
}
