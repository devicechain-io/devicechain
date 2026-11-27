// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/installtui/result"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// dcctl bootstrap --ui=tui --yes (or --dev) is a progress-only instance journey: there is
// no review, because the command line approved it, so the engine it was handed IS the run.
// It starts at once, its passphrase is still asked on the form and never drawn, it reveals
// nothing, and the exit summary names where the password is kept.
func TestAProgressOnlyInstanceRunIsTheCommandLinesRun(t *testing.T) {
	pass := canaryValue("escrow-passphrase")
	caps := tuiCaps(true)
	caps.Width, caps.Height, caps.SizeKnown, caps.Layout = 100, 30, true, terminal.Full
	h := newHarness(t, caps)
	scr := &BootstrapScreens{Setup: bsSetup(caps)}
	scr.Setup.Instance.Name = "acme"
	h.p.Bootstrap = scr
	var ran atomic.Bool
	got := make(chan string, 1)
	done := h.run(engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		ran.Store(true)
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		ans, err := rt.Prompts.Ask(ctx, installflow.Prompt{Step: installflow.StepBootstrapEscrow, Kind: installflow.PromptSecret,
			Question: installflow.QuestionEscrowPassphrase, Params: []installflow.Param{{Name: "instance", Value: installflow.Text("acme")}}})
		if err != nil {
			return installflow.Summary{}, err
		}
		got <- string(ans.Secret.Expose())
		ans.Secret.Zero()
		rt.Reveals.Offer(installflow.RevealOffer{Step: installflow.StepBootstrapReport, Subject: installflow.RevealSuperuserSeed,
			Where: seedRef, Reveal: installflow.NewReveal([]byte(canaryValue("superuser-seed")))})
		if err := endRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		return okBootstrapSummary(), nil
	}), installflow.Request{Operation: installflow.OperationBootstrap, AssumeYes: true})
	waitFor(t, "the passphrase form", h.out, screenShows(h.out, "Escrow passphrase"))
	h.press(pass + "\r")
	if err := h.result(done); err != nil {
		t.Fatalf("the command line's run did not run as it asked: %v\n%q", err, h.out.String())
	}
	if !ran.Load() {
		t.Fatal("the engine never ran")
	}
	if a := <-got; a != pass {
		t.Fatal("the passphrase typed on the form is not what reached the run")
	}
	out := h.out.String()
	testkit.AssertNoCanary(t, "the terminal", []byte(out))
	assertRestored(t, out, "dcctl bootstrap: succeeded")
	if !strings.Contains(out, "password: kept in Secret dci-acme/dci-acme-superuser, key password") {
		t.Fatalf("the exit summary does not say where the password is kept:\n%q", out)
	}
}

// The screens the instance journey begins on are still the gate when they are up: a
// presenter with instance screens and no progress-only run starts nothing on its own.
func TestAnInstanceJourneyWithScreensStillWaitsForItsApproval(t *testing.T) {
	h := newHarness(t, tuiCaps(false))
	scr := instScreens(goodPlan)
	scr.Handover = nil
	h.p.Bootstrap = scr
	var ran atomic.Bool
	done := h.run(engineFunc(func(context.Context, installflow.Runtime) (installflow.Summary, error) {
		ran.Store(true)
		return okBootstrapSummary(), nil
	}), installflow.Request{Operation: installflow.OperationBootstrap})
	waitFor(t, "the instance screen", h.out, screenShows(h.out, "Choose the instance to create"))
	h.press("\x03")
	if err := h.result(done); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("leaving the instance screen: %v", err)
	}
	if ran.Load() {
		t.Fatal("the engine the command passed ran without the review's approval")
	}
}

// After dcctl install --ui=tui succeeds, its verdict and its exit summary name the next
// step in the terminal UI: dcctl bootstrap ... --ui=tui, on the cluster the run installed.
func TestAnInstallSuccessOffersTheInstanceTerminalUI(t *testing.T) {
	caps := tuiCaps(true)
	h := newHarness(t, caps)
	f := preFacts()
	f.Provider, f.KubeContext, f.Cluster = "local", "", "edge"
	h.p.Wizard = &Wizard{Facts: f}
	err := h.result(h.run(engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		return succeeded, endRun(rt)
	}), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}))
	if err != nil {
		t.Fatal(err)
	}
	const want = "create an instance: dcctl bootstrap local <instance> --cluster edge --ui=tui"
	if out := h.out.String(); !strings.Contains(out, want) {
		t.Fatalf("the exit summary does not offer %q:\n%q", want, out)
	}

	// The verdict on screen says the same, and only for a success.
	m := newModel(tuiCaps(false), &interrupter{}, func() {}, emptyLogs{}, nil,
		modelOptions{next: func() string { return BootstrapNext(f, result.POSIX) }})
	m.width, m.height = 120, 40
	s := succeeded
	m.summary = &s
	if v := m.render(); !strings.Contains(v, "Create an instance: dcctl bootstrap local <instance> --cluster edge --ui=tui") {
		t.Fatalf("the verdict does not offer the instance terminal UI:\n%s", v)
	}
	failed := installflow.Summary{Outcome: installflow.RunFailed}
	m.summary = &failed
	if v := m.render(); strings.Contains(v, "Create an instance") {
		t.Fatalf("a failed install offered the next step:\n%s", v)
	}

	// An existing cluster is named by its context.
	f.KubeContext = "prod"
	if c := BootstrapNext(f, result.POSIX); c != "dcctl bootstrap local <instance> --kube-context prod --ui=tui" {
		t.Fatalf("the next step on an existing cluster: %q", c)
	}
}

// Inside dcctl setup local the cluster stage goes on to the instance itself, so its
// welcome does not send the operator off to run dcctl bootstrap later.
func TestSetupsClusterWelcomeSaysTheInstanceFollows(t *testing.T) {
	p := newTestPrerun(&Wizard{Facts: preFacts()}, nil)
	p.continues = true
	view := strings.Join(strings.Fields(screenText(p)), " ")
	if !strings.Contains(view, welcomeScopeSetup) {
		t.Fatalf("setup's welcome does not say the instance follows:\n%s", view)
	}
	if strings.Contains(view, "Create an instance later") {
		t.Fatalf("setup's welcome still sends the operator to dcctl bootstrap:\n%s", view)
	}
}

// The command setup prints to create the instance later opens the same screens.
func TestTheContinueCommandOpensTheInstanceTerminalUI(t *testing.T) {
	in := InstanceChoices{Name: "acme", Given: map[string]bool{"name": true}}
	c := BootstrapContinueCommand(in, EscrowChoice{Mode: EscrowWrite}, []string{"--cluster=devicechain"})
	if c != "dcctl bootstrap local acme --cluster=devicechain --ui=tui" {
		t.Fatalf("the continue command: %q", c)
	}
	// The equivalent command reproduces the approval instead, and never names --ui.
	if c := BootstrapCommand(in, EscrowChoice{Mode: EscrowWrite}, nil); strings.Contains(c, "--ui") {
		t.Fatalf("the equivalent command names a presentation flag: %q", c)
	}
}

// The escrow screen hands its check the instance named on the instance screen, so the
// engine's rule (outside THAT instance's state directory) can be applied.
func TestTheEscrowPathIsCheckedForTheChosenInstance(t *testing.T) {
	s := bsSetup(bsCaps(terminal.L1, 100, 40))
	var asked []string
	s.Escrow.ValidatePath = func(instance, path string) error {
		asked = append(asked, instance+" "+path)
		return nil
	}
	d := newBSDriver(t, s)
	d.throughPlan()
	if len(asked) != 1 || asked[0] != "acme /home/op/.devicechain/escrow/acme-rootkey.escrow" {
		t.Fatalf("the path check was asked %q", asked)
	}
}
