// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"os"

	"golang.org/x/sys/windows"
)

// discardQueuedInput flushes the console's input buffer and reports whether it held any
// events. A console the program may still be reading is refused before this is reached
// (see stdinGate.refuse); this is the same discipline for any other console.
func discardQueuedInput(f *os.File) (bool, error) {
	h := windows.Handle(f.Fd())
	var n uint32
	if err := windows.GetNumberOfConsoleInputEvents(h, &n); err != nil {
		return false, err
	}
	if err := windows.FlushConsoleInputBuffer(h); err != nil {
		return false, err
	}
	return n > 0, nil
}
