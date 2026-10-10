// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const stateVersion = 1

// attemptState is what a device must remember to resume an update after losing power: which
// attempt, how far the report sequence and the download got, and the one report that has been
// decided but not yet confirmed sent.
type attemptState struct {
	AttemptID      string `json:"attemptId"`
	AssignmentID   string `json:"assignmentId"`
	Digest         string `json:"digest"`
	Version        string `json:"version"`
	Size           int64  `json:"size"`
	RequiresReboot bool   `json:"requiresReboot"`

	// Stage is the stage of the last report GENERATED, which may still be Pending.
	Stage     string `json:"stage"`
	Seq       uint64 `json:"seq"`
	BytesHave int64  `json:"bytesHave"`
	// HashState is the SHA-256 state after BytesHave bytes, so a restart verifies the whole image
	// without having kept it.
	HashState []byte `json:"hashState,omitempty"`
	// Booted is set once the reboot for this attempt has happened, so a restart between the boot
	// and the confirmation does not reboot twice.
	Booted bool `json:"booted,omitempty"`

	// Pending is the exact bytes of a report persisted before it was sent. It is resent verbatim
	// (same seq) until a send succeeds: at-least-once, with the platform's duplicate handling
	// making the repeats harmless.
	Pending json.RawMessage `json:"pending,omitempty"`
	// LastSent is the last report whose send succeeded; it becomes state.Previous when the next
	// assignment replaces this attempt.
	LastSent json.RawMessage `json:"lastSent,omitempty"`
}

// state is the device-local file. BootID/RunningVersion/SlotVersion model the device's boot and
// its two image slots; they outlive any one attempt.
type state struct {
	V              int           `json:"v"`
	Component      string        `json:"component"`
	BootSeq        int           `json:"bootSeq"`
	BootID         string        `json:"bootId"`
	RunningVersion string        `json:"runningVersion"`
	SlotVersion    string        `json:"slotVersion,omitempty"`
	Attempt        *attemptState `json:"attempt,omitempty"`
	// Previous is the last report of the attempt before the current one, kept so a test can
	// replay a stale attempt's report.
	Previous json.RawMessage `json:"previous,omitempty"`
}

// ErrCorruptState is returned when the state file exists but cannot be trusted. The updater
// refuses to start over it: silently beginning from scratch would forget an attempt that is
// half-applied. To reset the simulated device deliberately, delete the state file; the error text
// says so.
var ErrCorruptState = errors.New("updater: state file is corrupt (delete the state file to reset the simulated device)")

func loadState(path string) (state, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state{}, false, nil
	}
	if err != nil {
		return state{}, false, fmt.Errorf("updater: read state %s: %w", path, err)
	}
	var st state
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		return state{}, false, fmt.Errorf("%w: %s: %v", ErrCorruptState, path, err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return state{}, false, fmt.Errorf("%w: %s: trailing data", ErrCorruptState, path)
	}
	if st.V != stateVersion {
		return state{}, false, fmt.Errorf("%w: %s: version %d, want %d", ErrCorruptState, path, st.V, stateVersion)
	}
	if st.Component == "" || st.BootID == "" || st.BootSeq < 1 {
		return state{}, false, fmt.Errorf("%w: %s: missing component or boot identity", ErrCorruptState, path)
	}
	if a := st.Attempt; a != nil {
		if a.AttemptID == "" || a.AssignmentID == "" || a.Digest == "" || a.Size <= 0 ||
			a.BytesHave < 0 || a.BytesHave > a.Size || (a.BytesHave > 0 && len(a.HashState) == 0) {
			return state{}, false, fmt.Errorf("%w: %s: inconsistent attempt", ErrCorruptState, path)
		}
	}
	return st, true, nil
}

// saveState writes the file the way a device must: to a sibling temp file, fsynced, then renamed
// over the real one, so a crash leaves either the old state or the new, never a torn mix.
func saveState(path string, st state) error {
	st.V = stateVersion
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("updater: write state: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return fmt.Errorf("updater: write state: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("updater: sync state: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("updater: close state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("updater: commit state: %w", err)
	}
	// Make the rename itself durable where the platform allows it. Windows cannot open a
	// directory for sync; a failure there is not a correctness problem for a simulator.
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
