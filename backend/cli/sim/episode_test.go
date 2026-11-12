// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestEpisodeRecordRoundTripAndLegacy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	epoch := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	want := Record{Name: "night", ManifestId: "towerpulse", Episode: "night-shift", StartedAt: &epoch, Windows: []EpisodeWindow{{"INCIDENT", epoch.Add(45 * time.Minute), epoch.Add(95 * time.Minute)}, {"NORMAL", epoch.Add(105 * time.Minute), epoch.Add(160 * time.Minute)}}}
	if err := Save(&want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(want.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, &want) {
		t.Fatalf("record round trip: %+v != %+v", got, want)
	}
	path, err := RecordPath(want.Name)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"name": "night", "manifestId": "towerpulse"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	old, err := Load(want.Name)
	if err != nil {
		t.Fatal(err)
	}
	if old.Episode != "" || old.StartedAt != nil || len(old.Windows) != 0 {
		t.Fatalf("legacy record acquired episode fields: %+v", old)
	}
}
