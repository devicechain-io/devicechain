// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// filesystemStore is the default backend (ADR-058 §2): objects live as files under
// a mounted volume/PVC, laid out by the full object key. It has no public path —
// reads are served through the caller's authorizing proxy (Open); URL is
// unsupported. Content-Type is not persisted; Info infers it from the key's
// extension (a consumer that needs an exact type encodes it in the Key.ID
// extension — e.g. branding stores logos as "{uuid}.png").
type filesystemStore struct {
	root       string
	instanceID string
}

// defaultContentType is reported for an object whose key extension maps to no known
// MIME type. A generic asset served with this type downloads rather than executes.
const defaultContentType = "application/octet-stream"

// tempPrefix is the in-flight temp-file prefix for the atomic write. It starts with
// a dot, which validateSegment forbids as a leading char in an object id, so a temp
// file can never collide with (or be dereferenced as) a real object.
const tempPrefix = ".put-"

func init() {
	// Register the asset MIME types the branding read proxy relies on so the
	// inferred Content-Type is deterministic regardless of the container image's
	// /etc/mime.types (mime.TypeByExtension otherwise consults it — non-hermetic).
	// A static type here is a programming error caught by tests, so errors are
	// ignored.
	_ = mime.AddExtensionType(".png", "image/png")
	_ = mime.AddExtensionType(".jpg", "image/jpeg")
	_ = mime.AddExtensionType(".jpeg", "image/jpeg")
	_ = mime.AddExtensionType(".webp", "image/webp")
	_ = mime.AddExtensionType(".svg", "image/svg+xml")
}

// NewFilesystemStore builds a filesystem-backed Store rooted at cfg.Directory and
// prefixing every key with instanceID. The root is created if absent. It is
// exported for direct construction and tests; production wiring goes through New.
func NewFilesystemStore(cfg Config, instanceID string) (Store, error) {
	if strings.TrimSpace(cfg.Directory) == "" {
		return nil, fmt.Errorf("blob: filesystem backend requires a directory")
	}
	if err := validateSegment("instanceId", instanceID); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(filepath.Clean(cfg.Directory))
	if err != nil {
		return nil, fmt.Errorf("blob: resolving filesystem root: %w", err)
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("blob: creating filesystem root %q: %w", root, err)
	}
	return &filesystemStore{root: root, instanceID: instanceID}, nil
}

// objectPath maps a full object key to an absolute filesystem path and verifies,
// defense-in-depth, that it stays within the store root. buildKey/validateSegment
// already guarantee no ".." or "/" within a segment, so this can only fail on a
// tampered Ref handed straight to Open/Delete/Stat.
func (s *filesystemStore) objectPath(fullKey string) (string, error) {
	if strings.TrimSpace(fullKey) == "" {
		return "", fmt.Errorf("blob: empty object key")
	}
	if strings.HasSuffix(fullKey, reservedIDSuffix) {
		return "", fmt.Errorf("blob: object key uses the reserved %q suffix", reservedIDSuffix)
	}
	p := filepath.Join(s.root, filepath.FromSlash(fullKey))
	rel, err := filepath.Rel(s.root, p)
	// Reject anything that is not strictly BELOW the root: an escape ("..", "../…")
	// AND the root itself ("."), which a tampered Ref like "blob://filesystem/."
	// would otherwise resolve to. A regular-file guard on read/delete (below) is the
	// second half of this backstop — objectPath keeps a key inside the root; the
	// caller must still confirm the target is an object, not a directory.
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("blob: object key escapes the store root")
	}
	return p, nil
}

// pathForRef validates the Ref belongs to this backend and this instance, then
// resolves its path. The instance-prefix check is the ADR-048 isolation invariant
// made load-bearing: a tampered Ref naming another instance's key
// ("otherInstance/…") is refused rather than served, which matters the moment two
// instances share a store root or (a later slice) an S3 bucket.
func (s *filesystemStore) pathForRef(ref Ref) (string, error) {
	if ref.Backend != BackendFilesystem {
		return "", fmt.Errorf("blob: ref backend %q is not %q", ref.Backend, BackendFilesystem)
	}
	if !strings.HasPrefix(ref.Key, s.instanceID+"/") {
		return "", fmt.Errorf("blob: ref key is not within instance %q", s.instanceID)
	}
	return s.objectPath(ref.Key)
}

func (s *filesystemStore) Put(ctx context.Context, key Key, r io.Reader, opts PutOptions) (Ref, error) {
	fullKey, err := buildKey(s.instanceID, key)
	if err != nil {
		return Ref{}, err
	}
	path, err := s.objectPath(fullKey)
	if err != nil {
		return Ref{}, err
	}
	// The filesystem backend does not persist Content-Type — it infers it from the
	// id extension on read. A declared type that CONTRADICTS the extension would
	// therefore serve one type here and (after a migration) the declared type on a
	// cloud backend, so reject the contradiction up front. A declared type with no
	// inferable extension is allowed (nothing to contradict).
	if err := checkContentTypeMatchesExt(key.ID, opts.ContentType); err != nil {
		return Ref{}, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Ref{}, fmt.Errorf("blob: creating object dir: %w", err)
	}
	// Write to a temp file in the SAME directory so the final rename is atomic on
	// the same filesystem; clean it up on any failure so a partial/over-limit write
	// never commits.
	tmp, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return Ref{}, fmt.Errorf("blob: creating temp object: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		tmp.Close()
		if !committed {
			os.Remove(tmpName)
		}
	}()

	// Enforce MaxSize by reading one byte past the limit and rejecting if reached,
	// so an over-limit stream cannot fill the volume before we notice.
	var src io.Reader = r
	if opts.MaxSize > 0 {
		// Guard MaxSize+1 against int64 overflow (a MaxInt64 sentinel would wrap
		// negative and make LimitReader read zero bytes → a silent empty object).
		lim := opts.MaxSize
		if lim > math.MaxInt64-1 {
			lim = math.MaxInt64 - 1
		}
		src = io.LimitReader(r, lim+1)
	}
	n, err := io.Copy(tmp, src)
	if err != nil {
		return Ref{}, fmt.Errorf("blob: writing object: %w", err)
	}
	if opts.MaxSize > 0 && n > opts.MaxSize {
		return Ref{}, ErrTooLarge
	}
	if err := tmp.Sync(); err != nil {
		return Ref{}, fmt.Errorf("blob: syncing object: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Ref{}, fmt.Errorf("blob: closing object: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return Ref{}, fmt.Errorf("blob: committing object: %w", err)
	}
	committed = true
	return Ref{Backend: BackendFilesystem, Key: fullKey}, nil
}

func (s *filesystemStore) Open(ctx context.Context, ref Ref) (io.ReadCloser, Info, error) {
	path, err := s.pathForRef(ref)
	if err != nil {
		return nil, Info{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, Info{}, ErrNotFound
		}
		return nil, Info{}, fmt.Errorf("blob: opening object: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, Info{}, fmt.Errorf("blob: stating object: %w", err)
	}
	// A key that resolves to a directory (a truncated/tampered ref naming a prefix
	// like "inst1/acme") is not an object: treat it as absent rather than handing
	// back a reader that fails mid-response after the proxy has written headers.
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, Info{}, ErrNotFound
	}
	return f, infoFor(ref.Key, fi), nil
}

func (s *filesystemStore) Stat(ctx context.Context, ref Ref) (Info, error) {
	path, err := s.pathForRef(ref)
	if err != nil {
		return Info{}, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Info{}, ErrNotFound
		}
		return Info{}, fmt.Errorf("blob: stating object: %w", err)
	}
	// A directory-resolving key is not an object (see Open).
	if !fi.Mode().IsRegular() {
		return Info{}, ErrNotFound
	}
	return infoFor(ref.Key, fi), nil
}

// URL is unsupported for the filesystem backend — it has no public/presigned path.
// The caller serves bytes through the authorizing proxy (Open) instead.
func (s *filesystemStore) URL(ctx context.Context, ref Ref, opts URLOptions) (string, time.Time, error) {
	return "", time.Time{}, ErrURLUnsupported
}

func (s *filesystemStore) Delete(ctx context.Context, ref Ref) error {
	path, err := s.pathForRef(ref)
	if err != nil {
		return err
	}
	// Refuse to remove anything that is not a regular file: a truncated/tampered ref
	// naming a directory must not delete a tenant/instance directory. Lstat (not
	// Stat) so a symlink is judged as a symlink, never followed. Absent is a no-op
	// (idempotent Delete).
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("blob: stating object for delete: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("blob: refusing to delete non-regular path for ref %q", ref.Key)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blob: deleting object: %w", err)
	}
	return nil
}

// checkContentTypeMatchesExt fails closed when a declared Content-Type contradicts
// the type the id's extension infers, so the filesystem backend (which serves the
// inferred type) and a cloud backend (which serves the declared type) cannot end
// up serving the same object as two different types. It compares base media types
// only (ignoring parameters like charset). An empty declared type, or an extension
// that infers nothing, is not a contradiction and is allowed.
func checkContentTypeMatchesExt(id, declared string) error {
	if declared == "" {
		return nil
	}
	inferred := mime.TypeByExtension(filepath.Ext(id))
	if inferred == "" {
		return nil
	}
	declaredBase, _, derr := mime.ParseMediaType(declared)
	inferredBase, _, ierr := mime.ParseMediaType(inferred)
	if derr != nil || ierr != nil {
		return nil // a malformed declared type is not treated as a contradiction here
	}
	if !strings.EqualFold(declaredBase, inferredBase) {
		return fmt.Errorf("blob: content type %q contradicts the %q extension type %q", declared, filepath.Ext(id), inferredBase)
	}
	return nil
}

// infoFor builds Info from a stat result, inferring Content-Type from the key's
// extension (the filesystem backend persists none of its own).
func infoFor(key string, fi os.FileInfo) Info {
	ct := mime.TypeByExtension(filepath.Ext(key))
	if ct == "" {
		ct = defaultContentType
	}
	return Info{
		Key:         key,
		Size:        fi.Size(),
		ContentType: ct,
		ModTime:     fi.ModTime(),
	}
}
