// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RecordEpisodeStart attaches the first emitted tick's epoch to the existing
// handshake. It preserves dcctl-only fields, and never runs an oracle in live mode.
func RecordEpisodeStart(driver Sim, path string) {
	s, ok := driver.(*towerpulse)
	if !ok || s.episode != EpisodeNightShift {
		return
	}
	s.recordStart = func(start time.Time) error {
		handshakeWriteMu.Lock()
		defer handshakeWriteMu.Unlock()
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &doc); err != nil {
			return err
		}
		if doc == nil {
			return fmt.Errorf("record episode start: handshake is not an object")
		}
		start = start.UTC()
		windows := []struct {
			Role  string    `json:"role"`
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		}{
			{"INCIDENT", start.Add(45 * time.Minute), start.Add(95 * time.Minute)},
			{"NORMAL", start.Add(105 * time.Minute), start.Add(160 * time.Minute)},
		}
		doc["startedAt"], err = json.Marshal(start)
		if err != nil {
			return err
		}
		doc["windows"], err = json.Marshal(windows)
		if err != nil {
			return err
		}
		raw, err = json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".episode-start-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.Write(raw); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := os.Chmod(tmp.Name(), 0600); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), path)
	}
}
