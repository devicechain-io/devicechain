// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package installtui is the terminal-UI presenter of a dcctl install, chosen with
// --ui=tui (or --ui=auto) on a terminal that can carry it. It and its subpackages are
// the only packages that import the Charm libraries.
//
// It renders the same event stream the plain presenter does, through one Bubble Tea
// program: the hub's events, the prompt broker's questions and the engine's own words
// (into the hub's log ring) all reach the screen as messages through one bridge, and the
// program is the single reader of stdin while it runs. The screens here are minimal; the
// wizard, preflight and progress screens replace the model without changing the bridge.
package installtui
