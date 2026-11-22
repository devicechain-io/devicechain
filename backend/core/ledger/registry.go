// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ledger

import "errors"

// Kind names an effect vocabulary. Posting belongs to the future engine release.
type Kind interface {
	Name() string
}

// Served is false until an engine and its activation guarantees ship together.
const Served = false

// ErrUnserved means this build cannot execute any kind.
var ErrUnserved = errors.New("fleet-operation kind registration refused: engine is not implemented")

// Register refuses every registration, including empty, duplicate and nil kinds.
// A kind with no engine behind it would be a plausible value from an unimplemented
// feature. This release intentionally has no mutable registry to activate.
func Register(k Kind) error {
	return ErrUnserved
}

// ExecutableKinds returns a sorted copy of the executable names. This build has
// none; each returned empty slice can be extended without altering package state.
func ExecutableKinds() []string { return []string{} }

// Executable reports whether this build can execute name. No names are registered.
func Executable(name string) bool { return false }
