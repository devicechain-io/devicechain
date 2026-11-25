// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxTextBytes caps one SafeText, and so one log line, in bytes.
const MaxTextBytes = 8 << 10

const truncationMark = "…"

// SafeText is display text that has been sanitised: terminal escape sequences
// (CSI, OSC, DCS and the rest), C0 and C1 controls and bidirectional overrides
// are removed, line breaks and tabs become spaces, invalid UTF-8 is replaced,
// and the result is capped at MaxTextBytes. The only way to make one is Text,
// so a payload field of this type has always been through it.
//
// Sanitising is not secret handling. A producer still decides what may be
// shown; the hub additionally redacts known secret values from every SafeText
// it carries.
type SafeText struct{ s string }

// Text sanitises s into SafeText.
func Text(s string) SafeText { return SafeText{s: sanitize(s)} }

// String returns the sanitised text.
func (t SafeText) String() string { return t.s }

// IsZero reports whether the text is empty.
func (t SafeText) IsZero() bool { return t.s == "" }

// MarshalJSON encodes the sanitised text as a JSON string.
func (t SafeText) MarshalJSON() ([]byte, error) { return json.Marshal(t.s) }

func sanitize(s string) string {
	var b strings.Builder
	b.Grow(min(len(s), MaxTextBytes))
	const (
		ground = iota
		escape // after ESC
		csi    // ESC [ ... final byte
		str    // OSC / DCS / SOS / PM / APC body, ends at BEL or ST
		strEsc // ESC inside a string body
	)
	state := ground
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch state {
		case escape:
			switch r {
			case '[':
				state = csi
			case ']', 'P', 'X', '^', '_':
				state = str
			default:
				// A two-byte escape (or an intermediate we do not model): drop
				// the introducer and this byte.
				state = ground
			}
			continue
		case csi:
			if r >= 0x40 && r <= 0x7e {
				state = ground
			}
			continue
		case str:
			switch r {
			case 0x07:
				state = ground
			case 0x1b:
				state = strEsc
			}
			continue
		case strEsc:
			if r == '\\' {
				state = ground
			} else {
				state = str
			}
			continue
		}
		switch {
		case r == 0x1b:
			state = escape
			continue
		case r == 0x9b: // C1 CSI
			state = csi
			continue
		case r == 0x9d || r == 0x90 || r == 0x98 || r == 0x9e || r == 0x9f: // C1 OSC, DCS, SOS, PM, APC
			state = str
			continue
		case r == '\t' || r == '\n' || r == '\r':
			r = ' '
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			continue
		case (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			continue
		}
		if b.Len()+utf8.RuneLen(r) > MaxTextBytes-len(truncationMark) {
			// Always leave room for the mark, so a truncated text says so.
			b.WriteString(truncationMark)
			return b.String()
		}
		b.WriteRune(r)
	}
	return b.String()
}

// capText re-applies the cap after redaction changed a text's length.
func capText(s string) string {
	if len(s) <= MaxTextBytes {
		return s
	}
	cut := MaxTextBytes - len(truncationMark)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncationMark
}

const redactedMark = "[redacted]"

// Redacted carries a sensitive string through code that may print or encode
// it by accident. Every textual form of it — fmt verbs, String, GoString,
// JSON and text marshalling — is a fixed placeholder; only Expose returns the
// value, for the code that genuinely needs it.
type Redacted struct{ v string }

// Redact wraps v.
func Redact(v string) Redacted { return Redacted{v: v} }

// Expose returns the wrapped value. Never pass it to a payload.
func (r Redacted) Expose() string { return r.v }

// IsZero reports whether no value is wrapped.
func (r Redacted) IsZero() bool { return r.v == "" }

func (Redacted) String() string               { return redactedMark }
func (Redacted) GoString() string             { return redactedMark }
func (Redacted) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte(redactedMark)) }
func (Redacted) MarshalJSON() ([]byte, error) { return json.Marshal(redactedMark) }
func (Redacted) MarshalText() ([]byte, error) { return []byte(redactedMark), nil }

var errNotSerialisable = errors.New("installflow: a secret value is not serialisable")

const secretMark = "[secret]"

// Secret holds a secret answer, such as an escrow passphrase, as bytes that
// can be cleared. Copies share storage, so Zero clears every copy. Its textual
// forms are a placeholder, and marshalling it is an error rather than a
// placeholder: a secret reaching an encoder is a defect to surface, not to
// paper over. There is deliberately no length accessor.
type Secret struct{ b *[]byte }

// NewSecret copies p into a new Secret.
func NewSecret(p []byte) Secret {
	c := append([]byte(nil), p...)
	return Secret{b: &c}
}

// Expose returns the secret bytes. The slice aliases the Secret's storage.
func (s Secret) Expose() []byte {
	if s.b == nil {
		return nil
	}
	return *s.b
}

// IsEmpty reports whether the secret holds no bytes.
func (s Secret) IsEmpty() bool { return s.b == nil || len(*s.b) == 0 }

// Zero overwrites the secret's bytes and empties it. We make no claim about
// copies the holder made as strings.
func (s Secret) Zero() {
	if s.b == nil {
		return
	}
	clear(*s.b)
	*s.b = (*s.b)[:0]
}

func (Secret) String() string               { return secretMark }
func (Secret) GoString() string             { return secretMark }
func (Secret) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte(secretMark)) }
func (Secret) MarshalJSON() ([]byte, error) { return nil, errNotSerialisable }
func (Secret) MarshalText() ([]byte, error) { return nil, errNotSerialisable }

// SecretRef names where a secret is kept — never what it is. It is the
// allowlisted way to point at a credential from an event or a summary.
type SecretRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Key       string `json:"key,omitempty"`
}

var (
	dnsLabel     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	dnsSubdomain = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	secretKey    = regexp.MustCompile(`^[-._a-zA-Z0-9]{1,253}$`)
)

func (r SecretRef) validate() error {
	if !dnsLabel.MatchString(r.Namespace) || !dnsSubdomain.MatchString(r.Name) ||
		(r.Key != "" && !secretKey.MatchString(r.Key)) {
		return fmt.Errorf("%w: secret reference is not a Kubernetes name", ErrInvalidEvent)
	}
	return nil
}
