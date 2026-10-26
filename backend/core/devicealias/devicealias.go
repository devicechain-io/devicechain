// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package devicealias is the single definition of the identity aliases a device can carry
// besides its token, and the one normalization applied to them.
//
// An alias is a (scheme, value) pair naming a device in the vocabulary of a sender that is
// not the platform: a LoRaWAN network server names a device by its DevEUI, a webhook by
// whatever id the tenant chose. The scheme says whose vocabulary the value is in, so two
// senders that happen to use the same string for different devices never collide, and the
// value is stored in ONE canonical spelling, so a sender that writes the same DevEUI in
// upper case or lower case finds the same device.
//
// The package is a leaf (stdlib only), because the two sides of a lookup must agree on the
// canonical form to the byte: the service that writes an alias and the service that resolves
// a sender's identifier against it normalize through this one function. Two copies of the
// normalization is how one side starts storing upper case while the other looks up lower.
//
// # What is deliberately not here
//
// externalId is a different thing. It is the customer's own business identifier, matched
// exactly, and it may already hold a serial number that looks like a DevEUI. An alias lives
// in its own table so that neither can be mistaken for the other.
package devicealias

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The closed vocabulary of platform schemes. A tenant's own schemes are custom.<name>.
const (
	// SchemeDevEUI is a LoRaWAN DevEUI: 16 hexadecimal digits, stored upper case.
	SchemeDevEUI = "lorawan.deveui"
	// SchemeTTSDeviceID is The Things Stack's device id, stored lower case.
	SchemeTTSDeviceID = "tts.device-id"
	// CustomPrefix prefixes a tenant-chosen scheme.
	CustomPrefix = "custom."
)

const (
	// MaxValueLen bounds a custom alias value, in characters. The platform schemes are
	// bounded tighter by their own grammar.
	MaxValueLen = 128
	// MaxSchemeLen bounds a scheme, in characters.
	MaxSchemeLen = 70
	// MaxPerDevice bounds how many aliases one device carries.
	MaxPerDevice = 8
)

// ErrInvalid is the root of every refusal this package returns. Callers map it to their
// invalid-value answer without parsing text.
var ErrInvalid = errors.New("invalid device alias")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// customScheme is the grammar of a tenant-chosen scheme: custom.<name>, the name being lower
// case letters, digits, '-' and '_', starting with a letter or digit, at most 63 characters.
var customScheme = regexp.MustCompile(`^custom\.[a-z0-9][a-z0-9_-]{0,62}$`)

// ttsDeviceID is the grammar of The Things Stack's device id once lower-cased.
var ttsDeviceID = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,34}[a-z0-9])?$`)

// IsCustomScheme reports whether s is a well-formed tenant-chosen scheme.
func IsCustomScheme(s string) bool { return customScheme.MatchString(s) }

// ValidScheme reports whether s is in the vocabulary: a platform scheme or a well-formed
// custom one.
func ValidScheme(s string) bool {
	switch s {
	case SchemeDevEUI, SchemeTTSDeviceID:
		return true
	}
	return IsCustomScheme(s)
}

// Normalize returns the canonical stored form of value under scheme, or an error wrapping
// ErrInvalid when the scheme is not in the vocabulary or the value does not fit it.
//
//   - lorawan.deveui: 16 hexadecimal digits in either case, stored upper case. Separators
//     are not accepted: a value that is not exactly 16 digits is a mistake to report, not a
//     string to repair into a different device.
//   - tts.device-id: lower-cased, then 1 to 36 lower-case letters, digits and hyphens that
//     neither start nor end with a hyphen.
//   - custom.<name>: the surrounding space trimmed and the spelling otherwise kept, 1 to
//     MaxValueLen characters with no control characters. A tenant's own identifiers may be
//     case-sensitive, so no case folding is applied.
func Normalize(scheme, value string) (string, error) {
	if len(scheme) > MaxSchemeLen || !ValidScheme(scheme) {
		return "", invalid("scheme %q is not one of %s, %s or %s<name>", clip(scheme), SchemeDevEUI, SchemeTTSDeviceID, CustomPrefix)
	}
	switch scheme {
	case SchemeDevEUI:
		v := strings.TrimSpace(value)
		if len(v) != 16 || !isHex(v) {
			return "", invalid("a %s value must be exactly 16 hexadecimal digits", scheme)
		}
		return strings.ToUpper(v), nil
	case SchemeTTSDeviceID:
		v := strings.ToLower(strings.TrimSpace(value))
		if !ttsDeviceID.MatchString(v) {
			return "", invalid("a %s value must be 1 to 36 letters, digits and hyphens, not starting or ending with a hyphen", scheme)
		}
		return v, nil
	}
	v := strings.TrimSpace(value)
	if v == "" {
		return "", invalid("a %s value must not be empty", scheme)
	}
	if !utf8.ValidString(v) {
		return "", invalid("a %s value must be valid UTF-8", scheme)
	}
	if utf8.RuneCountInString(v) > MaxValueLen {
		return "", invalid("a %s value is at most %d characters", scheme, MaxValueLen)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "", invalid("a %s value must not contain control characters", scheme)
		}
	}
	return v, nil
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// clip keeps a refusal that echoes caller input from echoing an unbounded amount of it.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= 40 {
		return s
	}
	r := []rune(s)
	return string(r[:40]) + "..."
}
