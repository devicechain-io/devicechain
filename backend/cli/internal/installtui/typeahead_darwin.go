// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"os"

	"golang.org/x/sys/unix"
)

const (
	// fionread is FIONREAD, _IOR('f', 127, int), which golang.org/x/sys/unix does not
	// export for darwin.
	fionread = 0x4004667f
	// fread is FREAD, TIOCFLUSH's argument for the input queue.
	fread = 1
)

// discardQueuedInput flushes the terminal's input queue (TIOCFLUSH FREAD, which is what
// tcflush TCIFLUSH does) and reports whether it held anything (FIONREAD).
func discardQueuedInput(f *os.File) (bool, error) {
	fd := int(f.Fd())
	n, err := unix.IoctlGetInt(fd, fionread)
	if err != nil {
		return false, err
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, fread); err != nil {
		return false, err
	}
	return n > 0, nil
}
