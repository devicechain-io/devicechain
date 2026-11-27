// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import "github.com/devicechain-io/dcctl/internal/installflow"

// withheld stands for a line, or the value in it, that is not drawn as composed.
const withheld = "[withheld]"

// drawable reports whether a COMPOSED line may be drawn exactly as it is: the
// install UI's sanitiser and the hub's redaction leave it unchanged. Without a
// redactor only the sanitiser is applied.
//
// One pass decides it: when Text(redact(s)) is s, the next pass is the same
// pass over the same string, so s is already the fixed point a repeated
// sanitise-and-redact would settle on.
//
// TODO: this duplicates result.Input.line, which #3204 adds to the result
// package. Switch the progress screen to it once that lands, so there is one
// definition of a clean line.
func (m model) drawable(s string) bool {
	r := s
	if m.redact != nil {
		r = m.redact(s)
	}
	return installflow.Text(r).String() == s
}

// line is a COMPOSED line as the progress screen may draw it: itself when the
// sanitiser and the hub's redaction leave it alone, otherwise fallback (the
// same line with its value withheld) when that is left alone, otherwise the
// bare marker.
//
// 🔴 THE WHOLE LINE IS CHECKED, NOT ONLY ITS PARTS. Every value the screen
// draws has been through the hub already, but a label, its padding and a
// separator are text too: "Installing · " joined to a clean context name can be
// a registered value that neither part is. It is checked as plain text, before
// any styling, and after truncation, since what is checked is what is drawn.
func (m model) line(s, fallback string) string {
	if m.drawable(s) {
		return s
	}
	if fallback != "" && m.drawable(fallback) {
		return fallback
	}
	return withheld
}
