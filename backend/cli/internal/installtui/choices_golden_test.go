// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// The cluster choices, the drawer and the picker join the screens TestPrerunGoldens and
// TestEveryLevelSaysTheSameWords draw at every level and size.
func init() {
	preScenarios = append(preScenarios, choiceScenarios...)
}

// goldenChoices is the choices screen reached through a passing preflight, with a replan
// whose run passes its checks unless fail says otherwise.
func goldenChoices(c terminal.Capabilities, f Facts, fail bool) *prerun {
	r := &replanner{base: &f, runner: func(Edits) installflow.PreflightRunner {
		if fail {
			return laptopPreflight()
		}
		return healthyPreflight()
	}}
	w := choicesWizard(r)
	w.Facts = f
	p := newPrerun(context.Background(), c, w, healthyPreflight(), nil)
	press(p, "enter")
	for p.pf.action != actContinue {
		press(p, "shift+tab")
	}
	press(p, "enter")
	return p
}

func goldenContextFacts() Facts {
	f := preFacts()
	f.KubeContext, f.TargetFlag = "prod", "--kube-context prod"
	f.Compact, f.FootprintFlag, f.HA, f.TopologyFlag = true, "--compact", true, "--ha"
	return f
}

var choiceScenarios = []struct {
	name string
	make func(caps terminal.Capabilities) *prerun
}{
	{"choices-kind", func(c terminal.Capabilities) *prerun {
		return goldenChoices(c, preFacts(), false)
	}},
	{"choices-context-compact", func(c terminal.Capabilities) *prerun {
		p := goldenChoices(c, goldenContextFacts(), false)
		press(p, "tab", "tab")
		return p
	}},
	{"choices-rechecking", func(c terminal.Capabilities) *prerun {
		p := goldenChoices(c, preFacts(), false)
		press(p, "tab", "tab")
		p.update(keyOf("space")) // the checks for the changed plan start, and are still running
		return p
	}},
	{"choices-failed", func(c terminal.Capabilities) *prerun {
		p := goldenChoices(c, preFacts(), true)
		press(p, "tab", "tab", "tab", "space")
		return p
	}},
	{"choices-renaming", func(c terminal.Capabilities) *prerun {
		p := goldenChoices(c, preFacts(), false)
		press(p, "tab", "enter", "-", "l", "a", "b")
		return p
	}},
	{"drawer", func(c terminal.Capabilities) *prerun {
		p := goldenChoices(c, preFacts(), false)
		press(p, "shift+tab", "shift+tab", "shift+tab", "enter")
		return p
	}},
	{"drawer-compact", func(c terminal.Capabilities) *prerun {
		p := goldenChoices(c, preFacts(), false)
		press(p, "tab", "tab", "space")
		press(p, "tab", "tab", "tab", "enter", "down", "down", "down", "down")
		return p
	}},
	{"picker", func(c terminal.Capabilities) *prerun {
		p := goldenChoices(c, preFacts(), false)
		press(p, "space", "down")
		return p
	}},
}
