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
}

func newKeyMap() keyMap {
	return keyMap{
		answer:   key.NewBinding(key.WithKeys("y", "Y", "n", "N"), key.WithHelp("y/n", "answer")),
		logs:     key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "logs")),
		pageUp:   key.NewBinding(key.WithKeys("pgup"), key.WithHelp("PgUp/PgDn", "scroll")),
		pageDown: key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("PgDn", "scroll")),
		follow:   key.NewBinding(key.WithKeys("f", "end"), key.WithHelp("f", "follow")),
		exit:     key.NewBinding(key.WithKeys("enter", "q", "esc"), key.WithHelp("Enter", "exit")),
		stop:     key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("Ctrl+C", "stop")),
		force:    key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("Ctrl+C again", "force exit")),
	}
}

// active is the bindings to advertise now, in footer order.
func (k keyMap) active(m model) []key.Binding {
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
	switch {
	case m.summary != nil && !m.caps.ProgressOnly:
		out = append(out, k.exit)
	case m.summary != nil:
	case m.stopping:
		out = append(out, k.force)
	default:
		out = append(out, k.stop)
	}
	return out
}
