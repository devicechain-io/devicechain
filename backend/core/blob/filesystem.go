// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// metaSuffix would be a reserved sidecar extension — the filesystem backend does
// NOT use one (Content-Type is inferred), but the suffix is reserved so a future
// metadata sidecar cannot collide with a live object id. Documented for callers.
const reservedSuffix = ".dcmeta"

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
	if strings.HasSuffix(fullKey, reservedSuffix) {
		return "", fmt.Errorf("blob: object key uses the reserved %q suffix", reservedSuffix)
	}
	p := filepath.Join(s.root, filepath.FromSlash(fullKey))
	rel, err := filepath.Rel(s.root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("blob: object key escapes the store root")
	}
	return p, nil
}

// pathForRef validates the Ref belongs to this backend and resolves its path.
func (s *filesystemStore) pathForRef(ref Ref) (string, error) {
	if ref.Backend != BackendFilesystem {
		return "", fmt.Errorf("blob: ref backend %q is not %q", ref.Backend, BackendFilesystem)
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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Ref{}, fmt.Errorf("blob: creating object dir: %w", err)
	}
	// Write to a temp file in the SAME directory so the final rename is atomic on
	// the same filesystem; clean it up on any failure so a partial/over-limit write
	// never commits.
	tmp, err := os.CreateTemp(dir, ".put-*")
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
		src = io.LimitReader(r, opts.MaxSize+1)
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
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blob: deleting object: %w", err)
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
