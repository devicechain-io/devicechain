// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"os"

	"golang.org/x/sys/unix"
)

// discardQueuedInput flushes the terminal's input queue (tcflush TCIFLUSH) and reports
// whether it held anything (TIOCINQ).
func discardQueuedInput(f *os.File) (bool, error) {
	fd := int(f.Fd())
	n, err := unix.IoctlGetInt(fd, unix.TIOCINQ)
	if err != nil {
		return false, err
	}
	if err := unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH); err != nil {
		return false, err
	}
	return n > 0, nil
}
