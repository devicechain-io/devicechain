// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package durability is the vocabulary for what happens to a piece of non-database durable state
// (a JetStream stream, a KV bucket, the object store) when an instance is restored from backup.
//
// The test for a class is the one the disaster-recovery design applies to every item: if this is
// empty after a restore, what is lost that the database restore does not already reflect?
//
// It is a LEAF (it imports nothing) so that core/streams and core/kv, themselves leaves, can carry
// a class on every declaration. Adding a stream or a bucket without choosing one fails
// core/backupset's tests, because the zero value is Unclassified on purpose: a default of
// "rebuildable" would let a new item that holds the only copy of something pass as disposable.
package durability

import "fmt"

// Class says what a restore owes an item.
type Class int

const (
	// Unclassified is the zero value and is never valid on a declaration.
	Unclassified Class = iota
	// MustBackUp holds the only copy of something a promise was made about. The platform provides a
	// backup procedure for it.
	MustBackUp
	// Rebuildable is re-published or recomputed from the database, or sent again by whatever owns
	// it (a device, an integration, a person signing in), by a mechanism that exists in the running
	// system. Empty is correct after a restore. Where the sender must act (a device that has to
	// subscribe again), the item's reason says so.
	Rebuildable
	// Transient is in flight or short-lived. Losing it is bounded by pipeline lag or by its
	// lifetime and is part of the published recovery point.
	Transient
)

// String is the identifier used in the documentation table; it is part of what the docs pin.
func (c Class) String() string {
	switch c {
	case MustBackUp:
		return "must-back-up"
	case Rebuildable:
		return "rebuildable"
	case Transient:
		return "transient"
	case Unclassified:
		return "unclassified"
	}
	return fmt.Sprintf("durability.Class(%d)", int(c))
}

// Valid reports whether c is a class a declaration may carry.
func (c Class) Valid() bool { return c == MustBackUp || c == Rebuildable || c == Transient }
