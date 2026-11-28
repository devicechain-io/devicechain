// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// bsCaps is an eligible terminal at the given capability level and size.
func bsCaps(level terminal.Level, w, h int) terminal.Capabilities {
	in := terminal.Inputs{
		InputTTY: true, OutputTTY: true, ErrorTTY: true, GOOS: "linux",
		Env:   map[string]string{"TERM": "xterm-256color", "LANG": "en_US.UTF-8"},
		Width: w, Height: h, SizeKnown: true, Profile: colorprofile.TrueColor, UI: terminal.TUI,
	}
	switch level {
	case terminal.L3:
		in.Profile = colorprofile.ANSI256
	case terminal.L2:
		in.Profile = colorprofile.ANSI
	case terminal.L1:
		in.Env["NO_COLOR"] = "1"
	}
	c := terminal.Evaluate(in)
	if c.Level != level {
		panic(fmt.Sprintf("bsCaps: asked for %v, evaluated %v", level, c.Level))
	}
	return c
}

// bsKey is the key press a name stands for: a named key, or one character.
func bsKey(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "f1":
		return tea.KeyPressMsg{Code: tea.KeyF1}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	}
	r := []rune(name)
	if len(r) != 1 {
		panic("bsKey: unknown key " + name)
	}
	if r[0] == ' ' {
		return bsKey("space")
	}
	return tea.KeyPressMsg{Code: r[0], Text: name}
}

// bsDriver feeds a flow messages the way the program would: every command the
// flow returns is run, and its message fed back, except those meant for the
// parent, which are collected.
type bsDriver struct {
	t   *testing.T
	f   *BootstrapFlow
	out []tea.Msg
}

func (d *bsDriver) send(msg tea.Msg) {
	d.t.Helper()
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		_, cmd := d.f.Update(m)
		for _, next := range messages(cmd) {
			switch next.(type) {
			case BootstrapPlanned, BootstrapDone:
				d.out = append(d.out, next)
			case ScreenBack:
				d.out = append(d.out, next)
				queue = append(queue, next)
			default:
				queue = append(queue, next)
			}
		}
	}
}

func (d *bsDriver) keys(names ...string) {
	d.t.Helper()
	for _, n := range names {
		d.send(bsKey(n))
	}
}

// typeText types s one key at a time.
func (d *bsDriver) typeText(s string) {
	d.t.Helper()
	for _, r := range s {
		d.send(bsKey(string(r)))
	}
}

func (d *bsDriver) clearField() { d.keys("ctrl+u") }

func (d *bsDriver) render() string { return d.f.Render() }

// planned is the BootstrapPlanned the flow sent, or fails.
func (d *bsDriver) planned() BootstrapPlanned {
	d.t.Helper()
	for _, m := range d.out {
		if p, ok := m.(BootstrapPlanned); ok {
			return p
		}
	}
	d.t.Fatalf("no plan was sent; screen:\n%s", d.render())
	return BootstrapPlanned{}
}

func (d *bsDriver) doneSent() bool {
	for _, m := range d.out {
		if _, ok := m.(BootstrapDone); ok {
			return true
		}
	}
	return false
}

func bsSetup(caps terminal.Capabilities) BootstrapSetup {
	return BootstrapSetup{
		Caps: caps, Version: "v1.11.0", Target: "kind-devicechain", TargetArgs: []string{"--cluster=devicechain"},
		Instance: InstanceSeed{
			ClusterShape: "Standard, single node",
			ValidateName: func(s string) error {
				if strings.ToLower(s) != s || strings.ContainsAny(s, " _") {
					return errors.New("instance names are lower-case letters, digits and hyphens")
				}
				return nil
			},
			ValidateAreas: func(as []string) error {
				for _, a := range as {
					if a != "lwm2m-ingest" && a != "sparkplug-ingest" {
						return fmt.Errorf("unknown functional area %q", a)
					}
				}
				return nil
			},
		},
		Escrow: EscrowSeed{
			DefaultPath: func(name string) string { return "/home/op/.devicechain/escrow/" + name + "-rootkey.escrow" },
			ValidatePath: func(_, p string) error {
				if strings.HasSuffix(p, "exists.escrow") {
					return errors.New("refusing to overwrite the existing file " + p)
				}
				return nil
			},
		},
		Reveals: &RevealInbox{},
	}
}

func newBSDriver(t *testing.T, s BootstrapSetup) *bsDriver {
	return &bsDriver{t: t, f: NewBootstrapFlow(s)}
}

// throughPlan drives the pre-run screens for instance "acme" with defaults.
func (d *bsDriver) throughPlan() BootstrapPlanned {
	d.t.Helper()
	d.typeText("acme")
	for i := 0; i < 50 && d.f.phase == phaseInstance; i++ {
		if d.f.inst.focus == fieldContinue {
			d.keys("enter")
			break
		}
		d.keys("tab")
	}
	for i := 0; i < 50 && d.f.phase == phaseEscrow; i++ {
		if d.f.esc.focus == escrowFieldContinue {
			d.keys("enter")
			break
		}
		d.keys("tab")
	}
	return d.planned()
}
