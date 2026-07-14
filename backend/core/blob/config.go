// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package blob

import (
	"fmt"
	"strings"
)

// Backend identifiers (ADR-058 §2 — where objects physically live). This slice
// builds only BackendFilesystem; the cloud backends are declared so config
// validation accepts them once their impls land (additive, no consumer change),
// but selecting one before it is built fails closed at construction (New) rather
// than here — mirroring the secrets package's declared-vs-built split.
const (
	// BackendFilesystem stores objects on a mounted volume/PVC (the default; zero
	// cloud dependency, works in kind and self-host out of the box).
	BackendFilesystem = "filesystem"
	// BackendS3 stores objects in S3-compatible storage — AWS S3 or MinIO (opt-in,
	// a later slice).
	BackendS3 = "s3"
	// BackendGCS stores objects in Google Cloud Storage (opt-in, deferred to
	// Phase 2).
	BackendGCS = "gcs"
)

// Config is the typed, fail-closed object-store configuration. It selects the
// backend and carries the backend's non-secret settings. No cloud CREDENTIAL is
// ever a plaintext config value (ADR-058 §5): the S3 backend takes its access key /
// secret from the standard AWS credential chain (env vars from the instance K8s
// Secret, IRSA, or an instance profile), never from these fields.
type Config struct {
	// Backend selects where objects live. Default: BackendFilesystem.
	Backend string

	// Directory is the filesystem-backend root (a mounted volume/PVC path).
	// Required for BackendFilesystem; ignored for cloud backends.
	Directory string

	// S3 settings (BackendS3). Non-secret only.
	//
	// Bucket is the target bucket (required for s3). Region is the AWS region
	// (required unless Endpoint is set, e.g. for MinIO). Endpoint overrides the AWS
	// endpoint for an S3-compatible service (MinIO); leave empty for AWS S3.
	// UsePathStyle forces path-style addressing (bucket in the path, not the host) —
	// required by MinIO and most S3-compatible servers.
	Bucket       string
	Region       string
	Endpoint     string
	UsePathStyle bool
}

// DefaultConfig is the zero-cloud default: the filesystem backend. Directory is
// left empty and must be supplied (Validate rejects an empty filesystem root).
func DefaultConfig() Config {
	return Config{Backend: BackendFilesystem}
}

// withDefaults fills an empty backend with the default so an omitted key means
// "the default backend", not an invalid empty selection.
func (c Config) withDefaults() Config {
	if c.Backend == "" {
		c.Backend = BackendFilesystem
	}
	return c
}

// Validate fails closed on an unknown backend, and on a filesystem backend with no
// directory, so a misspelled or under-specified selection is rejected at startup
// rather than silently misbehaving. A declared-but-unbuilt cloud backend passes
// identifier validation here; whether it is actually built in this binary is
// enforced at construction (New).
func (c Config) Validate() error {
	c = c.withDefaults()
	switch c.Backend {
	case BackendFilesystem:
		if strings.TrimSpace(c.Directory) == "" {
			return fmt.Errorf("blob: filesystem backend requires a directory")
		}
	case BackendS3:
		if strings.TrimSpace(c.Bucket) == "" {
			return fmt.Errorf("blob: s3 backend requires a bucket")
		}
		// A region OR a custom endpoint must be set: AWS S3 needs a region to build
		// its endpoint; an S3-compatible service (MinIO) supplies an explicit one.
		if strings.TrimSpace(c.Region) == "" && strings.TrimSpace(c.Endpoint) == "" {
			return fmt.Errorf("blob: s3 backend requires a region (AWS) or an endpoint (S3-compatible)")
		}
	case BackendGCS:
		// Declared for forward-compatible validation; built in a later slice.
	default:
		return fmt.Errorf("blob: unknown store backend %q", c.Backend)
	}
	return nil
}
