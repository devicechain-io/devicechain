// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"io"
	"os"
)

// programFile is the program's input over a terminal file. On Windows it has a Name, so
// Bubble Tea's console reader takes it: that reader reads console input records from the
// handle itself, which no reader of the gate's could stand in for.
func programFile(g *stdinGate, f *os.File) (io.Reader, error) {
	return namedGatedFile{gatedFile{g: g, f: f}}, nil
}

type namedGatedFile struct{ gatedFile }

func (f namedGatedFile) Name() string { return f.f.Name() }
