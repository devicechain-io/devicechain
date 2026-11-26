// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"io"
	"os"

	"golang.org/x/term"
)

// savedTerminal is the terminal's modes before the program ran: the line discipline of
// stdin and, on Windows, the console modes of both handles.
type savedTerminal []struct {
	fd    int
	state *term.State
}

func saveTerminal(streams ...any) savedTerminal {
	var s savedTerminal
	for _, v := range streams {
		f, ok := v.(*os.File)
		if !ok || !term.IsTerminal(int(f.Fd())) {
			continue
		}
		if st, err := term.GetState(int(f.Fd())); err == nil {
			s = append(s, struct {
				fd    int
				state *term.State
			}{int(f.Fd()), st})
		}
	}
	return s
}

// restore puts every saved mode back. It is safe after the program has restored them.
func (s savedTerminal) restore() {
	for i := len(s) - 1; i >= 0; i-- {
		_ = term.Restore(s[i].fd, s[i].state)
	}
}

var _ io.Writer = (*latchWriter)(nil)
