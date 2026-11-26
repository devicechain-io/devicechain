// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// bsGolden compares out with testdata/<name>. The -update flag (the one
// teatest's golden package registers) rewrites it; run that locally, never in CI.
func bsGolden(t *testing.T, name string, out []byte) {
	t.Helper()
	path := filepath.Join("testdata", filepath.FromSlash(name))
	if f := flag.Lookup("update"); f != nil && f.Value.String() == "true" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(want, out) {
		t.Errorf("%s differs from its golden.\n--- want\n%s\n--- got\n%s", name, want, out)
	}
}

// bsStrip removes every escape sequence: the text a person reads.
func bsStrip(s string) string {
	var b strings.Builder
	w := &colorprofile.Writer{Forward: &b, Profile: colorprofile.NoTTY}
	_, _ = w.WriteString(s)
	return b.String()
}

// bsQuote makes raw ANSI reviewable: each line Go-quoted, escapes visible.
func bsQuote(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(strings.TrimPrefix(strconv.Quote(l), `"`), `"`)
	}
	return strings.Join(lines, "\n")
}

// bsScenes are the bootstrap screens the goldens hold, each reached by driving
// the flow as a person would. The reveal frame is deliberately not among them:
// a golden never holds the seed.
var bsScenes = []struct {
	name  string
	setup func(*BootstrapSetup)
	drive func(*bsDriver)
}{
	{"instance", nil, func(d *bsDriver) { d.typeText("acme") }},
	{"instance-refused", nil, func(d *bsDriver) {
		d.typeText("Acme")
		for d.f.inst.focus != fieldContinue {
			d.keys("tab")
		}
		d.keys("enter")
	}},
	{"escrow", nil, func(d *bsDriver) { d.typeText("acme"); toEscrow(d) }},
	{"escrow-none", nil, func(d *bsDriver) { d.typeText("acme"); toEscrow(d); d.keys("tab", "space") }},
	{"escrow-supplied", func(s *BootstrapSetup) { s.Escrow.Passphrase = PassphraseFromFile }, func(d *bsDriver) {
		d.typeText("acme")
		toEscrow(d)
	}},
	{"passphrase", nil, func(d *bsDriver) {
		d.typeText("acme")
		d.throughPlanAsIs()
		d.send(SecretQuestion{reply: &secretReply{ch: make(chan installflow.Response, 1)}, Question: installflow.Question{
			ID: "prompt-1", Kind: installflow.PromptSecret, Question: installflow.QuestionEscrowPassphrase,
			Params: []installflow.Param{{Name: "instance", Value: installflow.Text("acme")}}}})
		d.typeText(canaryValue("escrow-passphrase"))
	}},
	{"receipt", nil, func(d *bsDriver) { toPostRun(d) }},
	{"first-login", nil, func(d *bsDriver) { toPostRun(d); d.keys("enter") }},
	{"success", nil, func(d *bsDriver) { toPostRun(d); d.keys("space", "enter", "s", "space", "enter") }},
	{"help", nil, func(d *bsDriver) { d.typeText("acme"); toEscrow(d); d.keys("?") }},
}

func toEscrow(d *bsDriver) {
	for i := 0; i < 20 && d.f.phase == phaseInstance; i++ {
		if d.f.inst.focus == fieldContinue {
			d.keys("enter")
			continue
		}
		d.keys("tab")
	}
}

func toPostRun(d *bsDriver) {
	d.typeText("acme")
	d.throughPlanAsIs()
	d.f.setup.Reveals.Offer(installflow.RevealOffer{Step: installflow.StepBootstrapReport, Subject: installflow.RevealSuperuserSeed,
		Where: seedRef, Reveal: installflow.NewReveal([]byte(canaryValue("superuser-seed")))})
	d.send(BootstrapRunFinished{Summary: okBootstrapSummary(), Facts: acmeFacts()})
}

var bsSizes = [][2]int{{120, 40}, {100, 30}, {80, 24}, {60, 20}}

// Every bootstrap screen at every capability level and size. L1 carries no
// escape at all, and at every level the words are the same as L1's: no state
// is in colour alone.
func TestBootstrapScreenGoldens(t *testing.T) {
	for _, sc := range bsScenes {
		for _, size := range bsSizes {
			var plain string
			for _, level := range []terminal.Level{terminal.L1, terminal.L2, terminal.L3, terminal.L4} {
				s := bsSetup(bsCaps(level, size[0], size[1]))
				if sc.setup != nil {
					sc.setup(&s)
				}
				d := newBSDriver(t, s)
				sc.drive(d)
				view := d.render() + "\n"
				name := fmt.Sprintf("bootstrap/%s/L%d-%dx%d", sc.name, level, size[0], size[1])
				testkit.AssertNoCanary(t, name, []byte(view))
				text := bsStrip(view)
				bsGolden(t, name+".txt", []byte(text))
				switch level {
				case terminal.L1:
					if strings.Contains(view, "\x1b") {
						t.Errorf("%s: an escape sequence at L1", name)
					}
					plain = view
				case terminal.L2:
					bsGolden(t, name+".ansi", []byte(bsQuote(view)))
				default:
					bsGolden(t, name+".ansi", []byte(bsQuote(view)))
					if text != plain {
						t.Errorf("%s: the words differ from L1's; a state is in colour alone", name)
					}
				}
			}
		}
	}
}

// Too small a terminal is asked to grow before the run; nothing is cut.
func TestBootstrapScreensAskForSpaceBeforeTheRun(t *testing.T) {
	d := newBSDriver(t, bsSetup(bsCaps(terminal.L4, 50, 14)))
	if v := bsStrip(d.render()); !strings.Contains(v, "too small") || !strings.Contains(v, "--ui=plain") {
		t.Fatalf("minimal view:\n%s", v)
	}
	d = newBSDriver(t, bsSetup(bsCaps(terminal.L4, 100, 30)))
	toPostRun(d)
	d.send(tea.WindowSizeMsg{Width: 50, Height: 14})
	if v := bsStrip(d.render()); strings.Contains(v, "too small") || !strings.Contains(v, "Escrow receipt") {
		t.Fatalf("an after-run screen was hidden for its size:\n%s", v)
	}
}
