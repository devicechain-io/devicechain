// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"context"
	"testing"
)

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
	// GCS validates as an identifier (built in a later slice).
	if err := (Config{Backend: BackendGCS}).Validate(); err != nil {
		t.Fatalf("declared backend gcs must validate: %v", err)
	}
	// S3 requires a bucket, and a region OR an endpoint.
	if err := (Config{Backend: BackendS3}).Validate(); err == nil {
		t.Fatal("s3 backend with no bucket must be rejected")
	}
	if err := (Config{Backend: BackendS3, Bucket: "b"}).Validate(); err == nil {
		t.Fatal("s3 backend with no region and no endpoint must be rejected")
	}
	if err := (Config{Backend: BackendS3, Bucket: "b", Region: "us-east-1"}).Validate(); err != nil {
		t.Fatalf("s3 backend with bucket+region must validate: %v", err)
	}
	if err := (Config{Backend: BackendS3, Bucket: "b", Endpoint: "http://minio:9000"}).Validate(); err != nil {
		t.Fatalf("s3 backend with bucket+endpoint must validate: %v", err)
	}
	// Unknown backend fails closed.
	if err := (Config{Backend: "azure"}).Validate(); err == nil {
		t.Fatal("unknown backend must be rejected")
	}
}

func TestNewFailsClosed(t *testing.T) {
	ctx := context.Background()
	// gcs validates as an identifier but is not built in this binary — fail closed.
	if _, err := New(ctx, Config{Backend: BackendGCS}, "inst1"); err == nil {
		t.Fatal("New(gcs) must fail closed for an unbuilt backend")
	}
	// s3 is built, but an under-specified s3 config (no bucket) fails at Validate.
	if _, err := New(ctx, Config{Backend: BackendS3}, "inst1"); err == nil {
		t.Fatal("New(s3) with no bucket must error")
	}
	// Unknown backend fails at Validate inside New.
	if _, err := New(ctx, Config{Backend: "azure"}, "inst1"); err == nil {
		t.Fatal("New with unknown backend must error")
	}
	// A bad instance id fails closed even for a valid backend.
	if _, err := New(ctx, Config{Backend: BackendFilesystem, Directory: t.TempDir()}, "bad/id"); err == nil {
		t.Fatal("New with an invalid instance id must error")
	}
}
