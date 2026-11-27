// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package installtui

import (
	"io"
	"os"
)

// programFile is the program's input over a terminal file: a file with a descriptor but no
// name, so Bubble Tea puts it in raw mode yet does not build a cancelable reader over it
// (cancelreader takes only a file that has a Name). Its reads wait on the terminal through
// the gate's own cancelable reader, which the gate cancels and closes (stopProgram).
func programFile(g *stdinGate, f *os.File) (io.Reader, error) {
	if err := g.startCancelable(f); err != nil {
		return nil, err
	}
	return gatedFile{g: g, f: f}, nil
}
