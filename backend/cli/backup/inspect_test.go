// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/durability"
)

func TestFixtureSets(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"good", ""},
		{"tampered-hash", "object-store\" snapshot \"object-store.snap\": SHA-256 mismatch"},
		{"missing-file", "bucket\" snapshot \"bucket.snap\": open snapshot"},
		{"unknown-version", "unsupported manifest version 999"},
		{"wrong-class", "backup class \"ephemeral\" is not checkpoint-preserving"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Inspect(filepath.Join("testdata", tc.name))
			if tc.want == "" {
				if err != nil || m == nil || len(m.Entries) != 3 {
					t.Fatalf("Inspect = %v, %v", m, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) || m != nil {
				t.Fatalf("Inspect = %v, %v; want error containing %q and no result", m, err, tc.want)
			}
		})
	}
}

func TestInvalidManifest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Manifest)
		want   string
	}{
		{"size mismatch", func(m *Manifest) { *m.Entries[0].SizeBytes = 9 }, "size mismatch"},
		{"missing size", func(m *Manifest) { m.Entries[0].SizeBytes = nil }, "size_bytes"},
		{"negative size", func(m *Manifest) { *m.Entries[0].SizeBytes = -1 }, "size_bytes"},
		{"unknown class", func(m *Manifest) { m.Entries[0].Class = "future" }, "not checkpoint-preserving"},
		{"missing class", func(m *Manifest) { m.Entries[0].Class = "" }, "not checkpoint-preserving"},
		{"rebuildable", func(m *Manifest) { m.Entries[0].Class = durability.BackupRebuildable }, "not checkpoint-preserving"},
		{"unknown kind", func(m *Manifest) { m.Entries[0].Kind = "future" }, "unknown kind"},
		{"empty name", func(m *Manifest) { m.Entries[0].Name = "" }, "resource name"},
		{"duplicate name", func(m *Manifest) { m.Entries[1] = m.Entries[0] }, "resource name"},
		{"duplicate file", func(m *Manifest) { m.Entries[1].Snapshot = m.Entries[0].Snapshot }, "snapshot path"},
		{"traversal", func(m *Manifest) { m.Entries[0].Snapshot = "../outside" }, "snapshot path"},
		{"absolute", func(m *Manifest) { m.Entries[0].Snapshot = "/outside" }, "snapshot path"},
		{"windows path", func(m *Manifest) { m.Entries[0].Snapshot = `C:\outside` }, "snapshot path"},
		{"invalid hash", func(m *Manifest) { m.Entries[0].SHA256 = strings.Repeat("z", 64) }, "sha256"},
		{"short hash", func(m *Manifest) { m.Entries[0].SHA256 = "ab" }, "sha256"},
		{"empty inventory", func(m *Manifest) { m.Entries = nil }, "no entries"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Inspect(filepath.Join("testdata", "good"))
			if err != nil {
				t.Fatal(err)
			}
			tc.change(m)
			dir := copyGood(t)
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "manifest.json"), data)
			if got, err := Inspect(dir); err == nil || !strings.Contains(err.Error(), tc.want) || got != nil {
				t.Fatalf("Inspect = %v, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestStrictJSON(t *testing.T) {
	good, err := os.ReadFile(filepath.Join("testdata", "good", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		strings.Replace(string(good), `"version":`, `"future":true,"version":`, 1),
		strings.Replace(string(good), `"kind":`, `"future":true,"kind":`, 1),
		strings.Replace(string(good), `"version":`, `"version":999,"version":`, 1),
		strings.Replace(string(good), `"version":`, `"VERSION":999,"version":`, 1),
		strings.Replace(string(good), `"backup_class":`, `"backup_class":"ephemeral","backup_class":`, 1),
		string(good) + ` {}`,
		string(good) + ` garbage`,
		`null`, ``, `{`,
	} {
		dir := copyGood(t)
		write(t, filepath.Join(dir, "manifest.json"), []byte(input))
		if m, err := Inspect(dir); err == nil || m != nil {
			t.Fatalf("accepted %q: %v", input, m)
		}
	}
}

func TestEmptySnapshot(t *testing.T) {
	dir := copyGood(t)
	m, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	*m.Entries[0].SizeBytes = 0
	m.Entries[0].SHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "manifest.json"), data)
	write(t, filepath.Join(dir, "stream.snap"), nil)
	if _, err := Inspect(dir); err != nil {
		t.Fatalf("valid zero-byte snapshot: %v", err)
	}
}

func TestMissingManifestAndNonregularSnapshot(t *testing.T) {
	if _, err := Inspect(t.TempDir()); err == nil {
		t.Fatal("accepted missing manifest")
	}
	dir := copyGood(t)
	path := filepath.Join(dir, "stream.snap")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("directory snapshot: %v", err)
	}
}

func TestSymlinkEscape(t *testing.T) {
	dir := copyGood(t)
	outside := filepath.Join(t.TempDir(), "outside.snap")
	write(t, outside, []byte("snapshot"))
	path := filepath.Join(dir, "stream.snap")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Inspect(dir); err == nil {
		t.Fatal("accepted symlink outside backup set")
	}
}

func copyGood(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"manifest.json", "stream.snap", "bucket.snap", "object-store.snap"} {
		data, err := os.ReadFile(filepath.Join("testdata", "good", name))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, name), data)
	}
	return dir
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
