// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// A preflight as the doctor reports it on a laptop whose Docker is too small for a
// full-size kind cluster.
func laptopPreflight() *preRunner {
	return runnerOf(
		preSection(installplain.SectionTooling),
		preCheck(installflow.CheckPass, "kubectl v1.33.1", ""),
		preCheck(installflow.CheckPass, "tofu v1.10.6", ""),
		preCheck(installflow.CheckPass, "kind v0.29.0", ""),
		preSection(installplain.SectionDockerEngine),
		preCheck(installflow.CheckPass, "docker daemon reachable", ""),
		preCheck(installflow.CheckPass, "docker context = default (native engine)", ""),
		preCheck(installflow.CheckFail, "engine memory: 6.0Gi", capacityFix),
		preCheck(installflow.CheckWarn, "storage driver: vfs", "overlay2 is recommended for kind"),
		preSection(installplain.SectionPorts),
		preCheck(installflow.CheckPass, "port 80 free", ""),
		preCheck(installflow.CheckPass, "port 443 free", ""),
		preCheck(installflow.CheckFail, "port 1883 in use by mosquitto (pid 4121)",
			"stop what holds port 1883 and re-run: creating the kind cluster publishes it on the host and fails while it is taken"),
		preSection(installplain.SectionKubeContexts),
		preCheck(installflow.CheckInfo, "no contexts in the kubeconfig yet (expected before the first install)", ""),
	)
}

func healthyPreflight() *preRunner {
	return runnerOf(
		preSection(installplain.SectionTooling),
		preCheck(installflow.CheckPass, "kubectl v1.33.1", ""),
		preCheck(installflow.CheckPass, "tofu v1.10.6", ""),
		preSection(installplain.SectionDockerEngine),
		preCheck(installflow.CheckPass, "docker daemon reachable", ""),
		preCheck(installflow.CheckPass, "engine CPUs: 8", ""),
		preCheck(installflow.CheckPass, "engine memory: 16Gi", ""),
		preSection(installplain.SectionKubeServerVersion),
		preCheck(installflow.CheckPass, "server v1.33.1", ""),
	)
}

func warnPreflight() *preRunner {
	return runnerOf(
		preSection(installplain.SectionDockerEngine),
		preCheck(installflow.CheckPass, "docker daemon reachable", ""),
		preCheck(installflow.CheckWarn, "docker context = colima", "for the clean WSL2/Linux-native path run: docker context use default"),
		preSection(installplain.SectionKernel),
		preCheck(installflow.CheckPass, "cgroup v2", ""),
		preCheck(installflow.CheckWarn, "fs.inotify.max_user_watches = 8192",
			"sudo sysctl -w fs.inotify.max_user_watches=524288 (and persist it in /etc/sysctl.d)"),
	)
}

func verdictsPreflight() *preRunner {
	return runnerOf(
		preSection(installplain.SectionDockerEngine),
		preCheck(installflow.CheckNotNeeded, "engine memory: installing into prod from published images", ""),
		preCheck(installflow.CheckUnknown, "engine CPUs could not be read", "check that docker info answers, then Recheck"),
		preCheck(installflow.CheckInfo, "not needed — installing into prod from published images", ""),
		preSection(installplain.SectionKubeContexts),
		preCheck(installflow.CheckPass, "--kube-context prod exists", ""),
	)
}

// preScenarios are the screens the goldens draw, each built from scratch.
var preScenarios = []struct {
	name string
	make func(caps terminal.Capabilities) *prerun
}{
	{"welcome-kind", func(c terminal.Capabilities) *prerun {
		return newPrerun(context.Background(), c, &Wizard{Facts: preFacts()}, nil, nil)
	}},
	{"welcome-context", func(c terminal.Capabilities) *prerun {
		f := preFacts()
		f.KubeContext, f.Cluster, f.Compact, f.FootprintFlag, f.HA, f.TopologyFlag = "prod", "", true, "--compact", true, "--ha"
		f.Images = "built from this source checkout, pushed to localhost:5000 as dev"
		p := newPrerun(context.Background(), c, &Wizard{Facts: f}, nil, nil)
		press(p, "tab")
		return p
	}},
	{"preflight-running", func(c terminal.Capabilities) *prerun {
		p := newPrerun(context.Background(), c, &Wizard{Facts: preFacts()}, nil, nil)
		p.screen, p.pf.started, p.pf.running, p.pf.gen = screenPreflight, true, true, 1
		for _, o := range laptopPreflight().obs[:6] {
			p.update(checkMsg{gen: 1, obs: &o})
		}
		return p
	}},
	{"preflight-pass", func(c terminal.Capabilities) *prerun {
		p := newPrerun(context.Background(), c, &Wizard{Facts: preFacts(), Replan: noReplan}, healthyPreflight(), nil)
		press(p, "enter", "shift+tab")
		return p
	}},
	{"preflight-warn", func(c terminal.Capabilities) *prerun {
		p := newPrerun(context.Background(), c, &Wizard{Facts: preFacts(), Replan: noReplan}, warnPreflight(), nil)
		press(p, "enter", "down")
		return p
	}},
	{"preflight-fail", func(c terminal.Capabilities) *prerun {
		p := newPrerun(context.Background(), c, &Wizard{Facts: preFacts(), Replan: noReplan}, laptopPreflight(), nil)
		press(p, "enter")
		return p
	}},
	{"preflight-verdicts", func(c terminal.Capabilities) *prerun {
		f := preFacts()
		f.KubeContext, f.Cluster = "prod", ""
		p := newPrerun(context.Background(), c, &Wizard{Facts: f}, verdictsPreflight(), nil)
		press(p, "enter")
		return p
	}},
	{"preflight-skipped", func(c terminal.Capabilities) *prerun {
		f := preFacts()
		f.SkipPreflight = true
		p := newPrerun(context.Background(), c, &Wizard{Facts: f, Replan: noReplan}, nil, nil)
		press(p, "enter")
		return p
	}},
	{"preflight-switch", func(c terminal.Capabilities) *prerun {
		p := newPrerun(context.Background(), c, &Wizard{Facts: preFacts(), Replan: noReplan}, laptopPreflight(), nil)
		press(p, "enter", "tab", "enter")
		return p
	}},
}

func noReplan(bool) (installflow.Engine, installflow.PreflightRunner, error) {
	return nil, nil, fmt.Errorf("not in a golden")
}

var preLevels = []struct {
	name  string
	level terminal.Level
}{{"L4", terminal.L4}, {"L3", terminal.L3}, {"L2", terminal.L2}, {"L1", terminal.L1}}

// The last is the smallest size the screens accept.
var preSizes = [][2]int{{120, 40}, {100, 30}, {80, 24}, {60, 20}, {prerunMinWidth, prerunMinHeight}}

// TestPrerunGoldens draws the welcome and preflight screens at every capability level
// and every size, as escape-stripped text and as raw ANSI. Update with -update, locally,
// never in CI.
func TestPrerunGoldens(t *testing.T) {
	for _, sc := range preScenarios {
		for _, lv := range preLevels {
			for _, sz := range preSizes {
				name := fmt.Sprintf("%s/%s-%dx%d", sc.name, lv.name, sz[0], sz[1])
				t.Run(name, func(t *testing.T) {
					p := sc.make(preCaps(lv.level, sz[0], sz[1]))
					view := p.render()
					preFits(t, view, sz[0], sz[1])
					text := preStrip(view)
					if lv.level == terminal.L1 && text != view {
						t.Errorf("%s drew escape sequences", lv.name)
					}
					if lv.level == terminal.L2 && strings.ContainsAny(text, "╭╮╰╯│─›·…↑↓") {
						t.Errorf("%s drew a glyph outside ASCII:\n%s", lv.name, text)
					}
					requireGolden(t, "prerun/"+name+".txt", []byte(text+"\n"))
					requireGolden(t, "prerun/"+name+".ansi", []byte(quoteLines(view)+"\n"))
				})
			}
		}
	}
}

// preFits requires the screen to fill the terminal exactly: no line wider than it, and
// exactly its height in lines.
func preFits(t *testing.T, view string, w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != h {
		t.Errorf("%d lines on a %d-row terminal", len(lines), h)
	}
	for i, l := range lines {
		if lw := cellWidth(l); lw > w {
			t.Errorf("line %d is %d cells on a %d-column terminal: %q", i, lw, w, preStrip(l))
		}
	}
}

// Every state reads the same in every level's text: the words carry it, never colour.
func TestEveryLevelSaysTheSameWords(t *testing.T) {
	for _, sc := range preScenarios {
		var want []string
		for _, lv := range preLevels {
			view := preStrip(sc.make(preCaps(lv.level, 120, 40)).render())
			var words []string
			for _, w := range []string{"PASS", "WARN", "FAIL", "UNKNOWN", "NOT NEEDED", "NOT CHECKED", "INFO",
				"(unavailable)", "fix:"} {
				words = append(words, fmt.Sprintf("%s=%d", w, strings.Count(view, w)))
			}
			if want == nil {
				want = words
			} else if strings.Join(words, " ") != strings.Join(want, " ") {
				t.Errorf("%s at %s: %v, L4 %v", sc.name, lv.name, words, want)
			}
		}
	}
}
