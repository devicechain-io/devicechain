// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// Every text the choices screen, the drawer and a refusal draw is sanitised and
// redacted: a secret the run knows, or an escape in a value, never reaches the screen.
func TestSettingsAreSanitisedAndRedacted(t *testing.T) {
	hub, err := installflow.NewHub(installflow.Options{Operation: installflow.OperationInstall,
		Sink: installflow.SinkFunc(func(installflow.Envelope) {})})
	if err != nil {
		t.Fatal(err)
	}
	const canary = "canary-drawer-s3cr3t-9013"
	if err := hub.AddSecret(canary); err != nil {
		t.Fatal(err)
	}
	dirty := func(s string) string { return s + "\x1b]0;title\x07\x1b[31m-" + canary }
	r := &replanner{runner: func(Edits) installflow.PreflightRunner { return passing() },
		refuse: func(e Edits) error {
			if e.HA != nil {
				return errorString(dirty("--ha refused"))
			}
			return nil
		}}
	w := choicesWizard(r)
	w.Settings = []Setting{
		{Group: dirty("Other"), Flag: dirty("--odd"), Value: dirty("v"), Source: dirty("from")},
		{Group: "Monitoring and alerts", Flag: "--no-monitoring", Label: dirty("Monitoring"), Value: "on",
			Source: "default", Toggle: true, Name: "no-monitoring", Disabled: dirty("not here")},
	}
	f := preFacts()
	f.Cluster, f.FootprintFlag, f.Images = dirty("lab"), dirty("--compact"), dirty("img")
	w.Facts = f
	p := newPrerun(context.Background(), preCaps(terminal.L1, 200, 40), w, passing(), hub.Redact)
	toChoices(t, p)
	raw := p.render()
	focusOn(t, p, chTopology)
	press(p, "space")
	raw += p.render()
	if !strings.Contains(preStrip(raw), "The plan was not changed: --ha refused") {
		t.Fatalf("no refusal:\n%s", preStrip(raw))
	}
	focusOn(t, p, chAdvanced)
	press(p, "enter")
	raw += p.render()
	press(p, "down")
	raw += p.render()
	if !strings.Contains(raw, "Other") || !strings.Contains(raw, "--odd") {
		t.Fatalf("the drawer is not up:\n%s", raw)
	}
	if strings.Contains(raw, canary) || strings.Contains(raw, "\x1b]") || strings.Contains(raw, "\x07") {
		t.Fatalf("a secret or an escape reached the screen: %q", raw)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
