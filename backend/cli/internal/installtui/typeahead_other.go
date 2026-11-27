// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin && !windows

package installtui

import (
	"errors"
	"os"
)

// discardQueuedInput cannot flush a terminal's input queue on this platform, so a secret
// question after a fallback is not asked: queued input could become its answer.
func discardQueuedInput(*os.File) (bool, error) {
	return false, errors.New("flushing the terminal's input queue is not supported on this platform")
}
