// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// secretHub is a test hub with values registered for redaction, as the install
// registers credentials.
func secretHub(t *testing.T, secrets ...string) *installflow.Hub {
	t.Helper()
	h := testHub(t, 0)
	for _, s := range secrets {
		if err := h.AddSecret(s); err != nil {
			t.Fatalf("AddSecret(%q): %v", s, err)
		}
	}
	return h
}

// 🔴 MUTANT "parts only": every line the progress screen composes from a caller's
// value is checked WHOLE. Each value below is clean on its own and is carried by the
// hub unchanged; only the label, padding or separator joined to it completes a
// registered secret. The line must then be drawn with its value withheld, never as
// composed and never with the secret's halves on screen.
func TestComposedLinesCannotRebuildASecret(t *testing.T) {
	started := func(ctx string) installflow.Envelope {
		return env(at(0), "", 0, installflow.RunStarted{Target: installflow.Target{Mode: "existing-context",
			Context: installflow.Text(ctx)}, Manifest: manifestAll().Manifest})
	}
	cases := []struct {
		name   string
		secret string
		w, h   int
		envs   func() []installflow.Envelope
		logs   func(*installflow.Hub)
		prompt *installflow.Question
		leak   string // the caller's value, which must not be drawn
		want   string // the line as drawn instead
	}{
		{name: "bordered title", secret: "Installing - rnd3Hdr7xQ", w: 120, h: 40,
			envs: func() []installflow.Envelope { return []installflow.Envelope{started("rnd3Hdr7xQ")} },
			leak: "rnd3Hdr7xQ", want: "DeviceChain - Installing - [withheld]"},
		{name: "borderless header", secret: "Installing - rnd3Cmp7xQ", w: 70, h: 20,
			envs: func() []installflow.Envelope { return []installflow.Envelope{started("rnd3Cmp7xQ")} },
			leak: "rnd3Cmp7xQ", want: "DeviceChain - Installing - [withheld]"},
		{name: "now line", secret: "Now  rnd3Stage7xQ", w: 120, h: 40,
			envs: func() []installflow.Envelope {
				return []installflow.Envelope{started("kind-devicechain"),
					env(at(time.Second), "rnd3Stage7xQ", 1, installflow.StepStarted{})}
			},
			leak: "rnd3Stage7xQ", want: "Now  [withheld]"},
		{name: "question", secret: `cluster "rnd3Ask7xQ" found`, w: 120, h: 40,
			envs: func() []installflow.Envelope { return []installflow.Envelope{started("kind-devicechain")} },
			prompt: &installflow.Question{ID: "q1", Question: installflow.QuestionCreateKindCluster,
				Params: []installflow.Param{{Name: "cluster", Value: installflow.Text("rnd3Ask7xQ")}}},
			leak: "rnd3Ask7xQ", want: "No kind cluster [withheld] found. Create it now? [y/N]"},
		{name: "log row", secret: "tofu    rnd3Log7xQ", w: 120, h: 40,
			envs: func() []installflow.Envelope { return []installflow.Envelope{started("kind-devicechain")} },
			logs: func(h *installflow.Hub) {
				w, _ := h.LogWriter(installflow.StepInstallPrerequisites, "tofu", installflow.LogInfo)
				_, _ = w.Write([]byte("rnd3Log7xQ\n"))
			},
			leak: "rnd3Log7xQ", want: "tofu    [withheld]"},
		{name: "stage row label", secret: "3/9 rnd3Unit7xQ", w: 120, h: 40,
			envs: func() []installflow.Envelope {
				return []installflow.Envelope{started("kind-devicechain"),
					env(at(time.Second), installflow.StepInstallRDBReady, 1, installflow.StepStarted{}),
					env(at(2*time.Second), installflow.StepInstallRDBReady, 1, installflow.StepProgress{
						Mode: installflow.ProgressMeasured, Current: 3, Total: total(9), Unit: "rnd3Unit7xQ"})}
			},
			leak: "3/9 rnd3Unit7xQ", want: "RUN   Relational store ready"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := secretHub(t, c.secret)
			if c.logs != nil {
				c.logs(h)
			}
			m := newTestModel(levelCaps(terminal.L2, c.w, c.h), h, at(3*time.Second))
			for _, e := range c.envs() {
				next, _ := m.Update(eventMsg{env: e})
				m = next.(model)
			}
			if c.prompt != nil {
				next, _ := m.Update(promptMsg{q: *c.prompt, reply: make(chan installflow.Response, 1)})
				m = next.(model)
			}
			view := stripANSI(m.render())
			if strings.Contains(view, c.leak) {
				t.Fatalf("the line was drawn as composed, %q on screen:\n%s", c.leak, view)
			}
			if !strings.Contains(view, c.want) {
				t.Fatalf("want the line drawn with its value withheld, %q, in:\n%s", c.want, view)
			}
		})
	}
}

// The counterweight: a line no secret touches is drawn exactly as composed, and a
// fallback that is itself unsafe is never drawn, only the bare marker.
func TestALineFallsBackOnlyToACleanLine(t *testing.T) {
	m := newTestModel(levelCaps(terminal.L2, 120, 40), secretHub(t, "rnd3Both7xQ"), t0)
	if got := m.line("Installing - kind-devicechain", "x"); got != "Installing - kind-devicechain" {
		t.Fatalf("a clean line was changed: %q", got)
	}
	if got := m.line("Label rnd3Both7xQ", "Label [withheld]"); got != "Label [withheld]" {
		t.Fatalf("want the clean fallback, got %q", got)
	}
	if got := m.line("rnd3Both7xQ one", "rnd3Both7xQ two"); got != withheld {
		t.Fatalf("an unsafe fallback was drawn: %q", got)
	}
	// What the sanitiser would change is not drawn as composed either.
	if got := m.line("Label \x1b[31mred", "Label [withheld]"); got != "Label [withheld]" {
		t.Fatalf("a line the sanitiser changes was drawn: %q", got)
	}
}
