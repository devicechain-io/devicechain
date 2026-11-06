// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// The document is stored in a Postgres jsonb column, and jsonb does not hand back the bytes it was
// given: it re-renders every number without an exponent (1e100000 comes back as a 100,001-digit
// literal) and puts a space after every ',' and ':'. A cap measured on the bytes the client sent is
// therefore not a cap on what a read returns. CanonicalSize is an UPPER BOUND on the rendered
// length, so a document under MaxDocumentBytes by this measure is under it as stored, and the cap
// means the same thing on save, on read and on publish.

// maxExponentDigits keeps an exponent parseable; an exponent this long is refused outright, since
// its rendering is far over the cap on its own.
const maxExponentDigits = 7

type canonFrame struct {
	obj    bool
	n      int
	wantKV bool // object only: the next token is a key
}

// CanonicalSize is the upper bound, in bytes, of the jsonb rendering of raw. It fails closed on a
// document that does not parse, nests past MaxDocumentDepth, or holds a number whose rendering
// alone is over MaxDocumentBytes.
func CanonicalSize(raw []byte) (int, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var stack []canonFrame
	size := 0
	seenRoot := false
	// value is called as each value (scalar or container) begins.
	value := func() {
		if len(stack) == 0 {
			if seenRoot {
				size = MaxDocumentBytes + 1 // a second root value is not one document
			}
			seenRoot = true
			return
		}
		top := &stack[len(stack)-1]
		if !top.obj {
			if top.n > 0 {
				size += 2 // ", "
			}
			top.n++
		} else {
			top.wantKV = true
		}
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			if len(stack) != 0 || !seenRoot {
				return 0, errors.New("the document is incomplete")
			}
			return size, nil
		}
		if err != nil {
			return 0, err
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				value()
				stack = append(stack, canonFrame{obj: t == '{', wantKV: t == '{'})
				size++
			default:
				stack = stack[:len(stack)-1]
				size++
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].obj && stack[n-1].wantKV {
				top := &stack[n-1]
				if top.n > 0 {
					size += 2 // ", "
				}
				top.n++
				top.wantKV = false
				size += 2 + stringSize(t) // ": "
			} else {
				value()
				size += stringSize(t)
			}
		case json.Number:
			value()
			n, err := numberSize(string(t))
			if err != nil {
				return 0, err
			}
			size += n
		case bool:
			value()
			if t {
				size += 4
			} else {
				size += 5
			}
		case nil:
			value()
			size += 4
		}
		if size > MaxDocumentBytes {
			return size, nil
		}
	}
}

// stringSize is the rendered length of s with its quotes, counting every control character and
// quote as the escape jsonb writes for it, and a multi-byte rune as its UTF-8 bytes.
func stringSize(s string) int {
	n := 2
	for _, r := range s {
		switch {
		case r == '"' || r == 0x5c || r == '\n' || r == '\r' || r == '\t' || r == '\b' || r == '\f':
			n += 2
		case r < 0x20:
			n += 6
		case r < 0x80:
			n++
		case r < 0x800:
			n += 2
		case r < 0x10000:
			n += 3
		default:
			n += 4
		}
	}
	return n
}

// numberSize is the rendered length of a JSON number literal. Without an exponent jsonb keeps the
// digits as written, so it is the literal's length. With one, the digits are spread over |exp|
// more places (zero padding, or the leading "0." and zeros for a negative exponent).
func numberSize(tok string) (int, error) {
	i := strings.IndexAny(tok, "eE")
	if i < 0 {
		return len(tok), nil
	}
	exp := strings.TrimLeft(tok[i+1:], "+-")
	exp = strings.TrimLeft(exp, "0")
	if len(exp) > maxExponentDigits {
		return 0, errors.New("a number's exponent is too large to store")
	}
	e := 0
	for _, c := range exp {
		e = e*10 + int(c-'0')
	}
	return i + e + 2, nil
}
