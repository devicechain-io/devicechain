// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newFS(t *testing.T) Store {
	t.Helper()
	s, err := NewFilesystemStore(Config{Backend: BackendFilesystem, Directory: t.TempDir()}, "inst1")
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}
	return s
}

func TestFilesystemPutOpenStatDelete(t *testing.T) {
	ctx := context.Background()
	s := newFS(t)
	data := []byte("hello-logo-bytes")
	key := Key{Tenant: "acme", Purpose: "branding-logo", ID: "logo1.png"}

	ref, err := s.Put(ctx, key, bytes.NewReader(data), PutOptions{ContentType: "image/png"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.Backend != BackendFilesystem || ref.Key != "inst1/acme/branding-logo/logo1.png" {
		t.Fatalf("unexpected ref: %+v", ref)
	}

	// Stat: size + inferred content-type from the .png extension.
	info, err := s.Stat(ctx, ref)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size != int64(len(data)) {
		t.Fatalf("Stat size = %d, want %d", info.Size, len(data))
	}
	if info.ContentType != "image/png" {
		t.Fatalf("Stat content-type = %q, want image/png", info.ContentType)
	}

	// Open: bytes round-trip + same Info.
	rc, oi, err := s.Open(ctx, ref)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("Open bytes = %q, want %q", got, data)
	}
	if oi.ContentType != "image/png" || oi.Size != int64(len(data)) {
		t.Fatalf("Open info = %+v", oi)
	}

	// Delete removes it; a second Delete is idempotent.
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("idempotent Delete: %v", err)
	}
	if _, err := s.Stat(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat after delete = %v, want ErrNotFound", err)
	}
	if _, _, err := s.Open(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open after delete = %v, want ErrNotFound", err)
	}
}

func TestFilesystemContentTypeFallback(t *testing.T) {
	ctx := context.Background()
	s := newFS(t)
	ref, err := s.Put(ctx, Key{Tenant: "t", Purpose: "firmware", ID: "blob-no-ext"}, strings.NewReader("x"), PutOptions{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := s.Stat(ctx, ref)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.ContentType != defaultContentType {
		t.Fatalf("content-type = %q, want %q", info.ContentType, defaultContentType)
	}
}

func TestFilesystemMaxSizeEnforced(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewFilesystemStore(Config{Backend: BackendFilesystem, Directory: dir}, "inst1")
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}
	key := Key{Tenant: "t", Purpose: "branding-logo", ID: "big.png"}
	_, err = s.Put(ctx, key, bytes.NewReader(bytes.Repeat([]byte("a"), 100)), PutOptions{MaxSize: 10})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Put over limit = %v, want ErrTooLarge", err)
	}
	// The over-limit write must not have committed an object, and must not have
	// left a temp file behind.
	if _, err := s.Stat(ctx, Ref{Backend: BackendFilesystem, Key: "inst1/t/branding-logo/big.png"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("over-limit object must not exist: %v", err)
	}
	leftovers := 0
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, _ error) error {
		if fi != nil && !fi.IsDir() {
			leftovers++
		}
		return nil
	})
	if leftovers != 0 {
		t.Fatalf("expected no files after a rejected write, found %d", leftovers)
	}

	// Exactly at the limit is allowed.
	if _, err := s.Put(ctx, key, bytes.NewReader(bytes.Repeat([]byte("a"), 10)), PutOptions{MaxSize: 10}); err != nil {
		t.Fatalf("Put at limit: %v", err)
	}
}

func TestFilesystemURLUnsupported(t *testing.T) {
	s := newFS(t)
	if _, _, err := s.URL(context.Background(), Ref{Backend: BackendFilesystem, Key: "inst1/t/p/i"}, URLOptions{}); !errors.Is(err, ErrURLUnsupported) {
		t.Fatalf("URL = %v, want ErrURLUnsupported", err)
	}
}

func TestFilesystemRejectsForeignAndTamperedRef(t *testing.T) {
	ctx := context.Background()
	s := newFS(t)
	// A ref for a different backend must be refused, not silently read.
	if _, _, err := s.Open(ctx, Ref{Backend: "s3", Key: "inst1/t/p/i"}); err == nil {
		t.Fatal("Open with foreign backend ref must error")
	}
	// A tampered key with traversal must be refused by the containment check.
	for _, k := range []string{"../../../etc/passwd", "inst1/../../escape", ""} {
		if _, _, err := s.Open(ctx, Ref{Backend: BackendFilesystem, Key: k}); err == nil {
			t.Errorf("Open with traversal key %q must error", k)
		}
		if err := s.Delete(ctx, Ref{Backend: BackendFilesystem, Key: k}); err == nil {
			t.Errorf("Delete with traversal key %q must error", k)
		}
	}
}

func TestFilesystemPutOverwrites(t *testing.T) {
	ctx := context.Background()
	s := newFS(t)
	key := Key{Tenant: "t", Purpose: "branding-logo", ID: "logo.png"}
	if _, err := s.Put(ctx, key, strings.NewReader("first"), PutOptions{}); err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	ref, err := s.Put(ctx, key, strings.NewReader("second-longer"), PutOptions{})
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	rc, _, err := s.Open(ctx, ref)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "second-longer" {
		t.Fatalf("overwrite content = %q, want %q", got, "second-longer")
	}
}
