// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// ValueDigest identifies one typed value without carrying it:
//
//	"sha256:" + lowercase hex SHA-256 of the UTF-8 bytes of valueType + ":" + value
//
// The value text is exact: "40" and "040" are different values. The type is part of the digest,
// so LONG "40" and STRING "40" differ. The type vocabulary contains no ':', so the first ':'
// always ends the type and no two (type, value) pairs share an input.
//
// Device-management computes the same function over stored attribute rows, in SQL:
//
//	'sha256:' || encode(sha256(convert_to(value_type || ':' || value, 'UTF8')), 'hex')
//
// so an observation page carries a 71-byte digest whatever the size of the value. The format is
// durable; testdata/vectors.json pins it.
func ValueDigest(valueType, value string) (string, error) {
	if !ValidValueType(valueType) {
		return "", fmt.Errorf("value digest: unknown value type %q", valueType)
	}
	sum := sha256.Sum256([]byte(valueType + ":" + value))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Intended is the value an intent wants one device to hold at one instant. When Unset is true
// the key must be absent and ValueType and ValueDigest are empty.
type Intended struct {
	ValueType   string
	ValueDigest string
	Unset       bool
}

// IntendedValue is the intended-value function. In v1 every value is a constant, so the device
// and the instant do not change the answer; they are parameters so that a later value kind (a
// schedule) needs no new signature. It reads no clock.
func IntendedValue(s Spec, deviceID int64, at time.Time) (Intended, error) {
	if err := s.Validate(); err != nil {
		return Intended{}, err
	}
	return intended(s, deviceID, at)
}

// intended is IntendedValue on a spec the caller has already validated.
func intended(s Spec, deviceID int64, at time.Time) (Intended, error) {
	_, _ = deviceID, at
	if s.Value.Unset {
		return Intended{Unset: true}, nil
	}
	digest, err := ValueDigest(s.Value.ValueType, *s.Value.Value)
	if err != nil {
		return Intended{}, err
	}
	return Intended{ValueType: s.Value.ValueType, ValueDigest: digest}, nil
}

// InstantBucket is the part of an instant that can change what an intent wants. A v1 value is a
// constant, so the bucket is always 0: the passage of time alone never moves InputsDigest, and an
// unchanged fleet is skipped. A later time-varying value kind returns the index of the schedule
// segment that contains at.
func InstantBucket(s Spec, at time.Time) (int64, error) {
	if err := s.Validate(); err != nil {
		return 0, err
	}
	_ = at
	return 0, nil
}

// SameSubject reports whether two specs compete for the same thing: the same configuration key,
// the same class, and different intended values. It says nothing about targets. Whether the two
// targets share a device is a question only device-management can answer, over both frozen
// selectors; SameSubject only decides which pairs to ask it about. Two specs that agree on the
// value do not compete, whatever their targets.
func SameSubject(a, b Spec) (bool, error) {
	if err := a.Validate(); err != nil {
		return false, err
	}
	if err := b.Validate(); err != nil {
		return false, err
	}
	if a.Point.ConfigKey != b.Point.ConfigKey || a.Class != b.Class {
		return false, nil
	}
	return !sameValue(a.Value, b.Value), nil
}

func sameValue(a, b Value) bool {
	if a.Unset || b.Unset {
		return a.Unset == b.Unset
	}
	return a.ValueType == b.ValueType && *a.Value == *b.Value
}

// validDigest reports whether d is "sha256:" followed by exactly 64 lowercase hex characters.
func validDigest(d string) bool {
	hexPart, ok := strings.CutPrefix(d, "sha256:")
	if !ok || len(hexPart) != 64 {
		return false
	}
	for i := 0; i < len(hexPart); i++ {
		c := hexPart[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}
