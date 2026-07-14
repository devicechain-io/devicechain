// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package blob is the DeviceChain object/asset-store abstraction (ADR-058): one
// interface over a swappable binary-blob backend for the opaque assets that do not
// belong in Postgres — tenant branding logos/backgrounds (ADR-038), firmware/OTA
// packages (ADR-012/018), and tenant data-export archives.
//
// One seam, many backends. A consumer never touches a backend SDK: it Puts a blob
// under a (tenant, purpose, id) Key, persists the returned opaque Ref, and later
// Opens/Deletes/Stats by Ref. The backend is selected by typed, fail-closed config
// at startup (Config.Validate rejects an unknown backend; New fails closed on a
// declared-but-unbuilt one). Backends:
//
//   - filesystem (DEFAULT) — a mounted volume/PVC; zero cloud dependency, works in
//     kind and self-host out of the box. Reads are served through an authorizing
//     API proxy (Open) — there is no public path, so URL is unsupported.
//   - s3 / gcs (opt-in, later slices) — object storage; reads MAY additionally use
//     a presigned, expiring URL (URL) as well as the proxy.
//
// Isolation (ADR-048/015): every object key is instance- AND tenant-prefixed
// ({instanceId}/{tenant}/{purpose}/{id}); the instance prefix is fixed at store
// construction, and every key segment is charset-validated so a key can never
// traverse out of its namespace on a path-based backend. There is no public-by-
// default bucket — a consumer authorizes each read (proxy) or mints a short-lived
// signed URL (cloud) itself.
//
// This slice ships the interface + typed config + the filesystem default backend.
// The S3-compatible and GCS backends land behind these exact signatures in later
// slices (additive, no consumer change).
//
// Planned additive surface (not in this slice, called out so a later backend ships
// it deliberately rather than discovering the need mid-migration):
//   - a range/offset read (OpenRange or an OpenOptions offset/length) for resumable
//     HTTP Range GETs of large firmware/OTA images (ADR-012/018). Small assets
//     (branding logos) do not need it, so Open stays whole-object for now; the
//     firmware/OTA consumer slice adds the range method across all backends.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

var (
	// ErrNotFound is returned by Open/Stat/URL when no object is stored under the
	// given Ref. Consumers treat it as "asset missing" (fail-closed) rather than an
	// infrastructure error.
	ErrNotFound = errors.New("blob: object not found")

	// ErrURLUnsupported is returned by URL for a backend that cannot mint a direct
	// URL (the filesystem backend): the caller must serve the bytes through the
	// authorizing proxy (Open) instead of handing out a link.
	ErrURLUnsupported = errors.New("blob: direct URL not supported by this backend")

	// ErrTooLarge is returned by Put when the object exceeds PutOptions.MaxSize. The
	// partially-written object is not committed.
	ErrTooLarge = errors.New("blob: object exceeds the maximum allowed size")
)

// maxSegmentLen bounds a single key segment so a pathological id cannot blow past
// a filesystem's per-component name limit; comfortably above any real object id.
const maxSegmentLen = 128

// instanceScopeSegment is the reserved tenant slot for an instance-scoped asset
// (Key.Tenant == ""). It uses a leading '~', which the token grammar forbids, so
// it can never collide with a real tenant token's directory.
const instanceScopeSegment = "~instance"

// reservedIDSuffix is a reserved object-id suffix kept free across ALL backends so
// a future metadata sidecar (or any backend-internal companion file) can never
// collide with a live object. buildKey rejects an id ending in it, keeping key
// validity uniform regardless of backend (a filesystem-only reservation would let
// an S3 store accept an id the filesystem rejects).
const reservedIDSuffix = ".dcmeta"

// Key identifies an object within a tenant's namespace. The store prepends the
// fixed instance prefix, yielding the full object key
// {instanceId}/{tenant}/{purpose}/{id} (ADR-058 §4 / ADR-048). Every segment is
// validated so a key can never traverse out of its namespace on a path-based
// backend.
type Key struct {
	// Tenant is the owning tenant token. Empty means an instance-scoped asset
	// (stored under the reserved instance slot, not a tenant directory).
	Tenant string
	// Purpose is the asset class, e.g. "branding-logo", "firmware".
	Purpose string
	// ID is an opaque unique object id within (tenant, purpose). Include a file
	// extension (e.g. "{uuid}.png") so the filesystem backend can infer a
	// Content-Type — it does not persist one of its own.
	ID string
}

// Ref is the stable, opaque handle a consumer persists in place of the object
// (e.g. a branding_logo column). It names the backend that stored the object and
// the full object key; it carries no bytes. Serialize with String (a blob:// URI)
// and parse with ParseRef.
//
// Ref.Backend binds the stored data to the backend that wrote it: a Ref is only
// dereferenceable by a Store on the same backend (each backend refuses a foreign
// Ref). Switching an instance's backend therefore requires migrating the data (or
// a routing Store), not just flipping config — acceptable pre-GA, but deliberate.
//
// SECURITY: a Ref carries the full object key and the read paths do NOT know the
// caller's tenant, so a store cannot authorize a Ref by itself. Tenant isolation
// lives in the CONSUMER: only ever dereference a Ref you persisted for the acting
// tenant (e.g. look the Ref up from the tenant's own row), never one taken from
// untrusted input. The store enforces only that a key stays within the instance
// prefix and the store root (defense-in-depth, not authorization).
type Ref struct {
	Backend string // the backend identifier that stored the object (e.g. "filesystem")
	Key     string // full object key incl. the instance prefix
}

// String renders the ref as a blob:// URI ("blob://{backend}/{key}") — the form a
// consumer stores in its own column and ParseRef reverses.
func (r Ref) String() string {
	return "blob://" + r.Backend + "/" + r.Key
}

// ParseRef parses a blob:// URI produced by Ref.String back into a Ref. It fails
// closed on a missing scheme, backend, or key so a malformed stored value can
// never be dereferenced.
func ParseRef(s string) (Ref, error) {
	const scheme = "blob://"
	if !strings.HasPrefix(s, scheme) {
		return Ref{}, fmt.Errorf("blob: ref %q is missing the %q scheme", s, scheme)
	}
	rest := s[len(scheme):]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 || slash == len(rest)-1 {
		return Ref{}, fmt.Errorf("blob: ref %q is not of the form blob://backend/key", s)
	}
	return Ref{Backend: rest[:slash], Key: rest[slash+1:]}, nil
}

// PutOptions carries the metadata and limits for a write.
type PutOptions struct {
	// ContentType is the object's MIME type. Cloud backends record it natively; the
	// filesystem backend does not persist it (it infers Content-Type from the key's
	// extension on read), so a consumer needing an exact type should encode it in
	// the Key.ID extension.
	ContentType string
	// MaxSize rejects the write once more than MaxSize bytes have been read, with
	// ErrTooLarge and no committed object. 0 means no store-side limit (the caller
	// is responsible for its own ceiling).
	MaxSize int64
}

// Info is an object's metadata as reported by Stat/Open.
type Info struct {
	Key         string
	Size        int64
	ContentType string
	ModTime     time.Time
}

// URLOptions parameterizes a minted direct URL (cloud backends only).
type URLOptions struct {
	// Expiry is how long the minted URL stays valid; a backend clamps it to its own
	// maximum. Zero lets the backend pick a short default.
	Expiry time.Duration
}

// Store is the backend-agnostic object-store abstraction. Every consumer (branding,
// firmware/OTA, exports) uses this one seam rather than a backend SDK.
type Store interface {
	// Put streams r into the object at key and returns its Ref. The object is
	// committed atomically: a failed or over-limit write leaves no object.
	Put(ctx context.Context, key Key, r io.Reader, opts PutOptions) (Ref, error)
	// Open returns a reader over the object's bytes plus its Info. The caller must
	// Close the reader. Returns ErrNotFound when absent.
	Open(ctx context.Context, ref Ref) (io.ReadCloser, Info, error)
	// URL mints a direct, expiring URL for the object (cloud backends). Returns
	// ErrURLUnsupported for a backend with no public/presigned path (filesystem),
	// signalling the caller to serve via the authorizing proxy (Open) instead.
	URL(ctx context.Context, ref Ref, opts URLOptions) (string, time.Time, error)
	// Delete removes the object. It is idempotent: deleting an absent object is not
	// an error.
	Delete(ctx context.Context, ref Ref) error
	// Stat returns the object's Info without opening its bytes. Returns ErrNotFound
	// when absent.
	Stat(ctx context.Context, ref Ref) (Info, error)
}

// New builds the configured Store. The backend is validated by Config.Validate,
// then constructed; a backend that is a KNOWN identifier but not built in this
// binary (s3, gcs — later slices) fails closed here rather than silently doing
// nothing. instanceID is the fixed key prefix (ADR-048) and must be a valid
// segment.
func New(cfg Config, instanceID string) (Store, error) {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := validateSegment("instanceId", instanceID); err != nil {
		return nil, err
	}
	switch cfg.Backend {
	case BackendFilesystem:
		return NewFilesystemStore(cfg, instanceID)
	case BackendS3, BackendGCS:
		return nil, fmt.Errorf("blob: backend %q is declared but not built in this binary", cfg.Backend)
	default:
		// Unreachable after Validate, but fail closed rather than return a nil store.
		return nil, fmt.Errorf("blob: unknown store backend %q", cfg.Backend)
	}
}

// buildKey renders the full, validated object key {instanceId}/{tenant}/{purpose}/{id}.
// An empty Key.Tenant maps to the reserved instance slot. Every segment is charset-
// validated (validateSegment), so the returned key contains no "/" beyond the
// joins and no ".." — the invariant a path-based backend relies on to stay in its
// namespace.
func buildKey(instanceID string, k Key) (string, error) {
	if err := validateSegment("instanceId", instanceID); err != nil {
		return "", err
	}
	tenant := k.Tenant
	if tenant == "" {
		tenant = instanceScopeSegment
	} else if err := validateSegment("tenant", tenant); err != nil {
		return "", err
	}
	if err := validateSegment("purpose", k.Purpose); err != nil {
		return "", err
	}
	if err := validateSegment("id", k.ID); err != nil {
		return "", err
	}
	if strings.HasSuffix(k.ID, reservedIDSuffix) {
		return "", fmt.Errorf("blob: id segment uses the reserved %q suffix", reservedIDSuffix)
	}
	return instanceID + "/" + tenant + "/" + k.Purpose + "/" + k.ID, nil
}

// validateSegment enforces the strict charset every key segment must satisfy:
// non-empty, at most maxSegmentLen, only [A-Za-z0-9._-], and never exactly "." or
// "..". Excluding '/' and '\' and rejecting the dot-only segments makes it
// impossible for a segment to introduce path traversal or a nested directory on a
// filesystem backend.
func validateSegment(field, seg string) error {
	if seg == "" {
		return fmt.Errorf("blob: %s segment is empty", field)
	}
	if len(seg) > maxSegmentLen {
		return fmt.Errorf("blob: %s segment exceeds %d bytes", field, maxSegmentLen)
	}
	if seg == "." || seg == ".." {
		return fmt.Errorf("blob: %s segment %q is not allowed", field, seg)
	}
	// Reject a leading dot: it neutralizes dotfiles and, on the filesystem backend,
	// keeps object ids from colliding with the ".put-*" in-flight temp files — a
	// grammar-level guarantee that holds across backends.
	if seg[0] == '.' {
		return fmt.Errorf("blob: %s segment %q must not start with a dot", field, seg)
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return fmt.Errorf("blob: %s segment %q contains an invalid character", field, seg)
		}
	}
	return nil
}
