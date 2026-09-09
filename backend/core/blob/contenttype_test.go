// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"context"
	"mime"
	"slices"
	"strings"
	"testing"
)

// The extensions below are ones the PROCESS MIME table resolves to a type on at
// least some hosts, and that this package's closed table deliberately does not
// name. They are the input class every other test in this package misses: the rest
// use one of the five asset extensions or an id with no extension at all, so a
// regression to mime.TypeByExtension is invisible to them.
//
// They are split by WHERE the process table gets its answer, and that difference is
// the whole point of the closed table.

// builtinKnownExtensions are in Go's own builtin table (mime/type.go), so
// mime.TypeByExtension answers for them on every host, the published image
// included. These rows can fail anywhere, and they run unconditionally.
//
// ".bin" is carried for coverage but discriminates nothing on its own: the process
// table calls it "application/octet-stream", which is both the type these Puts
// declare and defaultContentType, so it agrees either way.
var builtinKnownExtensions = []string{".zip", ".gz", ".bin"}

// systemDBKnownExtensions come only from the host's system MIME database. A Debian
// dev/CI host has one; cgr.dev/chainguard/static, the base image we publish, does
// not — so before the closed table these resolved to a type in dev and to nothing
// in production. Reverting the closed table is therefore caught by these rows ONLY
// where a system MIME database exists, which is the property under test, not a
// shortcoming of the test. requireSystemMIMEDatabase asserts that precondition out
// loud rather than letting it lapse into a silent pass.
var systemDBKnownExtensions = []string{".img", ".tar", ".iso"}

// systemKnownExtensions is both groups, for the row loops that exercise every one.
var systemKnownExtensions = slices.Concat(builtinKnownExtensions, systemDBKnownExtensions)

// mimeDatabaseFiles are the files Go's unix MIME init consults, in the order it
// consults them. Only for the failure message — the check itself asks the process
// table, not the filesystem, because the table is what the code under test reads.
// Worth knowing when reproducing the absent case: the first globs2 that OPENS wins
// and ends the search, so an EMPTY globs2 still suppresses the /etc/mime.types
// fallback, and masking /etc/mime.types alone changes nothing on a host with globs2.
var mimeDatabaseFiles = []string{
	"/usr/local/share/mime/globs2", "/usr/share/mime/globs2",
	"/etc/mime.types", "/etc/apache2/mime.types", "/etc/apache/mime.types",
	"/etc/httpd/conf/mime.types",
}

// requireSystemMIMEDatabase fails the calling test when this host's process MIME
// table cannot answer for the systemDBKnownExtensions rows.
//
// Those rows are the half of this gate that can only fail where a system MIME
// database exists: a revert to mime.TypeByExtension answers "" for them on a host
// without one, which is what the correct code answers too, so the rows agree with
// the regression and pass. Without this check that loss is silent — "tested and
// fine" and "could not test this at all" become the same green.
//
// It is deliberately an Errorf and not a t.Skip. A skipped test is a green tick,
// which is precisely the ambiguity being removed here. The builtinKnownExtensions
// rows keep their kill on any host, so the calling test carries on after this
// reports rather than stopping.
func requireSystemMIMEDatabase(t *testing.T) {
	t.Helper()
	var unanswered []string
	for _, ext := range systemDBKnownExtensions {
		if mime.TypeByExtension(ext) == "" {
			unanswered = append(unanswered, ext)
		}
	}
	if len(unanswered) == 0 {
		return
	}
	t.Errorf("mime.TypeByExtension answers nothing for %s, so this host has no system MIME "+
		"database entry for them (Go consults %s, in that order, and takes the first globs2 "+
		"that opens). Those rows of this gate can no longer detect a revert to "+
		"mime.TypeByExtension — they pass whether the closed table is there or not, so a green "+
		"run would mean \"could not test this\" rather than \"tested and fine\". Either the "+
		"runner image changed and needs a system MIME database, or these rows need revisiting. "+
		"Do not silence this with t.Skip: a skip is a green tick, which is the ambiguity this "+
		"check exists to remove. The %s rows come from Go's builtin table and still hold.",
		strings.Join(unanswered, ", "), strings.Join(mimeDatabaseFiles, ", "),
		strings.Join(builtinKnownExtensions, ", "))
}

func TestInferContentTypeIsClosed(t *testing.T) {
	requireSystemMIMEDatabase(t)
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
	requireSystemMIMEDatabase(t)
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
	requireSystemMIMEDatabase(t)
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
