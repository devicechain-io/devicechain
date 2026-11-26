// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofujson

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// oversizedDiagnostic reads string fields without ever buffering a whole token.
// Keys and values may arrive in any order. Only the UI's diagnostic text fields
// are searched: a webhook mentioned in a resource value must not trigger retries.
type oversizedDiagnostic struct {
	stack   [2]diagnosticFrame // only the message and diagnostic select fields
	depth   int
	invalid bool
	started bool
	quoted  bool
	key     bool
	field   string
	escape  bool
	hex     [4]byte
	hexN    int
	unicode bool
	raw     [4]byte
	rawN    int
	text    diagnosticText

	kind, level, message, severity, summary, detail string
}

type diagnosticFrame struct {
	object     bool
	wantKey    bool
	key        string
	diagnostic bool
}

func (s *oversizedDiagnostic) write(p []byte) {
	for _, b := range p {
		s.byte(b)
	}
}

func (s *oversizedDiagnostic) byte(b byte) {
	if s.invalid {
		return
	}
	if s.quoted {
		if s.unicode {
			s.hex[s.hexN] = b
			s.hexN++
			if s.hexN == 4 {
				n, err := strconv.ParseUint(string(s.hex[:]), 16, 16)
				if err != nil {
					s.invalid = true
					return
				}
				s.emit(rune(n))
				s.unicode, s.hexN = false, 0
			}
			return
		}
		if s.escape {
			s.escape = false
			switch b {
			case 'u':
				s.unicode = true
			case '"', '\\', '/':
				s.emit(rune(b))
			case 'n':
				s.emit('\n')
			case 'r':
				s.emit('\r')
			case 't':
				s.emit('\t')
			case 'b':
				s.emit('\b')
			case 'f':
				s.emit('\f')
			default:
				s.invalid = true
			}
			return
		}
		switch b {
		case '\\':
			s.escape = true
		case '"':
			if s.rawN != 0 {
				s.invalid = true
				return
			}
			s.quoted = false
			text := s.text.value()
			if s.key {
				s.stack[s.depth-1].key = text
			} else {
				switch s.field {
				case "type":
					s.kind = text
				case "@level":
					s.level = text
				case "@message":
					s.message = text
				case "severity":
					s.severity = text
				case "summary":
					s.summary = text
				case "detail":
					s.detail = text
				}
			}
		default:
			if b < 0x20 {
				s.invalid = true
				return
			}
			if b < utf8.RuneSelf && s.rawN == 0 {
				s.emit(rune(b))
			} else {
				s.raw[s.rawN] = b
				s.rawN++
				if utf8.FullRune(s.raw[:s.rawN]) {
					r, n := utf8.DecodeRune(s.raw[:s.rawN])
					if n != s.rawN || r == utf8.RuneError && n == 1 {
						s.invalid = true
						return
					}
					s.emit(r)
					s.rawN = 0
				}
			}
		}
		return
	}
	if !s.started {
		if b == ' ' || b == '\t' || b == '\r' {
			return
		}
		if b != '{' {
			s.invalid = true
			return
		}
		s.started = true
	}
	switch b {
	case '{', '[':
		if s.depth < len(s.stack) {
			d := s.depth == 1 && s.stack[0].key == "diagnostic" && b == '{'
			s.stack[s.depth] = diagnosticFrame{object: b == '{', wantKey: b == '{', diagnostic: d}
		}
		s.depth++
		if s.depth < 0 { // integer overflow in a never-ending nested value
			s.invalid = true
		}
	case '}', ']':
		if s.depth == 0 || s.depth <= len(s.stack) && s.stack[s.depth-1].object != (b == '}') {
			s.invalid = true
			return
		}
		s.depth--
	case ':':
		if s.depth > 0 && s.depth <= len(s.stack) {
			s.stack[s.depth-1].wantKey = false
		}
	case ',':
		if s.depth > 0 && s.depth <= len(s.stack) {
			s.stack[s.depth-1].wantKey = s.stack[s.depth-1].object
		}
	case '"':
		if s.depth == 0 {
			s.invalid = true
			return
		}
		s.quoted, s.key, s.field = true, false, ""
		if s.depth <= len(s.stack) {
			f := &s.stack[s.depth-1]
			s.key = f.wantKey
			if !s.key {
				if s.depth == 1 && (f.key == "type" || f.key == "@level" || f.key == "@message") ||
					s.depth == 2 && f.diagnostic && (f.key == "severity" || f.key == "summary" || f.key == "detail") {
					s.field = f.key
				}
			}
		}
		s.text.reset()
	}
}

func (s *oversizedDiagnostic) emit(r rune) {
	if s.key || s.field != "" {
		s.text.write(r)
	}
}

// result is deliberately only a diagnostic. An oversized apply_complete or plan
// summary cannot change progress based on a partially retained message.
func (s *oversizedDiagnostic) result() (text string, isError bool) {
	if s.invalid || s.quoted || s.depth != 0 || s.kind != "diagnostic" {
		return "", false
	}
	isError = s.severity == "error" || s.severity == "" && s.level == "error"
	label := "Warning"
	if isError {
		label = "Error"
	}
	text = label + ": " + s.summary
	if s.detail != "" {
		text += "\n\n" + s.detail
	}
	if s.message != "" {
		text += "\n\n" + s.message
	}
	return text + "\n[OpenTofu diagnostic truncated]", isError
}

// diagnosticText retains a small prefix plus a matching CNPG failure anywhere
// in the field. The matcher normalises whitespace like the admission classifier.
// Its fixed arrays also bound a never-terminated string or webhook name.
type diagnosticText struct {
	prefix    [4096]rune
	n         int
	truncated bool
	match     int
	inName    bool
	name      [256]rune
	nameN     int
	space     bool
	found     string
}

const cnpgCallMarker = "failed calling webhook \""

func (t *diagnosticText) reset() {
	t.n, t.match, t.nameN = 0, 0, 0
	t.truncated, t.inName, t.space, t.found = false, false, false, ""
}

func (t *diagnosticText) write(r rune) {
	if t.n < len(t.prefix) {
		t.prefix[t.n] = r
		t.n++
	} else {
		t.truncated = true
	}
	if unicode.IsSpace(r) {
		if t.space {
			return
		}
		t.space, r = true, ' '
	} else {
		t.space = false
	}
	if t.inName {
		if r == '"' {
			name := string(t.name[:t.nameN])
			if strings.HasSuffix(name, ".cnpg.io") && t.found == "" {
				t.found = cnpgCallMarker + name + "\""
			}
			t.inName, t.match = false, 0
		} else {
			if t.nameN == len(t.name) {
				copy(t.name[:], t.name[1:])
				t.nameN--
			}
			t.name[t.nameN] = r
			t.nameN++
		}
		return
	}
	if r == rune(cnpgCallMarker[t.match]) {
		t.match++
		if t.match == len(cnpgCallMarker) {
			t.inName, t.nameN = true, 0
		}
	} else if r == rune(cnpgCallMarker[0]) {
		t.match = 1
	} else {
		t.match = 0
	}
}

func (t *diagnosticText) value() string {
	text := string(t.prefix[:t.n])
	if t.truncated {
		text += "\n[truncated]"
		if t.found != "" {
			text += "\n" + t.found
		}
	}
	return text
}
