// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Canon returns the canonical form of a JSON document: object keys sorted, no insignificant
// whitespace, numbers kept as written, HTML characters unescaped. An empty input and the JSON
// literal null both mean "no document" and come back nil.
//
// Everything that is compared or written goes through here, so a document the server hands
// back reformatted compares equal to the one that was sent, and an export is byte-stable.
func Canon(in []byte) (Doc, error) {
	in = bytes.TrimSpace(in)
	if len(in) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("not valid JSON: trailing data after the document")
	}
	if v == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return Doc(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// canonText canonicalizes a document the API returned as text. A nil pointer and an empty
// string both mean "none".
func canonText(s *string) (Doc, error) {
	if s == nil {
		return nil, nil
	}
	return Canon([]byte(*s))
}

// text is the form a Doc takes on the wire: the canonical text, or nil for "none" (which a
// partial update sends as an explicit null, clearing the field).
func text(d Doc) any {
	if len(d) == 0 {
		return nil
	}
	return string(d)
}

// str is the wire form of an optional string: nil for empty, so an update clears it.
func str(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func fptr(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func iptr(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// deref turns an optional API string into the bundle's empty-means-absent string.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// eqDoc reports whether two canonical documents are equal.
func eqDoc(a, b Doc) bool { return bytes.Equal(a, b) }

// envName is the environment variable a connector credential handle defaults to on export:
// DC_CONNECTOR_<TOKEN> with every character outside A-Z0-9 replaced by an underscore.
func envName(token string) string {
	var b strings.Builder
	b.WriteString("DC_CONNECTOR_")
	for _, r := range strings.ToUpper(token) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
