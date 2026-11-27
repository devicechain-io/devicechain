// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package result

import (
	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
)

// HelpLines is a help overlay's body, under its title: one row for each key the screen
// answers, from the very bindings its footer draws, then what the screen does. Every
// screen of the installer draws its help with it, so help looks the same everywhere and
// at every level: the key in the accent colour where there is colour, the words alone
// where there is none.
//
// 🔴 THE ROWS ARE THE BINDINGS IT IS GIVEN, AND NOTHING ELSE. A screen passes the
// bindings its footer draws, so its help cannot list a key that does nothing, or leave
// out one that does something.
func HelpLines(t Theme, keys []key.Binding, about string, width int) []string {
	return helpLines(t.Accent, keys, about, width)
}

func helpLines(acc lipgloss.Style, keys []key.Binding, about string, width int) []string {
	// The key column is the result screen's, widened only for a key too long for it, so
	// a key and its words never run together.
	col := labelWidth
	for _, k := range keys {
		col = max(col, lipgloss.Width(k.Help().Key)+2)
	}
	out := make([]string, 0, len(keys)+4)
	for _, k := range keys {
		h := k.Help()
		out = append(out, paint(acc, padRight(h.Key, col))+h.Desc)
	}
	out = append(out, "")
	return append(out, wrap(about, width)...)
}
