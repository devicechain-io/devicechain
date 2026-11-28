// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/blob"
)

// sourceObjects is what every object-restore test backs up.
var sourceObjects = map[string]string{
	"sample/acme/ota/firmware-1": "firmware one",
	"sample/acme/ota/firmware-2": "firmware two",
	"sample/acme/branding/logo":  "logo",
}

// writeTree writes key->content files under root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for key, content := range files {
		p := filepath.Join(root, filepath.FromSlash(key))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// readTree returns every regular file under root/instance, keyed like an object.
func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// objectBackupSet backs up sourceObjects (and the seeded streams) into a set.
func objectBackupSet(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	writeTree(t, src, sourceObjects)
	seed, _ := restoreServer(t)
	seedRestoreStreams(t, seed)
	dir := filepath.Join(t.TempDir(), "set")
	export := func(ctx context.Context, w io.Writer) error {
		return ExportBlobs(ctx, blob.Config{Directory: src}, "sample", w)
	}
	if err := Create(context.Background(), seed, "sample", dir, export, nil); err != nil {
		t.Fatal(err)
	}
	return dir
}

// filesystemTarget is a target volume root holding an (empty) instance directory
// plus any files given.
func filesystemTarget(t *testing.T, files map[string]string) (string, *ObjectTarget, func(context.Context, io.Writer) error) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sample"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, files)
	target, err := NewObjectTarget(context.Background(), blob.Config{Directory: root})
	if err != nil {
		t.Fatal(err)
	}
	return root, target, func(ctx context.Context, w io.Writer) error {
		return ExportBlobs(ctx, blob.Config{Directory: root}, "sample", w)
	}
}

func prepared(t *testing.T, dir, instance string, foreign bool) *RestorePlan {
	t.Helper()
	p, err := PrepareRestore(dir, instance, foreign)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func sameTree(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("target holds %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("target holds %v, want %v", got, want)
		}
	}
}

func TestApplyObjectsIntoEmptyTarget(t *testing.T) {
	dir := objectBackupSet(t)
	root, target, current := filesystemTarget(t, nil)
	var progress bytes.Buffer
	res, err := prepared(t, dir, "sample", false).ApplyObjects(context.Background(), current, target, false, &progress)
	if err != nil {
		t.Fatal(err)
	}
	sameTree(t, readTree(t, root), sourceObjects)
	if res != (ObjectRestore{Written: 3}) {
		t.Fatalf("result %+v", res)
	}
	for _, line := range []string{"3 to write, 0 already match, 0 kept", "wrote sample/acme/ota/firmware-2", "(3 of 3)", "restored, all 3 backup objects match"} {
		if !strings.Contains(progress.String(), line) {
			t.Fatalf("progress lacks %q:\n%s", line, progress.String())
		}
	}
}

// Running the restore again, or finishing one that stopped part way, writes
// only what is missing and needs no overwrite permission.
func TestApplyObjectsIsIdempotent(t *testing.T) {
	dir := objectBackupSet(t)
	root, target, current := filesystemTarget(t, map[string]string{"sample/acme/ota/firmware-1": "firmware one"})
	plan := prepared(t, dir, "sample", false)
	res, err := plan.ApplyObjects(context.Background(), current, target, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res != (ObjectRestore{Written: 2, Matched: 1}) {
		t.Fatalf("partial target: %+v", res)
	}
	res, err = plan.ApplyObjects(context.Background(), current, target, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res != (ObjectRestore{Matched: 3}) {
		t.Fatalf("re-run: %+v", res)
	}
	sameTree(t, readTree(t, root), sourceObjects)
}

func TestApplyObjectsRefusesNonEmptyTarget(t *testing.T) {
	dir := objectBackupSet(t)
	for name, existing := range map[string]map[string]string{
		"divergent": {"sample/acme/ota/firmware-1": "newer firmware"},
		"extra":     {"sample/acme/ota/firmware-9": "uploaded after the backup"},
		"same size": {"sample/acme/branding/logo": "LOGO"},
	} {
		t.Run(name, func(t *testing.T) {
			root, target, current := filesystemTarget(t, existing)
			_, err := prepared(t, dir, "sample", false).ApplyObjects(context.Background(), current, target, false, nil)
			if err == nil || !strings.Contains(err.Error(), "--overwrite-target") {
				t.Fatalf("non-empty target accepted: %v", err)
			}
			// Nothing at all was written, not even the objects the target lacks.
			sameTree(t, readTree(t, root), existing)
		})
	}
}

func TestApplyObjectsOverwriteReplacesDifferingAndKeepsExtra(t *testing.T) {
	dir := objectBackupSet(t)
	root, target, current := filesystemTarget(t, map[string]string{
		"sample/acme/ota/firmware-1": "newer firmware",
		"sample/acme/ota/firmware-9": "uploaded after the backup",
	})
	res, err := prepared(t, dir, "sample", false).ApplyObjects(context.Background(), current, target, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"sample/acme/ota/firmware-9": "uploaded after the backup"}
	for k, v := range sourceObjects {
		want[k] = v
	}
	sameTree(t, readTree(t, root), want)
	if res != (ObjectRestore{Written: 3, Kept: 1}) {
		t.Fatalf("result %+v", res)
	}
}

func TestApplyObjectsRefusesForeignInstance(t *testing.T) {
	dir := objectBackupSet(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	target, err := NewObjectTarget(context.Background(), blob.Config{Directory: root})
	if err != nil {
		t.Fatal(err)
	}
	current := func(ctx context.Context, w io.Writer) error {
		return ExportBlobs(ctx, blob.Config{Directory: root}, "other", w)
	}
	_, err = prepared(t, dir, "other", true).ApplyObjects(context.Background(), current, target, true, nil)
	if err == nil || !strings.Contains(err.Error(), "another instance") {
		t.Fatalf("foreign object restore accepted: %v", err)
	}
	if got := readTree(t, root); len(got) != 0 {
		t.Fatalf("foreign restore wrote %v", got)
	}
}

func TestApplyObjectsRefusesBackendMismatch(t *testing.T) {
	dir := objectBackupSet(t)
	_, _, current := filesystemTarget(t, nil)
	wrote := false
	target := &ObjectTarget{backend: blob.BackendS3, write: func(context.Context, string, objectRecord, io.Reader, bool) error {
		wrote = true
		return nil
	}}
	_, err := prepared(t, dir, "sample", false).ApplyObjects(context.Background(), current, target, true, nil)
	if err == nil || !strings.Contains(err.Error(), "backend") || wrote {
		t.Fatalf("backend mismatch accepted (wrote %v): %v", wrote, err)
	}
}

// The set is checked when the plan is prepared AND as each object is written:
// bytes changed on disk in between are refused and never reach the target.
func TestApplyObjectsRechecksSnapshotBytes(t *testing.T) {
	dir := objectBackupSet(t)
	plan := prepared(t, dir, "sample", false)
	entry, _ := plan.objectEntry()
	snap := filepath.Join(dir, filepath.FromSlash(entry.Snapshot))
	raw, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(raw, []byte("firmware two"), []byte("FIRMWARE TWO"), 1)
	if bytes.Equal(raw, tampered) {
		t.Fatal("fixture object not found in the snapshot")
	}
	if err := os.WriteFile(snap, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	root, target, current := filesystemTarget(t, nil)
	_, err = plan.ApplyObjects(context.Background(), current, target, false, nil)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("tampered object accepted: %v", err)
	}
	for key := range readTree(t, root) {
		if key == "sample/acme/ota/firmware-2" || strings.Contains(key, ".put-") {
			t.Fatalf("tampered bytes or a staging file reached the target: %s", key)
		}
	}
}

// A missing instance directory is not an empty store: a mistyped mount looks
// exactly like one.
func TestApplyObjectsRefusesMissingInstanceDirectory(t *testing.T) {
	dir := objectBackupSet(t)
	root := t.TempDir()
	target, err := NewObjectTarget(context.Background(), blob.Config{Directory: root})
	if err != nil {
		t.Fatal(err)
	}
	current := func(ctx context.Context, w io.Writer) error {
		return ExportBlobs(ctx, blob.Config{Directory: root}, "sample", w)
	}
	_, err = prepared(t, dir, "sample", false).ApplyObjects(context.Background(), current, target, false, nil)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing instance directory read as empty: %v", err)
	}
	if got := readTree(t, root); len(got) != 0 {
		t.Fatalf("restore wrote %v", got)
	}
}

// The caller decides what may be replaced: only an object the target already
// held is written with replace, every other one as a create that cannot clobber.
func TestApplyObjectsReplacesOnlyWhatTheTargetHeld(t *testing.T) {
	dir := objectBackupSet(t)
	_, fsTarget, current := filesystemTarget(t, map[string]string{"sample/acme/ota/firmware-1": "newer firmware"})
	replaced := map[string]bool{}
	target := &ObjectTarget{backend: fsTarget.backend, write: func(ctx context.Context, key string, want objectRecord, src io.Reader, replace bool) error {
		replaced[key] = replace
		return fsTarget.write(ctx, key, want, src, replace)
	}}
	if _, err := prepared(t, dir, "sample", false).ApplyObjects(context.Background(), current, target, true, nil); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"sample/acme/ota/firmware-1": true, "sample/acme/ota/firmware-2": false, "sample/acme/branding/logo": false}
	if len(replaced) != len(want) {
		t.Fatalf("writes %v, want %v", replaced, want)
	}
	for k, v := range want {
		if r, ok := replaced[k]; !ok || r != v {
			t.Fatalf("writes %v, want %v", replaced, want)
		}
	}
}

// A write that reports success is not taken on trust: the target is read back.
func TestApplyObjectsReadsTheTargetBack(t *testing.T) {
	dir := objectBackupSet(t)
	_, fsTarget, current := filesystemTarget(t, nil)
	target := &ObjectTarget{backend: fsTarget.backend, write: func(_ context.Context, _ string, _ objectRecord, src io.Reader, _ bool) error {
		_, err := io.Copy(io.Discard, src)
		return err
	}}
	_, err := prepared(t, dir, "sample", false).ApplyObjects(context.Background(), current, target, false, nil)
	if err == nil || !strings.Contains(err.Error(), "does not match the backup") {
		t.Fatalf("a restore that wrote nothing reported success: %v", err)
	}
}

func record(content string) objectRecord {
	sum := sha256.Sum256([]byte(content))
	return objectRecord{Ref: "blob://filesystem/sample/a/b/c", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
}

func TestFilesystemWriterNeverClobbersANewKey(t *testing.T) {
	root, target, _ := filesystemTarget(t, map[string]string{"sample/a/b/c": "appeared meanwhile"})
	err := target.write(context.Background(), "sample/a/b/c", record("backup"), strings.NewReader("backup"), false)
	if err == nil {
		t.Fatal("create overwrote an existing object")
	}
	sameTree(t, readTree(t, root), map[string]string{"sample/a/b/c": "appeared meanwhile"})
	if err := target.write(context.Background(), "sample/a/b/c", record("backup"), strings.NewReader("backup"), true); err != nil {
		t.Fatal(err)
	}
	sameTree(t, readTree(t, root), map[string]string{"sample/a/b/c": "backup"})
}

func TestFilesystemWriterRefusesUnverifiedBytes(t *testing.T) {
	// Each refusal names what differs, so an operator can tell a truncated set
	// from altered bytes.
	for name, tc := range map[string]struct{ src, says string }{
		"short":   {"back", "size 4"},
		"long":    {"backup!", "size 7"},
		"changed": {"BACKUP", "SHA-256"},
	} {
		t.Run(name, func(t *testing.T) {
			root, target, _ := filesystemTarget(t, nil)
			err := target.write(context.Background(), "sample/a/b/c", record("backup"), strings.NewReader(tc.src), false)
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("unverified bytes: %v, want an error naming %q", err, tc.says)
			}
			if got := readTree(t, root); len(got) != 0 {
				t.Fatalf("left %v on the target", got)
			}
		})
	}
}

func TestS3WriterSendsChecksumAndNoClobberCondition(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	type put struct{ path, ifNoneMatch, checksum, contentType, body string }
	var puts []put
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPut {
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
		puts = append(puts, put{r.URL.Path, r.Header.Get("If-None-Match"), r.Header.Get("X-Amz-Checksum-Sha256"), r.Header.Get("Content-Type"), string(body)})
	}))
	defer srv.Close()
	target, err := NewObjectTarget(context.Background(), blob.Config{Backend: blob.BackendS3, Bucket: "bucket", Endpoint: srv.URL, UsePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	want := record("logo")
	want.ContentType = "image/png"
	sum := sha256.Sum256([]byte("logo"))
	checksum := base64.StdEncoding.EncodeToString(sum[:])
	if err := target.write(context.Background(), "sample/a/b/logo", want, strings.NewReader("logo"), false); err != nil {
		t.Fatal(err)
	}
	if err := target.write(context.Background(), "sample/a/b/logo", want, strings.NewReader("logo"), true); err != nil {
		t.Fatal(err)
	}
	if err := target.write(context.Background(), "sample/a/b/logo", want, strings.NewReader("LOGO"), true); err == nil {
		t.Fatal("unverified bytes uploaded")
	}
	if len(puts) != 2 {
		t.Fatalf("want 2 PUTs (the unverified one never sent), got %+v", puts)
	}
	for i, p := range puts {
		if p.path != "/bucket/sample/a/b/logo" || p.checksum != checksum || p.contentType != "image/png" || p.body != "logo" {
			t.Fatalf("PUT %d: %+v", i, p)
		}
	}
	if puts[0].ifNoneMatch != "*" || puts[1].ifNoneMatch != "" {
		t.Fatalf("create must be If-None-Match: *, replace unconditional: %+v", puts)
	}
}
