// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build race

package payloaddecode

// raceEnabled is true under the race detector, which slows everything by an order of
// magnitude and so makes the timing checks meaningless.
const raceEnabled = true
