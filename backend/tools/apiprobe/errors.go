// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
)

// codedError carries the exit code with the message, so that the verdict and the
// explanation cannot drift apart on their way up the stack.
//
// 🔴 The code is assigned where the FACT is observed, never inferred at the top
// from the text of a message. Deciding "this reads like a missing row" by
// matching an error string is how a negative control ends up asserting a class
// of failure it never actually saw.
type codedError struct {
	code int
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

func failWith(code int, format string, args ...any) error {
	return &codedError{code: code, err: fmt.Errorf(format, args...)}
}

// codeOf resolves the exit status for an error.
//
// ⚠️ It defaults to exitSetup — INCONCLUSIVE — and that default is deliberate.
// An unclassified failure is one nobody decided the meaning of, and reporting it
// as a verdict about the data would let a bug in this tool masquerade as a
// finding about the platform. Same polarity argument as the lifecycle guards:
// the default has to be the answer that claims the least.
func codeOf(err error) int {
	var ce *codedError
	if errors.As(err, &ce) {
		return ce.code
	}
	return exitSetup
}
