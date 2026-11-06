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

// Bucket is the slice of a KV bucket the mirror needs. nats.KeyValue satisfies it through
// FromKeyValue; tests use a map.
type Bucket interface {
	Put(key string, value []byte) error
	// Get returns the value, or found == false when the key does not exist.
	Get(key string) (value []byte, found bool, err error)
}

// FromKeyValue adapts a JetStream KV bucket.
func FromKeyValue(kv nats.KeyValue) Bucket { return natsBucket{kv} }

type natsBucket struct{ kv nats.KeyValue }

func (b natsBucket) Put(key string, value []byte) error {
	_, err := b.kv.Put(key, value)
	return err
}

func (b natsBucket) Get(key string) ([]byte, bool, error) {
	e, err := b.kv.Get(key)
	if errors.Is(err, nats.ErrKeyNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return e.Value(), true, nil
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

// Decode parses and validates a mirrored table. Unknown fields are refused: a table written by a
// newer release that this reader would half-understand must fail loudly, not route on the half.
func Decode(b []byte) (Table, error) {
	var t Table
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return Table{}, fmt.Errorf("partition: the mirrored table does not parse: %w", err)
	}
	if err := t.Validate(); err != nil {
		return Table{}, err
	}
	return t, nil
}

// Load reads the mirrored table.
func Load(b Bucket) (Table, error) {
	raw, found, err := b.Get(TableKey)
	if err != nil {
		return Table{}, fmt.Errorf("partition: reading the mirrored table: %w", err)
	}
	if !found {
		return Table{}, ErrNoTable
	}
	return Decode(raw)
}

// Publish mirrors t, REFUSING to replace a mirrored table whose P differs (ErrPChanged). The
// refusal is the mirror's half of P's immutability: the database row is the first guard, and
// this is the one that still holds when the row is wrong (a restore, a hand edit).
//
// It writes unconditionally when the P agrees, even for an unchanged table: the bucket's entries
// expire, and a rewrite is how an unchanged mirror stays alive.
func Publish(b Bucket, t Table) error {
	raw, err := Encode(t)
	if err != nil {
		return err
	}
	cur, err := Load(b)
	switch {
	case err == nil:
		if err := CheckP(cur.P, t.P); err != nil {
			return err
		}
	case errors.Is(err, ErrNoTable):
	default:
		return err
	}
	if err := b.Put(TableKey, raw); err != nil {
		return fmt.Errorf("partition: mirroring the table: %w", err)
	}
	return nil
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
