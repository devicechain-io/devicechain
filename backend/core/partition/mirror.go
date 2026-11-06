// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partition

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	nats "github.com/nats-io/nats.go"
)

// TableKey is the one key the placement table is mirrored under.
//
// 🔴 THE WHOLE TABLE IS ONE ENTRY, on purpose. A table spread over a key per vpartition plus a
// key per override would be read as a sequence of gets, and a reader could assemble a placement
// no writer ever held (half of a cutover). One key is one revision: a reader sees the table
// before a change or after it, never between.
const TableKey = "table"

// ErrNoTable is returned by Load when nothing has been mirrored yet.
var ErrNoTable = errors.New("partition: no placement table has been mirrored")

// ErrConflict is what a Bucket returns when a write lost a race: Create found the key already
// there, or Update found it at another revision.
var ErrConflict = errors.New("partition: the mirror changed underneath this write")

// ErrStale is returned by Publish when the mirror already holds a NEWER placement than the one
// offered. A replica working from an old read must never move the mirror backwards; it re-reads
// the database and offers the newer table on its next pass.
var ErrStale = errors.New("partition: the mirror already holds a newer placement; not overwriting it")

// ErrDivergent is returned by Publish when the mirror holds a different placement at the SAME
// generation as the one offered: two authorities disagree, and neither is overwritten.
var ErrDivergent = errors.New("partition: the mirror holds a different placement at the same generation")

// Bucket is the slice of a KV bucket the mirror needs. nats.KeyValue satisfies it through
// FromKeyValue; tests use a map. Writes are compare-and-set on the key's revision, so two
// publishers cannot overwrite each other from stale reads.
type Bucket interface {
	// Get returns the value and its revision, or found == false when the key does not exist.
	Get(key string) (value []byte, revision uint64, found bool, err error)
	// Create writes a key that must not exist; ErrConflict when it does.
	Create(key string, value []byte) error
	// Update writes a key that must be at revision; ErrConflict when it is not.
	Update(key string, value []byte, revision uint64) error
}

// FromKeyValue adapts a JetStream KV bucket.
func FromKeyValue(kv nats.KeyValue) Bucket { return natsBucket{kv} }

type natsBucket struct{ kv nats.KeyValue }

func (b natsBucket) Get(key string) ([]byte, uint64, bool, error) {
	e, err := b.kv.Get(key)
	if errors.Is(err, nats.ErrKeyNotFound) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	return e.Value(), e.Revision(), true, nil
}

func (b natsBucket) Create(key string, value []byte) error {
	_, err := b.kv.Create(key, value)
	if errors.Is(err, nats.ErrKeyExists) {
		return ErrConflict
	}
	return err
}

func (b natsBucket) Update(key string, value []byte, revision uint64) error {
	_, err := b.kv.Update(key, value, revision)
	if errors.Is(err, nats.ErrKeyRevisionMismatch) || errors.Is(err, nats.ErrKeyExists) {
		return ErrConflict
	}
	return err
}

// Encode is the canonical encoding of a table: validated first, and byte-stable (encoding/json
// sorts map keys), so two equal tables encode identically and a refresh of an unchanged table is
// a byte-for-byte rewrite.
func Encode(t Table) ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(t)
}

// Decode parses and validates a mirrored table. A Format newer than this build's is refused: a
// table a newer release wrote that this reader would half-understand must fail loudly, not route
// on the half. Unknown fields under a known format are tolerated, so a minor can add a field
// without stopping an older reader.
func Decode(b []byte) (Table, error) {
	var t Table
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&t); err != nil {
		return Table{}, fmt.Errorf("partition: the mirrored table does not parse: %w", err)
	}
	if err := t.Validate(); err != nil {
		return Table{}, err
	}
	return t, nil
}

// read returns the mirrored table with the revision it was read at.
func read(b Bucket) (t Table, revision uint64, found bool, err error) {
	raw, rev, found, err := b.Get(TableKey)
	if err != nil {
		return Table{}, 0, false, fmt.Errorf("partition: reading the mirrored table: %w", err)
	}
	if !found {
		return Table{}, 0, false, nil
	}
	t, err = Decode(raw)
	return t, rev, true, err
}

// Load reads the mirrored table.
func Load(b Bucket) (Table, error) {
	t, _, found, err := read(b)
	if err != nil {
		return Table{}, err
	}
	if !found {
		return Table{}, ErrNoTable
	}
	return t, nil
}

// publishAttempts bounds how often Publish re-reads after losing a race.
const publishAttempts = 4

// Publish mirrors t.
//
// It is a compare-and-set against the revision it read, so it can neither resurrect an older
// placement over a newer one nor decide P twice:
//
//   - a mirrored P that differs from t.P is ErrPChanged, and nothing is written. The database
//     row is the first guard of P's immutability; this is the one that still holds when the row is
//     wrong (a restore, a hand edit), and because the write is conditional on what was read, two
//     publishers cannot both succeed with different P values.
//   - a mirrored table with a higher Generation is ErrStale, and one with the same Generation
//     and different content is ErrDivergent.
//   - otherwise it writes, even for an unchanged table: the bucket's entries expire, and a rewrite
//     is how an unchanged mirror stays alive.
func Publish(b Bucket, t Table) error {
	raw, err := Encode(t)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < publishAttempts; attempt++ {
		cur, rev, found, err := read(b)
		if err != nil {
			return err
		}
		if found {
			if err := CheckP(cur.P, t.P); err != nil {
				return err
			}
			if cur.Generation > t.Generation {
				return fmt.Errorf("%w (mirror generation %d, offered %d)", ErrStale, cur.Generation, t.Generation)
			}
			if cur.Generation == t.Generation && !cur.Equal(t) {
				return fmt.Errorf("%w (generation %d)", ErrDivergent, t.Generation)
			}
			err = b.Update(TableKey, raw, rev)
		} else {
			err = b.Create(TableKey, raw)
		}
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return fmt.Errorf("partition: mirroring the table: %w", err)
		}
		return nil
	}
	return fmt.Errorf("partition: mirroring the table: lost the race %d times: %w", publishAttempts, ErrConflict)
}

// Verify reports whether the mirror holds exactly t. It is the check that the mirror is the
// table, not merely a table.
func Verify(b Bucket, t Table) error {
	got, err := Load(b)
	if err != nil {
		return err
	}
	if !got.Equal(t) {
		return fmt.Errorf("partition: the mirror (generation %d, P %d) differs from the table (generation %d, P %d)",
			got.Generation, got.P, t.Generation, t.P)
	}
	return nil
}
