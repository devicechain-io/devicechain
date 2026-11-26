// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import "sync"

// probeVT asks whether a console accepts the virtual-terminal flag vt, leaving it as it
// was found. get and set read and write the console mode.
//
// 🔴 A CONSOLE THAT CANNOT BE PUT BACK IS NOT ELIGIBLE, AND ITS MODE IS NOT FORGOTTEN. If
// the flag took but the original mode could not be restored, the console is no longer in
// the state it was found in: a terminal-UI session would save that changed mode as the
// baseline it restores to, so the session stays plain. The original mode is kept, and the
// returned restore puts it back when the command ends.
func probeVT(vt uint32, get func() (uint32, error), set func(uint32) error) (bool, func()) {
	mode, err := get()
	if err != nil {
		return false, nil
	}
	if mode&vt != 0 {
		return true, nil
	}
	if set(mode|vt) != nil {
		return false, nil
	}
	if set(mode) != nil {
		return false, func() { _ = set(mode) }
	}
	return true, nil
}

var (
	probeRestoreMu sync.Mutex
	probeRestore   func()
)

// keepProbeRestore records a console mode the probe could not put back.
func keepProbeRestore(restore func()) {
	if restore == nil {
		return
	}
	probeRestoreMu.Lock()
	defer probeRestoreMu.Unlock()
	probeRestore = restore
}

// RestoreProbedConsole puts back a console mode the probe changed and could not restore
// at the time. The command calls it as it finishes; it does nothing otherwise.
func RestoreProbedConsole() {
	probeRestoreMu.Lock()
	restore := probeRestore
	probeRestore = nil
	probeRestoreMu.Unlock()
	if restore != nil {
		restore()
	}
}
