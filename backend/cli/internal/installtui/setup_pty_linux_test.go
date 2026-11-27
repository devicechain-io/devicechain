// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// dcctl setup local's whole flow on a real pseudo-terminal, with fakes for the two
// engines: the cluster journey to its approval and its run, the hand-over, the instance
// journey to its own approval, the escrow passphrase asked on screen during the run,
// and the instance's success. The child exits with Setup's own exit status, and the
// terminal it leaves must be the one it was given.

const setupPTYChildEnv = "DCCTL_SETUP_PTY_CHILD"

// setupPassphrase is what the parent types for the escrow passphrase. It is a canary:
// it must never be drawn, printed or left in the scrollback.
var setupPassphrase = canaryValue("escrow-passphrase")

func startSetupPTYChild(t *testing.T, scenario string) *ptyRun {
	t.Helper()
	r := &ptyRun{t: t, pty: openPTY(t), out: &syncBuffer{}, marker: filepath.Join(t.TempDir(), "marker")}
	r.before = termios(t, r.pty.slave)
	r.cmd = exec.Command(os.Args[0], "-test.run=^TestSetupPTYChild$", "-test.count=1")
	r.cmd.Env = []string{
		setupPTYChildEnv + "=" + scenario, ptyMarkerEnv + "=" + r.marker,
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
		"TERM=xterm-256color", "LANG=C.UTF-8",
	}
	if d := os.Getenv("GOCOVERDIR"); d != "" {
		r.cmd.Env = append(r.cmd.Env, "GOCOVERDIR="+d)
	}
	r.cmd.Stdin, r.cmd.Stdout, r.cmd.Stderr = r.pty.slave, r.pty.slave, r.pty.slave
	r.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(r.out, r.pty.master) }()
	t.Cleanup(func() { _ = r.cmd.Process.Kill() })
	return r
}

// settled waits out a screen's settle before a key is typed: keys typed sooner are
// dropped by design.
func settled() { time.Sleep(700 * time.Millisecond) }

// throughCluster drives the cluster journey from its welcome to the hand-over.
func (r *ptyRun) throughCluster() {
	r.t.Helper()
	waitFor(r.t, "the welcome screen", r.out, screenShows(r.out, welcomeNoChanges))
	r.press("\r")
	waitFor(r.t, "the checks", r.out, screenShows(r.out, "all checks passed"))
	r.press("\x1b[Z\r") // Shift+Tab to Continue
	waitFor(r.t, "the cluster choices", r.out, screenShows(r.out, choicesTitle))
	r.press("\t\t\t\t\r")
	waitFor(r.t, "the cluster review", r.out, screenShows(r.out, reviewCommandTitle))
	settled()
	r.press("\t\r") // Back -> Approve
	waitFor(r.t, "the cluster verdict", r.out, screenShows(r.out, "Press Enter to continue."))
	r.press("\r")
	waitFor(r.t, "the hand-over", r.out, screenShows(r.out, handoverTitle))
}

// toInstanceReview drives the hand-over and the instance choices to the instance review.
func (r *ptyRun) toInstanceReview() {
	r.t.Helper()
	settled()
	r.press("\r") // Continue to the instance
	waitFor(r.t, "the instance screen", r.out, screenShows(r.out, "Choose the instance to create"))
	r.press("acme")
	r.press(strings.Repeat("\t", 7) + "\r") // name .. Continue
	waitFor(r.t, "the escrow screen", r.out, screenShows(r.out, "Root-key escrow"))
	r.press(strings.Repeat("\t", 4) + "\r") // write .. Continue
	waitFor(r.t, "the instance review", r.out, screenShows(r.out, "Approve and create the instance"))
}

// 🔴 THE WHOLE JOURNEY, ON A REAL TERMINAL: two approvals, the passphrase typed on screen
// and never drawn, exit 0, and the terminal restored.
func TestSetupPTYTheFullFlowSucceeds(t *testing.T) {
	r := startSetupPTYChild(t, "success")
	r.throughCluster()
	if m := r.markers(); m != "cluster ran\n" {
		t.Fatalf("after the cluster stage the engines saw %q", m)
	}
	r.toInstanceReview()
	if m := r.markers(); m != "cluster ran\n" {
		t.Fatalf("the instance run started before its approval: %q", m)
	}
	settled()
	r.press("\t\r") // Back -> Approve
	waitFor(t, "the passphrase form", r.out, func() bool { return strings.Contains(r.markers(), "asked") })
	time.Sleep(200 * time.Millisecond)
	r.press(setupPassphrase + "\r")
	waitFor(t, "the instance success", r.out, screenShows(r.out, "Instance acme is ready."))
	r.press("\r")
	if code := r.wait(); code != SetupExitSucceeded {
		t.Fatalf("exit %d; terminal:\n%q", code, r.out.String())
	}
	if m := r.markers(); m != "cluster ran\ninstance ran\nasked\nanswered\n" {
		t.Fatalf("the engines saw %q", m)
	}
	out := r.out.String()
	testkit.AssertNoCanary(t, "the terminal", []byte(out))
	assertRestored(t, out, "dcctl bootstrap: succeeded")
}

// Leaving on the hand-over: the cluster stays installed, setup says so and names the
// command that continues, and exits 3.
func TestSetupPTYExitOnTheHandoverLeavesTheClusterInstalled(t *testing.T) {
	r := startSetupPTYChild(t, "success")
	r.throughCluster()
	settled()
	r.press("\t\r") // Continue -> Exit
	if code := r.wait(); code != SetupExitInstanceNotCreated {
		t.Fatalf("exit %d; terminal:\n%q", code, r.out.String())
	}
	if m := r.markers(); m != "cluster ran\n" {
		t.Fatalf("the engines saw %q", m)
	}
	assertRestored(t, r.out.String(), "dcctl bootstrap local <instance> --cluster=devicechain")
	if !strings.Contains(r.out.String(), "stays installed") {
		t.Fatalf("the cluster was not said to stay installed:\n%q", r.out.String())
	}
}

// A failed instance run exits 4, and names the chosen instance's command.
func TestSetupPTYAFailedInstanceExitsFour(t *testing.T) {
	r := startSetupPTYChild(t, "instance-fails")
	r.throughCluster()
	r.toInstanceReview()
	settled()
	r.press("\t\r")
	waitFor(t, "the verdict", r.out, screenShows(r.out, "Bootstrap failed."))
	r.press("\r")
	if code := r.wait(); code != SetupExitInstanceFailed {
		t.Fatalf("exit %d; terminal:\n%q", code, r.out.String())
	}
	assertRestored(t, r.out.String(), "dcctl bootstrap local acme --cluster=devicechain")
}

// A failed cluster stage exits 1 and never shows the hand-over.
func TestSetupPTYAFailedClusterNeverReachesTheInstance(t *testing.T) {
	r := startSetupPTYChild(t, "cluster-fails")
	waitFor(t, "the welcome screen", r.out, screenShows(r.out, welcomeNoChanges))
	r.press("\r")
	waitFor(t, "the checks", r.out, screenShows(r.out, "all checks passed"))
	r.press("\x1b[Z\r")
	waitFor(t, "the cluster choices", r.out, screenShows(r.out, choicesTitle))
	r.press("\t\t\t\t\r")
	waitFor(t, "the cluster review", r.out, screenShows(r.out, reviewCommandTitle))
	settled()
	r.press("\t\r")
	waitFor(t, "the cluster verdict", r.out, screenShows(r.out, "Install failed."))
	r.press("\r")
	if code := r.wait(); code != SetupExitClusterFailed {
		t.Fatalf("exit %d; terminal:\n%q", code, r.out.String())
	}
	if strings.Contains(r.out.String(), handoverTitle) || strings.Contains(r.markers(), "instance") {
		t.Fatalf("the instance stage was reached after a failed cluster stage; markers %q", r.markers())
	}
}

// 🔴 A SECOND CTRL+C AFTER THE INSTANCE'S APPROVAL EXITS 4: the cluster is installed and
// the instance may be partly built. It never exits 1, which would say the cluster failed.
func TestSetupPTYADoubleCtrlCAfterTheInstanceApprovalExitsFour(t *testing.T) {
	r := startSetupPTYChild(t, "instance-stubborn")
	r.throughCluster()
	r.toInstanceReview()
	settled()
	r.press("\t\r")
	waitFor(t, "the instance run", r.out, func() bool { return strings.Contains(r.markers(), "instance ran") })
	r.press("\x03")
	waitFor(t, "the cancel", r.out, func() bool { return strings.Contains(r.markers(), "cancelled") })
	r.press("\x03")
	if code := r.wait(); code != SetupExitInstanceFailed {
		t.Fatalf("exit %d; terminal:\n%q", code, r.out.String())
	}
	assertRestored(t, r.out.String(), "interrupted again")
}

// Ctrl+C before the cluster's approval exits 2: nothing was changed.
func TestSetupPTYCtrlCBeforeTheClusterApprovalExitsTwo(t *testing.T) {
	r := startSetupPTYChild(t, "success")
	waitFor(t, "the welcome screen", r.out, screenShows(r.out, welcomeNoChanges))
	r.press("\x03")
	if code := r.wait(); code != SetupExitNothingChanged {
		t.Fatalf("exit %d; terminal:\n%q", code, r.out.String())
	}
	if m := r.markers(); m != "" {
		t.Fatalf("an engine ran: %q", m)
	}
}

// Ctrl+C on the cluster's success screen declines the instance: no hand-over, exit 3,
// and the command that creates the instance later.
func TestSetupPTYCtrlCAfterTheClusterSucceededExitsThree(t *testing.T) {
	r := startSetupPTYChild(t, "success")
	waitFor(t, "the welcome screen", r.out, screenShows(r.out, welcomeNoChanges))
	r.press("\r")
	waitFor(t, "the checks", r.out, screenShows(r.out, "all checks passed"))
	r.press("\x1b[Z\r")
	waitFor(t, "the cluster choices", r.out, screenShows(r.out, choicesTitle))
	r.press("\t\t\t\t\r")
	waitFor(t, "the cluster review", r.out, screenShows(r.out, reviewCommandTitle))
	settled()
	r.press("\t\r")
	waitFor(t, "the cluster verdict", r.out, screenShows(r.out, "Press Enter to continue."))
	r.press("\x03")
	if code := r.wait(); code != SetupExitInstanceNotCreated {
		t.Fatalf("exit %d; terminal:\n%q", code, r.out.String())
	}
	out := r.out.String()
	if strings.Contains(out, handoverTitle) {
		t.Fatal("the hand-over was opened after Ctrl+C")
	}
	assertRestored(t, out, "dcctl bootstrap local <instance> --cluster=devicechain")
}

// An engine panic after the instance's approval exits 4, with the terminal restored.
func TestSetupPTYAnInstancePanicExitsFour(t *testing.T) {
	r := startSetupPTYChild(t, "instance-panic")
	r.throughCluster()
	r.toInstanceReview()
	settled()
	r.press("\t\r")
	if code := r.wait(); code != SetupExitInstanceFailed {
		t.Fatalf("exit %d; terminal:\n%q", code, r.out.String())
	}
	assertRestored(t, r.out.String(), "panicked: instance engine exploded")
}

// TestSetupPTYChild is the child half. It does nothing unless a parent started it.
func TestSetupPTYChild(t *testing.T) {
	scenario := os.Getenv(setupPTYChildEnv)
	if scenario == "" {
		return
	}
	reviewSettle = 500 * time.Millisecond // the real settle: the parent waits it out
	mark := func(s string) {
		f, err := os.OpenFile(os.Getenv(ptyMarkerEnv), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(s + "\n")
			_ = f.Close()
		}
	}
	in := Probe(os.Stdin, os.Stdout, os.Stderr)
	in.UI, in.NoSplash = terminal.TUI, true
	caps := terminal.Evaluate(in)
	if !caps.TUI || caps.ProgressOnly {
		fmt.Fprintf(os.Stderr, "the pty was not eligible: %+v\n", caps)
		os.Exit(9)
	}
	cluster := engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		mark("cluster ran")
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		if scenario == "cluster-fails" {
			return installflow.Summary{Outcome: installflow.RunFailed, InstallRecord: installflow.RecordUnknown,
				Readiness: installflow.ReadinessUnknown, Claim: installflow.ClaimUnknown,
				PartialState: installflow.EffectMayHaveChanged}, errors.New("the operator rollout timed out")
		}
		return succeeded, endRun(rt)
	})
	instance := engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		mark("instance ran")
		switch scenario {
		case "instance-stubborn":
			<-ctx.Done()
			mark("cancelled")
			select {} // still stopping gracefully
		case "instance-panic":
			panic("instance engine exploded")
		}
		if err := rt.Events.Emit(installflow.Event{Payload: installflow.RunStarted{
			Target: installflow.Target{Mode: "existing", Context: installflow.Text("kind-devicechain")},
			Manifest: []installflow.ManifestStep{{ID: installflow.StepBootstrapInputs}, {ID: installflow.StepBootstrapEscrow},
				{ID: installflow.StepBootstrapHelm}},
		}}); err != nil {
			return installflow.Summary{}, err
		}
		step := func(id installflow.StepID, run func() error) error {
			if err := rt.Events.Emit(installflow.Event{Step: id, Attempt: 1,
				Payload: installflow.StepStarted{Message: installflow.MessageCode(id)}}); err != nil {
				return err
			}
			if err := run(); err != nil {
				return err
			}
			return rt.Events.Emit(installflow.Event{Step: id, Attempt: 1,
				Payload: installflow.StepFinished{Outcome: installflow.StepSucceeded}})
		}
		if err := step(installflow.StepBootstrapEscrow, func() error {
			if scenario == "instance-fails" {
				return nil
			}
			mark("asked")
			ans, err := rt.Prompts.Ask(ctx, installflow.Prompt{Step: installflow.StepBootstrapEscrow,
				Kind: installflow.PromptSecret, Question: installflow.QuestionEscrowPassphrase,
				Params: []installflow.Param{{Name: "instance", Value: installflow.Text("acme")}}})
			if err != nil {
				return err
			}
			defer ans.Secret.Zero()
			if string(ans.Secret.Expose()) != setupPassphrase {
				return errors.New("the passphrase reached the run changed")
			}
			mark("answered")
			return nil
		}); err != nil {
			return cancelled, err
		}
		if scenario == "instance-fails" {
			failed := installflow.Summary{Outcome: installflow.RunFailed, InstallRecord: installflow.RecordInstalled,
				Readiness: installflow.ReadinessUnknown, Claim: installflow.ClaimReleased,
				PartialState: installflow.EffectMayHaveChanged, FailedStep: installflow.StepBootstrapHelm}
			return failed, errors.New("helm: timed out waiting for the release")
		}
		return okBootstrapSummary(), step(installflow.StepBootstrapHelm, func() error { return nil })
	})
	s := Setup{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Caps: caps,
		Cluster: cluster, ClusterRequest: installflow.Request{Operation: installflow.OperationInstall},
		ClusterPreflight: runnerOf(preCheck(installflow.CheckPass, "kubectl", "")),
		Wizard:           &Wizard{Facts: preFacts()},
		Instance: func() *BootstrapScreens {
			return &BootstrapScreens{
				Setup: BootstrapSetup{Version: "v1.12.0", Target: "kind cluster devicechain",
					TargetArgs: []string{"--cluster=devicechain"},
					Escrow: EscrowSeed{DefaultPath: func(n string) string {
						return "/home/op/.devicechain/escrow/" + n + "-rootkey.escrow"
					}}},
				Handover: &Handover{Target: "kind cluster devicechain",
					Continue: "dcctl bootstrap local <instance> --cluster=devicechain"},
				Plan: func(p BootstrapPlanned) (BootstrapPlan, error) {
					return BootstrapPlan{Engine: instance, Review: Review{Rows: []ReviewRow{{Label: "Instance", Value: p.Instance.Name}}},
						Command: BootstrapCommand(p.Instance, p.Escrow, []string{"--cluster=devicechain"})}, nil
				},
				Facts: func(installflow.Summary) InstanceFacts {
					return InstanceFacts{ConsoleURL: "https://devicechain.local/", Account: "superuser@devicechain.local"}
				},
			}
		},
		ContinueCommand: func(p *BootstrapPlanned) string {
			if p == nil {
				return "dcctl bootstrap local <instance> --cluster=devicechain"
			}
			return BootstrapContinueCommand(p.Instance, p.Escrow, []string{"--cluster=devicechain"})
		},
	}
	res := s.Run(context.Background())
	if res.Err != nil {
		fmt.Fprintln(os.Stderr, "Error:", res.Err)
	}
	os.Exit(res.ExitCode())
}
