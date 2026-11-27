// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
	"github.com/spf13/pflag"
)

// withSetup replaces setup's probe and stage runner for one test, and resets its flags.
func withSetup(t *testing.T, probe func() terminal.Inputs,
	run func(context.Context, installtui.Setup) installtui.SetupResult) {
	t.Helper()
	saveProbe, saveRun, saveVersion := probeInstallTerminal, runSetup, bootstrap.DefaultImageVersion
	t.Cleanup(func() {
		probeInstallTerminal, runSetup, bootstrap.DefaultImageVersion = saveProbe, saveRun, saveVersion
		resetSetupFlags(t)
	})
	bootstrap.DefaultImageVersion = "v1.12.0" // a released dcctl: its published images resolve
	probeInstallTerminal, runSetup = probe, run
	resetSetupFlags(t)
}

func resetSetupFlags(t *testing.T) {
	t.Helper()
	setupLocalCmd.Flags().VisitAll(func(f *pflag.Flag) {
		if err := f.Value.Set(f.DefValue); err != nil {
			t.Fatal(err)
		}
		f.Changed = false
	})
}

// 🔴 GUIDED ONLY, FAIL CLOSED: without an interactive terminal setup refuses before
// anything starts, says how to do the same by hand, and exits 2 (nothing changed). It
// never falls back to running the stages unattended.
func TestSetupLocalRefusesWithoutAnInteractiveTerminal(t *testing.T) {
	for name, mut := range map[string]func(*terminal.Inputs){
		"stdin redirected":  func(in *terminal.Inputs) { in.InputTTY = false },
		"stdout redirected": func(in *terminal.Inputs) { in.OutputTTY = false },
		"stderr redirected": func(in *terminal.Inputs) { in.ErrorTTY = false },
		"CI":                func(in *terminal.Inputs) { in.Env["CI"] = "true" },
		"TERM=dumb":         func(in *terminal.Inputs) { in.Env["TERM"] = "dumb" },
	} {
		t.Run(name, func(t *testing.T) {
			withSetup(t, func() terminal.Inputs { in := eligibleTerminal(); mut(&in); return in },
				func(context.Context, installtui.Setup) installtui.SetupResult {
					t.Fatal("a stage ran without an interactive terminal")
					return installtui.SetupResult{}
				})
			err := setupLocalCmd.RunE(setupLocalCmd, nil)
			if exitCode(err) != installtui.SetupExitNothingChanged || !errors.Is(err, errSetupNeedsTerminal) {
				t.Fatalf("exit %d: %v", exitCode(err), err)
			}
			for _, w := range []string{"dcctl install local", "dcctl bootstrap local <instance>", "Nothing was changed"} {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the refusal does not say %q:\n%s", w, err)
				}
			}
		})
	}
}

// There is no flag that approves anything, and a bad flag or an argument is refused with
// the nothing-changed status.
func TestSetupLocalHasNoApprovalFlagAndRefusesBadInput(t *testing.T) {
	for _, name := range []string{"yes", "dry-run", "dev", "ui"} {
		if setupLocalCmd.Flags().Lookup(name) != nil {
			t.Errorf("dcctl setup local has --%s", name)
		}
	}
	if setupLocalCmd.Flags().ShorthandLookup("y") != nil {
		t.Error("dcctl setup local has -y")
	}
	perr := setupLocalCmd.ParseFlags([]string{"--yes"})
	if perr == nil {
		t.Fatal("--yes was accepted")
	}
	if code := exitCode(setupLocalCmd.FlagErrorFunc()(setupLocalCmd, perr)); code != installtui.SetupExitNothingChanged {
		t.Fatalf("a flag error exits %d", code)
	}
	if code := exitCode(setupLocalCmd.Args(setupLocalCmd, []string{"acme"})); code != installtui.SetupExitNothingChanged {
		t.Fatalf("an argument exits %d", code)
	}
	if !strings.HasPrefix(setupLocalCmd.Short, "Preview. ") || !strings.HasPrefix(setupCmd.Short, "Preview. ") {
		t.Fatal("setup is not labelled Preview")
	}
	setupLocalCmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Name != "help" && !strings.HasPrefix(f.Usage, "Preview. ") {
			t.Errorf("--%s is not labelled Preview", f.Name)
		}
	})
}

// The command's exit status is the stages' result, through the root command's own
// exit-code mapping: 0 only when both stages succeeded.
func TestSetupLocalExitsWithTheStagesResult(t *testing.T) {
	for _, c := range []struct {
		res  installtui.SetupResult
		want int
	}{
		{installtui.SetupResult{Cluster: installtui.PhaseSucceeded, Instance: installtui.PhaseSucceeded}, 0},
		{installtui.SetupResult{Cluster: installtui.PhaseFailed, Err: errors.New("rollout")}, 1},
		{installtui.SetupResult{Cluster: installtui.PhaseNotApproved, Err: errors.New("not started")}, 2},
		{installtui.SetupResult{Cluster: installtui.PhaseSucceeded, Instance: installtui.PhaseNotApproved,
			Err: errors.New("not created")}, 3},
		{installtui.SetupResult{Cluster: installtui.PhaseSucceeded, Instance: installtui.PhaseFailed,
			Err: errors.New("helm")}, 4},
		{installtui.SetupResult{Cluster: installtui.PhaseSucceeded, Instance: installtui.PhaseNotApproved}, 3},
	} {
		withSetup(t, eligibleTerminal, func(context.Context, installtui.Setup) installtui.SetupResult { return c.res })
		err := setupLocalCmd.RunE(setupLocalCmd, nil)
		if got := exitCode(err); (err == nil) != (c.want == 0) || (err != nil && got != c.want) {
			t.Errorf("%+v: exit %d (%v), want %d", c.res, got, err, c.want)
		}
	}
}

// The stages the command builds: the install's own engine for the flags given, with no
// --yes, the install's screens, and an instance stage on the cluster the install ran on.
func TestSetupLocalWiresTheStages(t *testing.T) {
	var got installtui.Setup
	withSetup(t, eligibleTerminal, func(_ context.Context, s installtui.Setup) installtui.SetupResult {
		got = s
		return installtui.SetupResult{Cluster: installtui.PhaseSucceeded, Instance: installtui.PhaseSucceeded}
	})
	for _, a := range [][2]string{{"kube-context", "prod"}, {"compact", "true"}} {
		if err := setupLocalCmd.Flags().Set(a[0], a[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := setupLocalCmd.RunE(setupLocalCmd, nil); err != nil {
		t.Fatal(err)
	}
	ie, ok := got.Cluster.(*bootstrap.InstallEngine)
	if !ok {
		t.Fatalf("the cluster engine is %T", got.Cluster)
	}
	if o := ie.Options; o.KubeContext != "prod" || o.AssumeYes || o.DryRun {
		t.Fatalf("the install's options: context %q, yes %v, dry-run %v", o.KubeContext, o.AssumeYes, o.DryRun)
	}
	// The engine's own resolution is what names the cluster the instance is built on.
	if o, err := ie.ResolveOptions(); err != nil || !o.Compact || o.KubeContext != "prod" {
		t.Fatalf("the resolved options: compact %v, context %q, %v", o.Compact, o.KubeContext, err)
	}
	if r := got.ClusterRequest; r.Operation != installflow.OperationInstall || r.AssumeYes || r.DryRun {
		t.Fatalf("the cluster request: %+v", r)
	}
	if got.Wizard == nil || got.Wizard.Replan == nil || got.ClusterPreflight == nil || !got.OwnProcessStreams {
		t.Fatal("the cluster stage has no screens, no replan or no preflight")
	}
	scr := got.Instance()
	if scr.Handover == nil || scr.Handover.Continue != "dcctl bootstrap local <instance> --kube-context=prod" {
		t.Fatalf("the hand-over: %+v", scr.Handover)
	}
	if !slices.Equal(scr.Setup.TargetArgs, []string{"--kube-context=prod"}) ||
		!strings.Contains(scr.Setup.Instance.ClusterShape, "Compact") {
		t.Fatalf("the instance target: %v, %q", scr.Setup.TargetArgs, scr.Setup.Instance.ClusterShape)
	}
	if c := got.ContinueCommand(&installtui.BootstrapPlanned{Instance: installtui.InstanceChoices{Name: "acme",
		Given: map[string]bool{"name": true}}}); c != "dcctl bootstrap local acme --kube-context=prod" {
		t.Fatalf("the continue command: %q", c)
	}
}

// An instance plan is built through the bootstrap command's own flags, exactly as the
// command line it names would build it, and nothing one plan parsed is carried into the
// next.
func TestSetupBuildsTheInstanceRunThroughTheBootstrapFlags(t *testing.T) {
	t.Cleanup(func() {
		if _, _, err := setupBootstrapRun([]string{"dcctl", "bootstrap", "local", "reset"}, nil); err != nil {
			t.Fatal(err)
		}
	})
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	scr := setupInstanceScreens(setupTarget{Cluster: "devicechain"})
	first := installtui.BootstrapPlanned{
		Instance: installtui.InstanceChoices{Name: "acme", Profile: "default", Areas: []string{"lwm2m-ingest"},
			Host: "localhost", ObjectStore: "none",
			Given: map[string]bool{"name": true, "enable-area": true, "host": true, "object-store": true}},
		Escrow: installtui.EscrowChoice{Mode: installtui.EscrowNone},
	}
	plan, err := scr.Plan(first)
	if err != nil {
		t.Fatal(err)
	}
	e := plan.Engine.(*bootstrap.BootstrapEngine)
	if e.Instance != "acme" || e.Cluster != "devicechain" || !e.Escrow.NoEscrow || e.AssumeYes || e.DryRun {
		t.Fatalf("the first plan: %+v", e)
	}
	if e.PersonAtTerminal == nil || !e.PersonAtTerminal() {
		t.Fatal("the instance run does not know a person is at the terminal to ask for the passphrase")
	}
	if !slices.Equal(bootstrapEnableAreas, []string{"lwm2m-ingest"}) || bootstrapHost != "localhost" {
		t.Fatalf("the flags parsed: areas %v, host %q", bootstrapEnableAreas, bootstrapHost)
	}
	if !strings.Contains(plan.Command, "--no-escrow") || !strings.HasSuffix(plan.Command, "--yes") {
		t.Fatalf("the equivalent command: %q", plan.Command)
	}
	second := installtui.BootstrapPlanned{
		Instance: installtui.InstanceChoices{Name: "beta", Profile: "default", Given: map[string]bool{"name": true}},
		Escrow:   installtui.EscrowChoice{Mode: installtui.EscrowWrite},
	}
	plan, err = scr.Plan(second)
	if err != nil {
		t.Fatal(err)
	}
	e = plan.Engine.(*bootstrap.BootstrapEngine)
	if e.Instance != "beta" || e.Escrow.NoEscrow || len(bootstrapEnableAreas) != 0 || bootstrapHost != "" ||
		bootstrapCmd.Flags().Changed("host") {
		t.Fatalf("the second plan carried the first's flags: no-escrow %v, areas %v, host %q", e.Escrow.NoEscrow,
			bootstrapEnableAreas, bootstrapHost)
	}
	if rows := plan.Review.Rows; len(rows) == 0 || rows[0].Value != "beta" {
		t.Fatalf("the review: %+v", rows)
	}
	// A plan the command line refuses is refused here, with its words.
	if _, err := scr.Plan(installtui.BootstrapPlanned{Instance: installtui.InstanceChoices{Name: "acme",
		Areas: []string{"no-such-area"}, Given: map[string]bool{"name": true, "enable-area": true}}}); err == nil {
		t.Fatal("an unknown area was planned")
	}
}
