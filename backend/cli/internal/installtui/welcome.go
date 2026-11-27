// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// The welcome screen's words. They state the scope of the command and nothing it does
// not do: install prepares a cluster, and an instance is a separate command.
const (
	welcomeTitle     = "Welcome"
	welcomeScope     = "This installs shared cluster prerequisites. Create an instance later with dcctl bootstrap."
	welcomeNoChanges = "No changes have been made."
)

// The welcome screen's actions, in order.
const (
	welcomeContinue = iota
	welcomeExit
)

func (p *prerun) welcomeKey(k tea.KeyPressMsg) (tea.Cmd, prerunOutcome) {
	switch {
	case key.Matches(k, p.keys.next, p.keys.prev):
		p.welcome = 1 - p.welcome
	case key.Matches(k, p.keys.exit):
		p.welcome = welcomeExit
	case key.Matches(k, p.keys.enter):
		if p.welcome == welcomeExit {
			return nil, leave
		}
		p.screen = screenPreflight
		if !p.pf.started {
			return p.startChecks(), stay
		}
	}
	return nil, stay
}

// origin says where a choice came from, after its value: the flag that set it, or that
// it was changed on these screens. A default says nothing.
func (p *prerun) origin(flag string, changed bool) string {
	switch {
	case changed:
		return " (" + changedHere + ")"
	case flag != "":
		return " (from " + p.text(flag) + ")"
	}
	return ""
}

// changedHere marks a choice made on these screens rather than on the command line.
const changedHere = "changed in this installer"

func topologyWord(ha bool) string {
	if ha {
		return "HA"
	}
	return "Single"
}

// footprintWord is the footprint as the screens name it.
func footprintWord(compact bool) string {
	if compact {
		return "Compact"
	}
	return "Standard"
}

func (p *prerun) welcomeFacts() [][2]string {
	f := p.facts
	var rows [][2]string
	if f.Provider != "" {
		v := p.text(f.Provider)
		var others []string
		for _, n := range f.Providers {
			if n != f.Provider {
				others = append(others, p.text(n))
			}
		}
		if len(others) > 0 {
			v += " (also available: " + strings.Join(others, ", ") + ")"
		}
		rows = append(rows, [2]string{"Provider", v})
	}
	switch {
	case f.KubeContext != "":
		rows = append(rows, [2]string{"Target", "existing cluster, context " + p.text(f.KubeContext) +
			" (dcctl never creates or deletes it)"})
	case f.Cluster != "":
		rows = append(rows, [2]string{"Target", "local kind cluster " + p.text(f.Cluster) + ", created if it does not exist"})
	}
	rows = append(rows, [2]string{"Footprint", footprintWord(f.Compact) + p.origin(f.FootprintFlag, p.edits.Compact != nil)})
	rows = append(rows, [2]string{"Topology", topologyWord(f.HA) + p.origin(f.TopologyFlag, p.edits.HA != nil)})
	if f.Images != "" {
		rows = append(rows, [2]string{"Images", p.text(f.Images)})
	}
	if f.SkipPreflight {
		rows = append(rows, [2]string{"Preflight", "not checked (--skip-preflight supplied)"})
	} else {
		rows = append(rows, [2]string{"Preflight", "read-only checks run next"})
	}
	return rows
}

func (p *prerun) welcomeLines(w, rows int) []string {
	t := p.theme
	head := []string{t.Bold.Render(welcomeTitle)}
	head = append(head, wrap(welcomeScope, w)...)
	head = append(head, "")
	var facts []string
	for _, r := range p.welcomeFacts() {
		lead := "  " + t.Dim.Render(padCells(r[0], 11)) + " "
		facts = append(facts, hang(lead, r[1], w)...)
	}
	tail := []string{""}
	tail = append(tail, wrap(welcomeNoChanges, w)...)
	tail = append(tail, "")
	tail = append(tail, p.actionBar([]string{"Continue", "Exit"}, []bool{true, true}, p.welcome, w)...)
	// The facts give way to the actions: they scroll, counted, never the other way.
	return stack(head, facts, tail, rows, -1, -1, t.Glyphs.Ellipsis)
}
