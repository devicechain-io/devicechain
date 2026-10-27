// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package boot is the A/B slot state shared by the launcher (ota-boot) and the payload
// (ota-device), and the launcher's decision table. It stands in for what a bootloader does on a
// real board: pick a slot, try a new one once, and fall back to the last one that proved itself.
//
// State lives in <root>/boot.json:
//
//	{"confirmed":"a","pending":"b","tries":1}
//
// confirmed is the slot that has run to a healthy state and is the fallback. pending is a freshly
// installed slot that has not yet proved itself; the launcher gives it exactly one try. The
// payload confirms itself (moves pending into confirmed) only after it is up and healthy.
package boot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"devicechain.io/examples/ota-device/internal/fsutil"
)

// Slots are the two places an image can live.
const (
	SlotA = "a"
	SlotB = "b"
)

// Other returns the slot that is not s.
func Other(s string) string {
	if s == SlotA {
		return SlotB
	}
	return SlotA
}

func validSlot(s string) bool { return s == SlotA || s == SlotB }

// State is boot.json.
type State struct {
	Confirmed string `json:"confirmed"`
	Pending   string `json:"pending,omitempty"`
	Tries     int    `json:"tries,omitempty"`
}

// Validate reports whether the state is one the launcher can act on.
func (s State) Validate() error {
	if !validSlot(s.Confirmed) {
		return fmt.Errorf("confirmed slot %q is not a or b", s.Confirmed)
	}
	if s.Pending != "" && !validSlot(s.Pending) {
		return fmt.Errorf("pending slot %q is not a or b", s.Pending)
	}
	if s.Pending == "" && s.Tries != 0 {
		return errors.New("tries is set with nothing pending")
	}
	if s.Tries < 0 {
		return errors.New("tries is negative")
	}
	return nil
}

// Rollback is rollback.json: the launcher's note to the payload that the slot it tried did not
// make it, so the payload can report why the update it was expecting never arrived.
type Rollback struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// Paths of the files under a root.
func StatePath(root string) string    { return filepath.Join(root, "boot.json") }
func goodPath(root string) string     { return filepath.Join(root, "boot.json.good") }
func RollbackPath(root string) string { return filepath.Join(root, "rollback.json") }

// SlotDir is the directory a slot's image lives in.
func SlotDir(root, slot string) string { return filepath.Join(root, "slots", slot) }

// ImageName is the file name of the payload inside a slot.
func ImageName() string { return "ota-device" + fsutil.ExeSuffix() }

// ImagePath is the payload executable of a slot.
func ImagePath(root, slot string) string { return filepath.Join(SlotDir(root, slot), ImageName()) }

// Save writes the state atomically. When nothing is pending the state is quiescent, and a copy is
// kept as boot.json.good: the one thing the launcher can fall back on if boot.json is ever
// unreadable.
func Save(root string, s State) error {
	if err := s.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(StatePath(root), b, 0o600); err != nil {
		return err
	}
	if s.Pending == "" {
		return fsutil.WriteFileAtomic(goodPath(root), b, 0o600)
	}
	return nil
}

// Load reads the state. A boot.json that is missing, unparseable or invalid falls back to the
// last quiescent copy, and Recovered says so. If that is unusable too, Load fails: the launcher
// does not guess which slot to run.
func Load(root string) (s State, recovered bool, err error) {
	if s, err = read(StatePath(root)); err == nil {
		return s, false, nil
	}
	primary := err
	if s, err = read(goodPath(root)); err == nil {
		// The pending change that was in flight is dropped: an unreadable state is not evidence
		// that the pending slot ever proved itself.
		return State{Confirmed: s.Confirmed}, true, nil
	}
	return State{}, false, fmt.Errorf("boot state unusable (%v) and no good copy (%v)", primary, err)
}

func read(path string) (State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, err
	}
	return s, s.Validate()
}

// Decision is what the launcher does for one start of the child.
type Decision struct {
	Launch   string    // the slot to run
	State    State     // the state to persist before running it
	Rollback *Rollback // non-nil when this decision abandons a pending slot
}

// Decide is the launcher's whole policy as a pure function:
//
//   - nothing pending: run the confirmed slot;
//   - pending, not yet tried: run it, counting the try;
//   - pending, already tried and still unconfirmed: it failed to prove itself, so forget it, note
//     the rollback, and run the confirmed slot.
func Decide(s State) Decision {
	switch {
	case s.Pending == "":
		return Decision{Launch: s.Confirmed, State: State{Confirmed: s.Confirmed}}
	case s.Tries < 1:
		return Decision{Launch: s.Pending, State: State{Confirmed: s.Confirmed, Pending: s.Pending, Tries: s.Tries + 1}}
	default:
		return Decision{
			Launch:   s.Confirmed,
			State:    State{Confirmed: s.Confirmed},
			Rollback: &Rollback{From: s.Pending, To: s.Confirmed, Reason: "the new slot did not confirm itself after its one boot attempt"},
		}
	}
}

// Confirm is called by the payload once it is up and healthy: if it is the pending slot, it
// becomes the confirmed one. It returns whether anything changed.
func Confirm(root, slot string) (bool, error) {
	s, _, err := Load(root)
	if err != nil {
		return false, err
	}
	if s.Pending != slot {
		return false, nil
	}
	return true, Save(root, State{Confirmed: slot})
}

// Stage marks slot as the next one to try. The caller has already put a verified image there.
func Stage(root, slot string) error {
	s, _, err := Load(root)
	if err != nil {
		return err
	}
	return Save(root, State{Confirmed: s.Confirmed, Pending: slot, Tries: 0})
}

// ReadRollback returns the launcher's rollback note, or nil if there is none.
func ReadRollback(root string) (*Rollback, error) {
	b, err := os.ReadFile(RollbackPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Rollback
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// WriteRollback records a rollback for the payload to report.
func WriteRollback(root string, r Rollback) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(RollbackPath(root), b, 0o600)
}

// ClearRollback removes the note once it has been reported.
func ClearRollback(root string) error {
	err := os.Remove(RollbackPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
