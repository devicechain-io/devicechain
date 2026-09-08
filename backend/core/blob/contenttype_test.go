// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"context"
	"mime"
	"strings"
	"testing"
)

// systemKnownExtensions are extensions the PROCESS MIME table resolves to a type on
// at least some hosts, and that this package's closed table deliberately does not
// name. They are the input class every other test in this package misses: the rest
// use one of the five asset extensions or an id with no extension at all, so a
// regression to mime.TypeByExtension is invisible to them.
//
// The two groups differ in WHERE the process table gets its answer, and that
// difference is the whole point of the closed table:
//
//   - ".zip", ".gz" and ".bin" are in Go's own builtin table, so mime.TypeByExtension
//     answers for them on every host, published image included.
//   - ".img", ".tar" and ".iso" come only from the host's system MIME database (the
//     first openable of /usr/local/share/mime/globs2 and /usr/share/mime/globs2,
//     falling back to /etc/mime.types and the apache/httpd files only if neither
//     opens). A Debian dev/CI host has one; cgr.dev/chainguard/static, the base image
//     we publish, does not — so before the closed table these resolved to a type in
//     dev and to nothing in production.
//
// Only the first group can fail on every host. Reverting the closed table is caught
// by the second group ONLY where a system MIME database exists — which is the
// property under test, not a shortcoming of the test.
//
// ".bin" is carried for coverage but discriminates nothing on its own: the process
// table calls it "application/octet-stream", which is both the type these Puts
// declare and defaultContentType, so it agrees either way.
var systemKnownExtensions = []string{".zip", ".gz", ".img", ".tar", ".iso", ".bin"}

func TestInferContentTypeIsClosed(t *testing.T) {
	for ext, want := range map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
		".webp": "image/webp", ".svg": "image/svg+xml",
	} {
		if got := inferContentType(ext); got != want {
			t.Errorf("inferContentType(%q) = %q, want %q", ext, got, want)
		}
		// The table is matched case-insensitively, so an uppercased id agrees.
		if got := inferContentType(strings.ToUpper(ext)); got != want {
			t.Errorf("inferContentType(%q) = %q, want %q", strings.ToUpper(ext), got, want)
		}
	}
	// Nothing outside the table infers anything, whatever the process MIME table
	// says about it on this host.
	for _, ext := range systemKnownExtensions {
		if got := inferContentType(ext); got != "" {
			t.Errorf("inferContentType(%q) = %q, want %q (the table is closed)", ext, got, "")
		}
		t.Logf("process table: mime.TypeByExtension(%q) = %q", ext, mime.TypeByExtension(ext))
	}
	if got := inferContentType(""); got != "" {
		t.Errorf("inferContentType(\"\") = %q, want empty", got)
	}
}

// TestPutAcceptsDeclaredTypeForSystemKnownExtension is the behavioural gate. A Put
// whose id carries an extension outside the closed table must be accepted with any
// declared type, on every backend and on every host — the write decision must not
// depend on what the base image happens to ship in its MIME database.
func TestPutAcceptsDeclaredTypeForSystemKnownExtension(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t)
	s3s := newS3(&fakeS3{})

	for _, ext := range systemKnownExtensions {
		id := "fw-1.2.3" + ext
		if _, err := fs.Put(ctx, Key{Tenant: "t", Purpose: "firmware", ID: id},
			strings.NewReader("x"), PutOptions{ContentType: "application/octet-stream"}); err != nil {
			t.Errorf("filesystem Put(%q, declared application/octet-stream) = %v, want accepted", id, err)
		}
		if _, err := s3s.Put(ctx, Key{Tenant: "t", Purpose: "firmware", ID: id},
			strings.NewReader("x"), PutOptions{ContentType: "application/octet-stream"}); err != nil {
			t.Errorf("s3 Put(%q, declared application/octet-stream) = %v, want accepted", id, err)
		}
	}
}

// TestStatReportsDefaultTypeForSystemKnownExtension is the read half: an object
// outside the closed table is reported as the generic default, not as whatever the
// host's MIME database calls that extension.
func TestStatReportsDefaultTypeForSystemKnownExtension(t *testing.T) {
	ctx := context.Background()
	s := newFS(t)
	for _, ext := range systemKnownExtensions {
		id := "fw-1.2.3" + ext
		ref, err := s.Put(ctx, Key{Tenant: "t", Purpose: "firmware", ID: id},
			strings.NewReader("x"), PutOptions{})
		if err != nil {
			t.Fatalf("Put(%q): %v", id, err)
		}
		info, err := s.Stat(ctx, ref)
		if err != nil {
			t.Fatalf("Stat(%q): %v", id, err)
		}
		if info.ContentType != defaultContentType {
			t.Errorf("Stat(%q).ContentType = %q, want %q", id, info.ContentType, defaultContentType)
		}
	}
}

// TestS3PutRejectsContradictoryContentType pins the decision that the extension/type
// contradiction check runs on BOTH backends, so an object cannot acquire a
// contradictory type by being written through the backend that skips the check.
func TestS3PutRejectsContradictoryContentType(t *testing.T) {
	ctx := context.Background()
	f := &fakeS3{}
	s := newS3(f)

	if _, err := s.Put(ctx, Key{Tenant: "t", Purpose: "branding-logo", ID: "logo.svg"},
		strings.NewReader("x"), PutOptions{ContentType: "image/png"}); err == nil {
		t.Fatal("s3 Put with a content type contradicting the .svg extension must error")
	}
	// Refused BEFORE the object is written, not after.
	if f.putIn != nil {
		t.Fatal("s3 Put must not call PutObject when the content type is refused")
	}
	// A matching declared type is still accepted.
	if _, err := s.Put(ctx, Key{Tenant: "t", Purpose: "branding-logo", ID: "logo.svg"},
		strings.NewReader("x"), PutOptions{ContentType: "image/svg+xml"}); err != nil {
		t.Fatalf("s3 Put with a matching content type: %v", err)
	}
}
