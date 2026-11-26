// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installjson

import (
	"bufio"
	"io"
	"os"
	"time"
)

// processOutputDrain bounds how long restoring the process's streams waits for
// the redacting pipe to drain. A child that inherited the pipe (an exec plugin
// still running, say) keeps it open; the run is not held hostage to it.
const processOutputDrain = 2 * time.Second

// redirectProcessOutput points os.Stdout and os.Stderr at a pipe whose every line
// is written to the current os.Stderr through redact, and returns what puts them
// back.
//
// 🔴 THIS IS FOR WHAT dcctl DOES NOT HAND OUT ITSELF. The engine's console is
// already events. What remains is anything that reaches the process's streams
// directly — above all a kubeconfig exec credential plugin, which client-go starts
// with os.Stderr as its stderr — and in json mode all of it belongs on stderr, so
// both streams go there. Lines are redacted whole, so a value split across two
// writes of one line is still found; a final line with no newline is written when
// the pipe closes.
func redirectProcessOutput(redact func(string) string) (restore func(), err error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	out, errOut := os.Stdout, os.Stderr
	dest := errOut
	done := make(chan struct{})
	go func() {
		defer close(done)
		br := bufio.NewReader(r)
		for {
			line, rerr := br.ReadString('\n')
			if line != "" {
				_, _ = io.WriteString(dest, redact(line))
			}
			if rerr != nil {
				return
			}
		}
	}()
	os.Stdout, os.Stderr = w, w
	return func() {
		os.Stdout, os.Stderr = out, errOut
		_ = w.Close()
		select {
		case <-done:
			_ = r.Close()
		case <-time.After(processOutputDrain):
			// Left open for the child that holds it; the reader goes on writing
			// redacted lines until it exits.
		}
	}, nil
}
