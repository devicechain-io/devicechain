// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// PointerError is a decode failure that names WHERE in the posted document the problem is, as an
// RFC 6901 JSON Pointer ("/payload/entries/0/measurements"). Its message is the wrapped error's
// own, unchanged, so every place that logs or compares the text sees what it always did; the
// pointer is for the one caller that can hand it back to the sender, the HTTP route's 422.
type PointerError struct {
	// Pointer addresses the offending value in the request body. The empty string is the whole
	// document, which is what RFC 6901 says it is.
	Pointer string
	Err     error
}

func (e *PointerError) Error() string { return e.Err.Error() }
func (e *PointerError) Unwrap() error { return e.Err }

// atPointer wraps err so it carries pointer. A nil err stays nil, and an error that already
// names a pointer keeps the more specific one it has.
func atPointer(pointer string, err error) error {
	if err == nil {
		return nil
	}
	var pe *PointerError
	if errors.As(err, &pe) {
		return err
	}
	return &PointerError{Pointer: pointer, Err: err}
}

// entryPointer is the pointer to a field of the i-th entry of the payload.
func entryPointer(i int, field string) string {
	p := "/payload/entries/" + strconv.Itoa(i)
	if field != "" {
		p += "/" + escapePointerToken(field)
	}
	return p
}

// escapePointerToken escapes one reference token as RFC 6901 section 3 requires.
func escapePointerToken(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	return strings.ReplaceAll(s, "/", "~1")
}

// IsMalformedJSON reports whether err says the body is not JSON at all (a syntax error or a
// truncated document). That is a 400: nothing in it can be pointed at. Everything else a decoder
// refuses is a well-formed document with something wrong in it, which is a 422 with a pointer.
func IsMalformedJSON(err error) bool {
	var syn *json.SyntaxError
	return errors.As(err, &syn)
}

// DecodeProblem is the pointer and message a 422 carries for a decode failure.
type DecodeProblem struct {
	Pointer string `json:"pointer"`
	Detail  string `json:"detail"`
}

// ProblemOf describes a decode failure for the sender. A failure that names no place in the
// document is addressed to the whole document (the empty pointer) rather than guessed at.
func ProblemOf(err error) DecodeProblem {
	var pe *PointerError
	if errors.As(err, &pe) {
		return DecodeProblem{Pointer: pe.Pointer, Detail: err.Error()}
	}
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) && ute.Field != "" {
		// encoding/json reports the path of struct fields and leaves the array indices out, so a
		// path through the entries array cannot be turned into a pointer that resolves. It is cut
		// at the array: the pointer names the nearest place that does exist.
		field := ute.Field
		if i := strings.Index(field, "entries."); i >= 0 {
			field = field[:i+len("entries")]
		}
		parts := strings.Split(field, ".")
		for i := range parts {
			parts[i] = escapePointerToken(parts[i])
		}
		return DecodeProblem{Pointer: "/" + strings.Join(parts, "/"), Detail: err.Error()}
	}
	return DecodeProblem{Pointer: "", Detail: err.Error()}
}

// payloadErrorPointer is where a failure to read the payload OBJECT sits in the posted document.
// encoding/json names a field relative to the object it was handed, which here is the payload, so
// it cannot be turned into a pointer on its own: a wrong type inside the entries is addressed to
// the entries array (the nearest place that resolves) and anything else to the payload.
func payloadErrorPointer(err error) string {
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) && strings.HasPrefix(ute.Field, "entries") {
		return "/payload/entries"
	}
	return "/payload"
}
