// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/brand"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// countingEngine records whether it was ever run.
func countingEngine(ran *atomic.Int32) installflow.Engine {
	return engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		ran.Add(1)
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		return succeeded, endRun(rt)
	})
}

// 🔴 A RENDERER THAT FAILS BEFORE CONTINUE MUST NOT START AN INSTALL NOBODY CONFIRMED:
// not the request the command line resolved, and not the one a footprint switch
// replanned to. The run ends cancelled, having done nothing, and the exit is not 0.
func TestARendererFailureBeforeContinueStartsNothing(t *testing.T) {
	for _, switchFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("switched=%v", switchFirst), func(t *testing.T) {
			var original, replanned atomic.Int32
			h := wizardHarness(t, &Wizard{Facts: preFacts(), Replan: func(e Edits) (Replanned, error) {
				return Replanned{Engine: countingEngine(&replanned),
					Preflight: runnerOf(preCheck(installflow.CheckFail, "engine memory: 6.0Gi", capacityFix)), Facts: planFacts(e)}, nil
			}})
			h.p.Caps.ErrorTTY = true
			out := &failingWriter{}
			errs := &syncBuffer{}
			h.p.Stdout, h.p.Stderr = out, errs
			done := h.runWith(countingEngine(&original), runnerOf(preCheck(installflow.CheckPass, "kubectl", "")))
			waitFor(t, "the welcome screen", &out.syncBuffer, screenShows(&out.syncBuffer, welcomeNoChanges))
			if switchFirst {
				h.press("\r")
				waitFor(t, "the checks", &out.syncBuffer, screenShows(&out.syncBuffer, "all checks passed"))
				h.press("\t\r") // Recheck -> the footprint switch
				waitFor(t, "the consequences", &out.syncBuffer, screenShows(&out.syncBuffer, "Switch to Compact?"))
				h.press("\r")
				waitFor(t, "the switched checks", &out.syncBuffer, screenShows(&out.syncBuffer, "1 failure"))
			}
			out.failing.Store(true)
			h.press("\t") // a redraw, which the dead screen refuses
			err := h.result(done)
			if err == nil {
				t.Fatal("a renderer failure before Continue returned success")
			}
			if original.Load() != 0 || replanned.Load() != 0 {
				t.Fatalf("an unconfirmed install ran: original %d, replanned %d", original.Load(), replanned.Load())
			}
			if !strings.Contains(out.String()+errs.String(), "dcctl install: cancelled") &&
				!strings.Contains(errs.String(), "not started") {
				t.Errorf("the outcome is not said:\nstdout %q\nstderr %q", out.String(), errs.String())
			}
		})
	}
}

// One OS interrupt on the screens before the run is the single cancel: the run settles,
// nothing starts, and the program ends without a key. It must not hang on the welcome
// screen.
func TestASignalBeforeTheRunSettlesIt(t *testing.T) {
	var ran atomic.Int32
	h := wizardHarness(t, &Wizard{Facts: preFacts()})
	done := h.runWith(countingEngine(&ran), runnerOf(preCheck(installflow.CheckPass, "kubectl", "")))
	waitFor(t, "the welcome screen", h.out, screenShows(h.out, welcomeNoChanges))
	sigs := <-h.sigs
	sigs <- interruptSignals[0]
	err := h.result(done)
	if err == nil || ran.Load() != 0 {
		t.Fatalf("a signal before the run: err %v, engine ran %d", err, ran.Load())
	}
}

// Exit while the screen's checks run stops them, and the presenter does not return
// before they have: a check's tool is never left running behind the command.
func TestExitJoinsTheScreensChecks(t *testing.T) {
	var finished atomic.Bool
	started := make(chan struct{})
	runner := installflow.PreflightFunc(func(ctx context.Context, observe func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		close(started)
		<-ctx.Done() // a tool that ends when the context kills it
		time.Sleep(150 * time.Millisecond)
		finished.Store(true)
		return installflow.PreflightResult{}, ctx.Err()
	})
	var ran atomic.Int32
	h := wizardHarness(t, &Wizard{Facts: preFacts()})
	done := h.runWith(countingEngine(&ran), runner)
	waitFor(t, "the welcome screen", h.out, screenShows(h.out, welcomeNoChanges))
	h.press("\r")
	<-started
	h.press("q\r")
	if err := h.result(done); err == nil {
		t.Fatal("Exit returned success")
	}
	if !finished.Load() {
		t.Fatal("the presenter returned while the screen's checks were still running")
	}
}

// At every accepted size the action row is on screen: facts and rows give way, never
// the actions whose keys are live.
func TestTheActionsAreVisibleAtEveryAcceptedSize(t *testing.T) {
	for _, sc := range preScenarios {
		for _, lv := range preLevels {
			for w := prerunMinWidth; w <= 100; w += 5 {
				for h := prerunMinHeight; h <= 24; h++ {
					p := sc.make(preCaps(lv.level, w, h))
					view := preStrip(p.render())
					preFits(t, view, w, h)
					var want []string
					switch {
					case p.ch.picker != nil, p.ch.drawer:
						// An overlay: its only keys are in the footer.
					case p.ch.naming != nil:
						want = []string{nameTitle}
					case p.screen == screenChoices:
						want = []string{"Continue", "Advanced", "Preflight", "Exit"}
					case p.screen == screenWelcome:
						want = []string{"Continue", "Exit"}
					case p.pf.confirm:
						want = []string{"Switch to " + footprintWord(!p.facts.Compact) + "?"}
					default:
						acts, _ := p.actions()
						for _, a := range acts {
							want = append(want, actionLabel(a, p.facts.Compact))
						}
					}
					for _, s := range want {
						if !strings.Contains(view, s) {
							t.Fatalf("%s %s %dx%d: %q is not on screen:\n%s", sc.name, lv.name, w, h, s, view)
						}
					}
				}
			}
		}
	}
}

// The version shown on the splash is sanitised and redacted like every other fact.
func TestTheSplashVersionIsSanitisedAndRedacted(t *testing.T) {
	const canary = "canary-version-s3cr3t-9917"
	hub, err := installflow.NewHub(installflow.Options{Operation: installflow.OperationInstall,
		Sink: installflow.SinkFunc(func(installflow.Envelope) {})})
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.AddSecret(canary); err != nil {
		t.Fatal(err)
	}
	caps := splashCaps(120, 40)
	caps.Splash, caps.Animate, caps.Level = terminal.TextHeader, false, terminal.L1
	m := newModel(caps, &interrupter{}, func() {}, nil, nil,
		modelOptions{version: splashVersion(&Wizard{Facts: Facts{Version: "v1 \x1b]0;t\x07" + canary}}, hub.Redact)})
	view := m.splashM.Splash().View()
	if strings.Contains(view, canary) || strings.Contains(view, "\x1b]") || !strings.Contains(view, "v1") {
		t.Fatalf("the splash version: %q", view)
	}
	_ = brand.SkipHint
}

// When even the head and the tail do not fit, stack keeps the END of the body, so the
// actions (the tail's last lines) are the last thing to go.
func TestStackKeepsTheActionsWhenNothingElseFits(t *testing.T) {
	head := []string{"title", "scope one", "scope two"}
	tail := []string{"tally", "  Continue", "› Exit"}
	got := stack(head, []string{"row 1", "row 2"}, tail, 4, -1, -1, "…")
	if len(got) != 4 || got[2] != "  Continue" || got[3] != "› Exit" {
		t.Fatalf("stack in 4 rows: %q", got)
	}
	if got := stack(head, nil, tail, 2, -1, -1, "…"); len(got) != 2 || got[1] != "› Exit" {
		t.Fatalf("stack in 2 rows: %q", got)
	}
}

var markerLine = regexp.MustCompile(`^… (\d+) lines? (above|below)$|^… (\d+) lines? above, (\d+) below$`)

// scroll never lets a marker take a shown row's place, keeps the focused row's first line
// in view at every room, and counts exactly what it hides.
func TestScrollCountsExactlyAndKeepsTheFocusedRow(t *testing.T) {
	for n := 1; n <= 20; n++ {
		list := make([]string, n)
		for i := range list {
			list[i] = fmt.Sprintf("row %02d", i)
		}
		for room := 1; room <= n+1; room++ {
			for from := -1; from < n; from++ {
				for to := from; to < min(n, from+4); to++ {
					if from < 0 && to >= 0 {
						continue
					}
					got := scroll(list, room, from, to, "…")
					if len(got) > room {
						t.Fatalf("n=%d room=%d [%d,%d]: %d lines", n, room, from, to, len(got))
					}
					shown, hidden := 0, 0
					focus := max(from, 0)
					sawFocus := false
					for _, l := range got {
						if m := markerLine.FindStringSubmatch(l); m != nil {
							for _, g := range []string{m[1], m[3], m[4]} {
								var v int
								if g != "" {
									fmt.Sscan(g, &v)
									hidden += v
								}
							}
							continue
						}
						shown++
						if l == list[focus] {
							sawFocus = true
						}
					}
					if !sawFocus {
						t.Fatalf("n=%d room=%d [%d,%d]: the focused row is not shown: %q", n, room, from, to, got)
					}
					if room >= 2 && shown+hidden != n {
						t.Fatalf("n=%d room=%d [%d,%d]: %d shown + %d counted hidden != %d: %q", n, room, from, to, shown, hidden, n, got)
					}
				}
			}
		}
	}
}

// boundRunner has a bound form, as the install's doctor does, and records which ran.
type boundRunner struct {
	unbound, bound *atomic.Int32
	isBound        bool
}

func (r boundRunner) RunPreflight(context.Context, func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
	if r.isBound {
		r.bound.Add(1)
	} else {
		r.unbound.Add(1)
	}
	return installflow.PreflightResult{}, nil
}

func (r boundRunner) BoundToContext() installflow.PreflightRunner { r.isBound = true; return r }

// The screen runs the bound form of the preflight; the run's own stage runs the form it
// was given, which prints what it always printed even if the run is interrupted.
func TestTheScreenRunsTheBoundPreflightAndTheRunDoesNot(t *testing.T) {
	var unbound, bound atomic.Int32
	r := boundRunner{unbound: &unbound, bound: &bound}
	h := wizardHarness(t, &Wizard{Facts: preFacts()})
	done := h.runWith(engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		if _, err := rt.Preflight.RunPreflight(ctx, func(installflow.PreflightObservation) {}); err != nil {
			return installflow.Summary{}, err
		}
		return succeeded, endRun(rt)
	}), r)
	waitFor(t, "the welcome screen", h.out, screenShows(h.out, welcomeNoChanges))
	h.press("\r")
	waitFor(t, "the checks", h.out, screenShows(h.out, "all checks passed"))
	h.press("\x1b[Z\r") // Shift+Tab to Continue
	waitFor(t, "the cluster choices", h.out, screenShows(h.out, choicesTitle))
	h.press("\r") // Continue, focused: no replan, so the choices are read-only
	waitFor(t, "the verdict", h.out, screenShows(h.out, "Install succeeded."))
	h.press("\r")
	if err := h.result(done); err != nil {
		t.Fatal(err)
	}
	if bound.Load() != 1 || unbound.Load() != 1 {
		t.Fatalf("screen (bound) ran %d, run's stage (unbound) ran %d", bound.Load(), unbound.Load())
	}
}

// A secret that only a composed line spells (a label, its padding and a clean value
// together) is withheld with the whole line, as on the progress and result screens.
func TestAComposedLineThatSpellsASecretIsWithheld(t *testing.T) {
	hub, err := installflow.NewHub(installflow.Options{Operation: installflow.OperationInstall,
		Sink: installflow.SinkFunc(func(installflow.Envelope) {})})
	if err != nil {
		t.Fatal(err)
	}
	const composed = "Provider    local-canary"
	if err := hub.AddRedaction(composed); err != nil {
		t.Fatal(err)
	}
	f := preFacts()
	f.Provider = "local-canary" // clean on its own
	p := newPrerun(context.Background(), preCaps(terminal.L4, 120, 40), &Wizard{Facts: f}, nil, hub.Redact)
	view := preStrip(p.render())
	if strings.Contains(view, "local-canary") || !strings.Contains(view, withheld) {
		t.Fatalf("a composed secret reached the screen:\n%s", view)
	}
	if !strings.Contains(view, welcomeNoChanges) {
		t.Fatalf("clean lines were withheld too:\n%s", view)
	}
}
