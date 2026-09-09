// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package msgtest

import (
	"context"
	"errors"
	"io"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// ErrPersistentRead is the error FailingReader returns. It is deliberately NOT io.EOF and
// deliberately not wrapped in anything a read loop special-cases, so a loop that keeps
// reading it is doing the one thing every consumer loop does with an unrecognised error:
// log it and read again.
var ErrPersistentRead = errors.New("msgtest: this reader always fails")

// FailingReader is a messaging.MessageReader whose every read fails with the same non-EOF
// error, counting the reads.
//
// 🔑 IT MEASURES A LOOP'S RATE, WHICH IS THE ONLY WAY THE DEFECT IT GUARDS IS VISIBLE. A
// read loop that retries an unclearable error immediately and forever is indistinguishable
// from a correct one at every assertion about OUTPUT: it produces none either way. What
// separates them is how many times it asked. Reads counts that, so a test can assert a
// bound instead of asserting an absence.
//
// It is used from the loop's own single goroutine, as messaging.MessageReader requires.
type FailingReader struct {
	// Err is returned by every read. Zero value means ErrPersistentRead.
	Err error

	// EOFAfter makes the reader return io.EOF once Reads reaches it, and 0 means never.
	//
	// 🔑 IT IS A TEST'S ESCAPE HATCH, NOT A BEHAVIOUR. A loop driven as a whole goroutine
	// rather than one iteration at a time has no place to put a counted bound, so against
	// an endlessly-failing reader an UNPACED loop would hang the run instead of failing an
	// assertion — and a hang records no number and names no defect. Setting this far above
	// any paced loop's reach means a paced loop never sees it, and an unpaced one ends at a
	// number the assertion can report.
	EOFAfter int

	// Reads counts calls to ReadMessage.
	Reads int
}

// ReadMessage counts the call and fails.
func (r *FailingReader) ReadMessage(context.Context) (messaging.Message, error) {
	r.Reads++
	if r.EOFAfter > 0 && r.Reads >= r.EOFAfter {
		return messaging.Message{}, io.EOF
	}
	if r.Err != nil {
		return messaging.Message{}, r.Err
	}
	return messaging.Message{}, ErrPersistentRead
}

// HandleResponse is the reader's own logging hook, which these tests do not need.
func (r *FailingReader) HandleResponse(error) {}
