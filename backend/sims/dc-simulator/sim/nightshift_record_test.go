// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNightShiftRecordsFirstTickEpochAndWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handshake.json")
	if err := os.WriteFile(path, []byte(`{"episode":"night-shift","manifestId":"towerpulse","name":"night","simPassword":"preserve","future":{"keep":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	driver, err := NewSimEpisode("towerpulse", EpisodeNightShift, 1, Load{})
	if err != nil {
		t.Fatal(err)
	}
	s := driver.(*towerpulse)
	epoch := time.Date(2026, 11, 11, 0, 0, 0, 123456, time.FixedZone("offset", -5*3600))
	s.now = func() time.Time { return epoch }
	RecordEpisodeStart(s, path)
	if err := s.Tick(context.Background(), &Runtime{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Episode, Name, SimPassword string
		Future                     map[string]bool
		StartedAt                  time.Time
		Windows                    []struct {
			Role       string
			Start, End time.Time
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.StartedAt.Equal(epoch) || !s.started.Equal(epoch) || doc.SimPassword != "preserve" || doc.Name != "night" || !doc.Future["keep"] {
		t.Fatalf("epoch/fields: %+v", doc)
	}
	if len(doc.Windows) != 2 {
		t.Fatalf("windows: %+v", doc.Windows)
	}
	for i, w := range doc.Windows {
		offsets := [][2]time.Duration{{45 * time.Minute, 95 * time.Minute}, {105 * time.Minute, 160 * time.Minute}}
		roles := []string{"INCIDENT", "NORMAL"}
		if w.Role != roles[i] || !w.Start.Equal(epoch.Add(offsets[i][0])) || !w.End.Equal(epoch.Add(offsets[i][1])) {
			t.Errorf("window: %+v", w)
		}
	}
	// A later tick keeps the first epoch, including across stop/resume.
	s.now = func() time.Time { return epoch.Add(time.Minute) }
	if err := s.Tick(context.Background(), &Runtime{}); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(raw) {
		t.Fatal("later tick rewrote the recorded epoch")
	}
}
func TestNightShiftRefusesToEmitWhenStartCannotBeRecorded(t *testing.T) {
	driver, err := NewSimEpisode("towerpulse", EpisodeNightShift, 1, Load{})
	if err != nil {
		t.Fatal(err)
	}
	s := driver.(*towerpulse)
	RecordEpisodeStart(s, filepath.Join(t.TempDir(), "missing.json"))
	if err := s.Tick(context.Background(), &Runtime{}); err == nil {
		t.Fatal("missing record silently accepted")
	}
	if !s.started.IsZero() {
		t.Fatal("failed recording advanced the start")
	}
}
