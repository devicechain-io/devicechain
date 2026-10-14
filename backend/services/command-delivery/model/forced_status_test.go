// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// forcedStatus is the column map a test uses to place a row in a status no public transition
// reaches. A terminal status carries an outcome like any real terminal write does (the write
// guard refuses one without), so a placed SUCCESSFUL row is SUCCEEDED, and a placed FAILED row is
// UNKNOWN — the conservative class, which is what a test that does not care should get.
func forcedStatus(status string) map[string]any {
	st := CommandStatus(status)
	if !st.Terminal() {
		return map[string]any{"status": status}
	}
	outcome, ok := intrinsicOutcome(st)
	if !ok {
		outcome = OutcomeUnknown
	}
	return terminalUpdate(st, outcome, nil)
}
