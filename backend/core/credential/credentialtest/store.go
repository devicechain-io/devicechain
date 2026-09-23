// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package credentialtest provides an in-memory credential.Store for tests.
//
// 🔴 IT MIRRORS THE nats.go SEMANTICS THE CHECKER DEPENDS ON, BECAUSE A FAKE THAT DIFFERS
// MAKES THE COMPARE-AND-SET TESTS PASS WHILE PRODUCTION FAILS. Each of these was read in
// nats.go's kv.go rather than assumed:
//
//   - Get on a missing OR deleted key returns nats.ErrKeyNotFound (kvs.Get maps
//     ErrKeyDeleted to it).
//   - Create over a delete tombstone SUCCEEDS (kvs.Create retries against the
//     tombstone's revision), and over a live key fails matching nats.ErrKeyExists.
//   - Update with a stale revision fails matching nats.ErrKeyRevisionMismatch; Update of
//     a missing key with a non-zero revision fails the same way.
//   - Revisions are one sequence across the whole bucket, as a stream's are.
//
// Delete options are accepted and IGNORED — their contents are unexported in nats.go,
// and the Checker passes none. The core tests also run the Checker once against a real
// embedded JetStream server, which is what keeps this file honest.
package credentialtest

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

type entry struct {
	value    []byte
	revision uint64
	deleted  bool
}

// Store is a concurrency-safe in-memory attempt store.
type Store struct {
	mu   sync.Mutex
	seq  uint64
	keys map[string]*entry
	ops  []string

	// Fail, when non-nil, is returned by every operation — a broker that is down.
	Fail error
	// BeforeWrite, when non-nil, runs before every Create and Update is applied, with
	// the lock released. A test uses it to land a competing write in the gap between a
	// read and the write that depends on it.
	BeforeWrite func(key string)
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{keys: map[string]*entry{}} }

// Ops returns the operations performed so far, in order ("get", "create", "update",
// "delete", each with its outcome), so a test can compare the TRACE two principals
// produce rather than only their results.
func (s *Store) Ops() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ops...)
}

// Len returns how many live (non-deleted) keys the store holds.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.keys {
		if !e.deleted {
			n++
		}
	}
	return n
}

func (s *Store) record(op string, err error) {
	outcome := "ok"
	switch {
	case err == nil:
	case errors.Is(err, nats.ErrKeyNotFound):
		outcome = "not-found"
	case errors.Is(err, nats.ErrKeyExists), errors.Is(err, nats.ErrKeyRevisionMismatch):
		outcome = "conflict"
	default:
		outcome = "error"
	}
	s.ops = append(s.ops, op+":"+outcome)
}

// Get implements credential.Store.
func (s *Store) Get(key string) (nats.KeyValueEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		s.record("get", s.Fail)
		return nil, s.Fail
	}
	e, ok := s.keys[key]
	if !ok || e.deleted {
		s.record("get", nats.ErrKeyNotFound)
		return nil, nats.ErrKeyNotFound
	}
	s.record("get", nil)
	return kvEntry{key: key, value: append([]byte(nil), e.value...), revision: e.revision}, nil
}

// Create implements credential.Store.
func (s *Store) Create(key string, value []byte) (uint64, error) {
	if hook := s.hook(); hook != nil {
		hook(key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		s.record("create", s.Fail)
		return 0, s.Fail
	}
	if e, ok := s.keys[key]; ok && !e.deleted {
		err := fmt.Errorf("wrong last sequence: %w", nats.ErrKeyExists)
		s.record("create", err)
		return 0, err
	}
	s.seq++
	s.keys[key] = &entry{value: append([]byte(nil), value...), revision: s.seq}
	s.record("create", nil)
	return s.seq, nil
}

// Update implements credential.Store.
func (s *Store) Update(key string, value []byte, last uint64) (uint64, error) {
	if hook := s.hook(); hook != nil {
		hook(key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		s.record("update", s.Fail)
		return 0, s.Fail
	}
	e, ok := s.keys[key]
	if !ok || e.revision != last {
		err := fmt.Errorf("wrong last sequence: %w", nats.ErrKeyRevisionMismatch)
		s.record("update", err)
		return 0, err
	}
	s.seq++
	*e = entry{value: append([]byte(nil), value...), revision: s.seq}
	s.record("update", nil)
	return s.seq, nil
}

// Delete implements credential.Store. Like a KV delete it leaves a tombstone, which
// Get reports as not found and Create may overwrite.
func (s *Store) Delete(key string, _ ...nats.DeleteOpt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		s.record("delete", s.Fail)
		return s.Fail
	}
	s.seq++
	s.keys[key] = &entry{revision: s.seq, deleted: true}
	s.record("delete", nil)
	return nil
}

func (s *Store) hook() func(string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.BeforeWrite
}

type kvEntry struct {
	key      string
	value    []byte
	revision uint64
}

func (e kvEntry) Bucket() string             { return "credentialtest" }
func (e kvEntry) Key() string                { return e.key }
func (e kvEntry) Value() []byte              { return e.value }
func (e kvEntry) Revision() uint64           { return e.revision }
func (e kvEntry) Created() time.Time         { return time.Time{} }
func (e kvEntry) Delta() uint64              { return 0 }
func (e kvEntry) Operation() nats.KeyValueOp { return nats.KeyValuePut }
