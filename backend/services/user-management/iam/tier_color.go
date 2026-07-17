// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

// The tier color palette (ADR-065 S5c). A tier's Color is a TOKEN into this closed,
// named vocabulary — never a raw hex value — for the same reasons the governance
// dimensions are a registry rather than free-form keys (ADR-065 decision 8):
//
//   - Contrast is guaranteed. Each entry is a semantic name the CONSOLE maps to a
//     theme-aware swatch, so a pill is legible in both light and dark themes. A raw hex
//     an operator typed carries no such guarantee — a dark value on the dark theme is
//     invisible, and picking a readable foreground from an arbitrary background needs a
//     luminance calculation nobody should own.
//   - The vocabulary is inspectable and validated. A write is checked against this list
//     (ValidTierColor), so a typo is rejected at the API rather than stored as a color
//     that renders as nothing. TierColors drives the console's picker, so the two cannot
//     drift.
//
// The server holds only the NAMES. It deliberately does not know the hex values — those
// are a console concern (a pill is only ever rendered there), and keeping them out of
// user-management means a palette restyle is a frontend change, not a schema one. This
// is the same split as an enum whose display strings live in the UI.
//
// The empty string is a valid, non-listed value meaning "no color chosen": a tier with
// no pill. It is the default (see the S5c migration), so ValidTierColor admits it.

// TierColor is a palette token. A distinct string type so a plain tier token or a hex
// value cannot be passed where a palette name is expected.
type TierColor string

const (
	TierColorSlate   TierColor = "slate"
	TierColorGray    TierColor = "gray"
	TierColorRed     TierColor = "red"
	TierColorOrange  TierColor = "orange"
	TierColorAmber   TierColor = "amber"
	TierColorGreen   TierColor = "green"
	TierColorTeal    TierColor = "teal"
	TierColorSky     TierColor = "sky"
	TierColorBlue    TierColor = "blue"
	TierColorViolet  TierColor = "violet"
	TierColorFuchsia TierColor = "fuchsia"
	TierColorRose    TierColor = "rose"
)

// tierColorOrder is the palette in the order a picker should present it — roughly the
// spectrum, so the swatch list reads as a gradient rather than an arbitrary jumble.
var tierColorOrder = []TierColor{
	TierColorSlate, TierColorGray, TierColorRed, TierColorOrange, TierColorAmber,
	TierColorGreen, TierColorTeal, TierColorSky, TierColorBlue, TierColorViolet,
	TierColorFuchsia, TierColorRose,
}

var tierColorSet = func() map[TierColor]struct{} {
	m := make(map[TierColor]struct{}, len(tierColorOrder))
	for _, c := range tierColorOrder {
		m[c] = struct{}{}
	}
	return m
}()

// TierColors returns the palette in presentation order, as plain strings for the API
// and the console picker.
func TierColors() []string {
	out := make([]string, len(tierColorOrder))
	for i, c := range tierColorOrder {
		out[i] = string(c)
	}
	return out
}

// ValidTierColor reports whether s is a palette token or the empty "no color" value.
// The write path rejects everything else, so an unknown color can never be stored.
func ValidTierColor(s string) bool {
	if s == "" {
		return true
	}
	_, ok := tierColorSet[TierColor(s)]
	return ok
}
