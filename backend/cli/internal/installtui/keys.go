// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import "charm.land/bubbles/v2/key"

// keyMap is every key the progress screen answers. The footer is generated from the
// bindings active at the moment, so it cannot advertise a key that does nothing.
type keyMap struct {
	answer   key.Binding
	logs     key.Binding
	pageUp   key.Binding
	pageDown key.Binding
	follow   key.Binding
	exit     key.Binding
	stop     key.Binding
	force    key.Binding
	// help opens help; closeHelp and helpScroll are help's own keys (see help.go).
	help       key.Binding
	closeHelp  key.Binding
	helpScroll key.Binding
}

// newKeyMap is the progress screen's keys; updown names the arrow keys at the level.
func newKeyMap(updown string) keyMap {
	return keyMap{
		answer:     key.NewBinding(key.WithKeys("y", "Y", "n", "N"), key.WithHelp("y/n", "answer")),
		logs:       key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "logs")),
		pageUp:     key.NewBinding(key.WithKeys("pgup"), key.WithHelp("PgUp/PgDn", "scroll")),
		pageDown:   key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("PgDn", "scroll")),
		follow:     key.NewBinding(key.WithKeys("f", "end"), key.WithHelp("f", "follow")),
		exit:       key.NewBinding(key.WithKeys("enter", "q", "esc"), key.WithHelp("Enter", "exit")),
		stop:       key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("Ctrl+C", "stop")),
		force:      key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("Ctrl+C again", "force exit")),
		help:       key.NewBinding(key.WithKeys("?", "f1"), key.WithHelp("?/F1", "help")),
		closeHelp:  key.NewBinding(key.WithKeys("esc", "?", "f1"), key.WithHelp("Esc", "close help")),
		helpScroll: key.NewBinding(key.WithKeys(helpScrollKeys...), key.WithHelp(updown, "scroll")),
	}
}

// active is the bindings to advertise now, in footer order: the screen's, or help's
// while help is open over it.
func (k keyMap) active(m model) []key.Binding {
	if m.help.open {
		out := []key.Binding{k.closeHelp}
		if _, list, room := m.helpParts(newFrame(m.width, m.height)); len(list) > room {
			out = append(out, k.helpScroll)
		}
		return append(out, k.cancelKeys(m)...)
	}
	return k.screen(m)
}

// screen is the progress screen's footer with help closed. Its help lists exactly these.
func (k keyMap) screen(m model) []key.Binding {
	var out []key.Binding
	if m.prompt != nil {
		out = append(out, k.answer)
	}
	if m.logs != nil {
		out = append(out, k.logs)
		if m.pane.open {
			out = append(out, k.pageUp)
			if !m.pane.follow {
				out = append(out, k.follow)
			}
		}
	}
	if m.summary != nil && !m.caps.ProgressOnly {
		out = append(out, k.exit)
	}
	out = append(out, k.cancelKeys(m)...)
	// Help comes last: where the footer is too narrow for every key, it is the one left
	// out, never the way to stop.
	return append(out, k.help)
}

// cancelKeys is Ctrl+C as it stands: stop the run, or force the exit once it is stopping.
// Once the run is over it is not offered.
func (k keyMap) cancelKeys(m model) []key.Binding {
	switch {
	case m.summary != nil:
		return nil
	case m.stopping:
		return []key.Binding{k.force}
	}
	return []key.Binding{k.stop}
}
