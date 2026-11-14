// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package backup exports instance streams and blobs and verifies local backup sets.
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/devicechain-io/dc-microservice/durability"
)

// ManifestVersion is the only supported on-disk schema version.
const ManifestVersion = 1

// Manifest is manifest.json in a backup-set directory. It describes only listed
// snapshots; verification does not prove inventory coverage or checkpoint consistency.
type Manifest struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Entry describes one opaque snapshot. Kind is stream, bucket, or object-store.
// Snapshot is a slash-separated path relative to the backup directory. SizeBytes
// is required (including for empty files); SHA256 is lowercase hexadecimal.
type Entry struct {
	Kind      string                 `json:"kind"`
	Name      string                 `json:"name"`
	Class     durability.BackupClass `json:"backup_class"`
	Snapshot  string                 `json:"snapshot"`
	SizeBytes *int64                 `json:"size_bytes"`
	SHA256    string                 `json:"sha256"`
}

// Inspect validates the entire manifest, then verifies every listed snapshot's
// size and SHA-256. All reads are confined by os.Root, including symlink resolution.
// It returns no successful result on a partial verification.
func Inspect(dir string) (*Manifest, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open backup set: %w", err)
	}
	defer root.Close()
	f, err := root.Open("manifest.json")
	if err != nil {
		return nil, fmt.Errorf("open manifest.json: %w", err)
	}
	defer f.Close()
	if err := uniqueKeys(json.NewDecoder(f)); err != nil {
		return nil, fmt.Errorf("decode manifest.json: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind manifest.json: %w", err)
	}
	var m Manifest
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest.json: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("manifest.json must contain exactly one JSON value")
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	for _, entry := range m.Entries {
		if err := verify(root, entry); err != nil {
			return nil, fmt.Errorf("%s %q snapshot %q: %w", entry.Kind, entry.Name, entry.Snapshot, err)
		}
	}
	return &m, nil
}

// encoding/json otherwise accepts duplicate keys with last-value-wins semantics,
// which could conceal an unsupported version or policy in an ambiguous manifest.
func uniqueKeys(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for dec.More() {
		if delim == '{' {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			name = strings.ToLower(name)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate or invalid JSON key %q", key)
			}
			seen[name] = true
		}
		if err := uniqueKeys(dec); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}

func (m Manifest) validate() error {
	if m.Version != ManifestVersion {
		return fmt.Errorf("unsupported manifest version %d", m.Version)
	}
	if len(m.Entries) == 0 {
		return fmt.Errorf("manifest has no entries")
	}
	names, files := map[string]bool{}, map[string]bool{}
	for i, e := range m.Entries {
		if e.Kind != "stream" && e.Kind != "bucket" && e.Kind != "object-store" {
			return fmt.Errorf("entry %d: unknown kind %q", i, e.Kind)
		}
		if strings.TrimSpace(e.Name) == "" || names[e.Kind+"\x00"+e.Name] {
			return fmt.Errorf("entry %d: missing or duplicate resource name %q", i, e.Name)
		}
		names[e.Kind+"\x00"+e.Name] = true
		if e.Class != durability.BackupRequired && e.Class != durability.BackupCheckpoint {
			return fmt.Errorf("entry %q: backup class %q is not checkpoint-preserving", e.Name, e.Class)
		}
		if !fs.ValidPath(e.Snapshot) || e.Snapshot == "." || strings.ContainsAny(e.Snapshot, "\\:") || e.Snapshot == "manifest.json" || files[e.Snapshot] {
			return fmt.Errorf("entry %q: invalid or duplicate snapshot path %q", e.Name, e.Snapshot)
		}
		files[e.Snapshot] = true
		if e.SizeBytes == nil || *e.SizeBytes < 0 {
			return fmt.Errorf("entry %q: size_bytes must be present and nonnegative", e.Name)
		}
		digest, err := hex.DecodeString(e.SHA256)
		if err != nil || len(digest) != sha256.Size || strings.ToLower(e.SHA256) != e.SHA256 {
			return fmt.Errorf("entry %q: sha256 must be 64 lowercase hexadecimal characters", e.Name)
		}
	}
	return nil
}

func verify(root *os.Root, e Entry) error {
	f, err := root.Open(e.Snapshot)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat snapshot: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("snapshot is not a regular file")
	}
	if info.Size() != *e.SizeBytes {
		return fmt.Errorf("size mismatch: got %d, want %d", info.Size(), *e.SizeBytes)
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}
	if n != *e.SizeBytes {
		return fmt.Errorf("size mismatch while reading: got %d, want %d", n, *e.SizeBytes)
	}
	if hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
		return fmt.Errorf("SHA-256 mismatch")
	}
	return nil
}
