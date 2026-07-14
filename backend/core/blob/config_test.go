// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import "testing"

func TestDefaultConfigBackend(t *testing.T) {
	if DefaultConfig().withDefaults().Backend != BackendFilesystem {
		t.Fatal("default backend must be filesystem")
	}
}

func TestConfigValidate(t *testing.T) {
	// Filesystem requires a directory.
	if err := (Config{Backend: BackendFilesystem}).Validate(); err == nil {
		t.Fatal("filesystem backend with no directory must be rejected")
	}
	if err := (Config{Backend: BackendFilesystem, Directory: "/data/blob"}).Validate(); err != nil {
		t.Fatalf("filesystem backend with a directory must validate: %v", err)
	}
	// An empty backend defaults to filesystem, so it still requires a directory.
	if err := (Config{Directory: "/data/blob"}).Validate(); err != nil {
		t.Fatalf("empty backend + directory must validate as filesystem: %v", err)
	}
	// Declared cloud backends pass identifier validation (built in a later slice).
	for _, b := range []string{BackendS3, BackendGCS} {
		if err := (Config{Backend: b}).Validate(); err != nil {
			t.Fatalf("declared backend %q must validate: %v", b, err)
		}
	}
	// Unknown backend fails closed.
	if err := (Config{Backend: "azure"}).Validate(); err == nil {
		t.Fatal("unknown backend must be rejected")
	}
}

func TestNewFailsClosedOnUnbuiltBackend(t *testing.T) {
	// s3/gcs validate but are not built in this binary — New must fail closed.
	for _, b := range []string{BackendS3, BackendGCS} {
		if _, err := New(Config{Backend: b}, "inst1"); err == nil {
			t.Fatalf("New(%q) must fail closed for an unbuilt backend", b)
		}
	}
	// Unknown backend fails at Validate inside New.
	if _, err := New(Config{Backend: "azure"}, "inst1"); err == nil {
		t.Fatal("New with unknown backend must error")
	}
	// A bad instance id fails closed even for a valid backend.
	if _, err := New(Config{Backend: BackendFilesystem, Directory: t.TempDir()}, "bad/id"); err == nil {
		t.Fatal("New with an invalid instance id must error")
	}
}
