// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"net/textproto"

	"github.com/nats-io/nats.go"
)

// The header block nats.go frames every non-empty header map with: the version line before
// the fields and the blank line after them.
const (
	natsHeaderLine = "NATS/1.0\r\n"
	natsHeaderEnd  = "\r\n"
)

// wireSize is nm.Size() for a message built to be SENT — subject, reply, encoded header
// block and data — computed from the header map instead of by encoding it.
//
// Both writers count every publish against the stream's backpressure gate, and nm.Size()
// encodes the whole header block into a fresh buffer only to take its length: about four
// times the header encoding the publish itself does, for a number. This counts the same
// bytes the encoder would write:
//
//   - no header block at all for an empty map;
//   - NO header block either when any key is one nats.go refuses (its encoder then returns
//     an error and Size counts the headers as zero bytes; such a publish fails anyway);
//   - otherwise the version line, then "key: value\r\n" per VALUE (a key with several values
//     is written once per value; one with none is not written), then the closing blank line.
//     A value is written trimmed of leading and trailing ASCII whitespace, with any CR or LF
//     left inside it replaced one-for-one by a space, so only the trim changes its length.
//
// It is exact, not a bound, and it is held to that by tests that compare it with nm.Size()
// under the nats.go this module resolves — so a client upgrade that changes the encoding
// fails them instead of skewing the gate. It does not read the size nats.go records on a
// RECEIVED message (Size returns that one when set); nothing here sizes a received message.
func wireSize(nm *nats.Msg) int {
	return len(nm.Subject) + len(nm.Reply) + headerWireSize(nm.Header) + len(nm.Data)
}

// headerWireSize is the length of the header block nats.go would encode for h; see wireSize.
func headerWireSize(h nats.Header) int {
	if len(h) == 0 {
		return 0
	}
	n := len(natsHeaderLine) + len(natsHeaderEnd)
	for k, vs := range h {
		if !natsHeaderKeyValid(k) {
			return 0
		}
		for _, v := range vs {
			n += len(k) + len(": ") + len(textproto.TrimString(v)) + len("\r\n")
		}
	}
	return n
}

// natsHeaderKeyValid is nats.go's header key rule: non-empty printable ASCII (33-126) other
// than the separators it forbids.
func natsHeaderKeyValid(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		if !natsHeaderKeyChar[k[i]] {
			return false
		}
	}
	return true
}

var natsHeaderKeyChar = func() (set [256]bool) {
	const forbidden = "\"()/,:;<=>?@[\\]{}"
	for c := 33; c <= 126; c++ {
		set[c] = true
	}
	for i := 0; i < len(forbidden); i++ {
		set[forbidden[i]] = false
	}
	return set
}()
