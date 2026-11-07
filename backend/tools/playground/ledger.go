// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// DayLedger remembers, per sim, the playground day it was created on. It lives beside the
// sim records on the same volume: a record without an entry is a sim an older driver made,
// and is trusted; an entry from another day is a tenant a destroy failed to remove.
//
// A nil ledger records nothing and finds nothing, so callers need not guard it.
type DayLedger struct {
	path string
	mu   sync.Mutex
}

func NewDayLedger(path string) *DayLedger { return &DayLedger{path: path} }

func (l *DayLedger) read() (map[string]int64, error) {
	b, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]int64{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]int64{}
	if err := json.Unmarshal(b, &m); err != nil {
		// A ledger that cannot be read is not guessed at: trusting an empty one would
		// turn off the check it exists for.
		return nil, fmt.Errorf("the day ledger %s is unreadable: %w", l.path, err)
	}
	return m, nil
}

func (l *DayLedger) write(m map[string]int64) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

// Get is the day name was recorded on.
func (l *DayLedger) Get(name string) (int64, bool, error) {
	if l == nil {
		return 0, false, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m, err := l.read()
	if err != nil {
		return 0, false, err
	}
	d, ok := m[name]
	return d, ok, nil
}

// Set records the day a sim was created on. The driver only calls it after Get has
// shown the sim is unrecorded or recorded for this same day, so a restart inside the day
// writes what is already there.
func (l *DayLedger) Set(name string, day int64) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m, err := l.read()
	if err != nil {
		return err
	}
	if cur, ok := m[name]; ok && cur == day {
		return nil
	}
	m[name] = day
	return l.write(m)
}

// Forget drops a sim that was destroyed or was never there.
func (l *DayLedger) Forget(name string) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m, err := l.read()
	if err != nil {
		return err
	}
	if _, ok := m[name]; !ok {
		return nil
	}
	delete(m, name)
	return l.write(m)
}
