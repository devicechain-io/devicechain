// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofurun

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// BackendKind names a remote state backend. The values equal the writer slot's.
type BackendKind string

// Backend kinds.
const (
	BackendPG  BackendKind = "pg"
	BackendGCS BackendKind = "gcs"
)

// BackendConfig is what `tofu init` is given for the state: -backend-config arguments (never
// secret) and environment variables (which may be). There is no local fallback: a root without a
// backend block, or one that resolves to local state, is refused after init.
type BackendConfig struct {
	Kind BackendKind
	// Args are -backend-config=key=value pairs, with no secret in them.
	Args []string
	// Env holds the secret half (the pg connection string), by variable name.
	Env map[string]string
	// Workspace, when set, is selected (or created) after init: one workspace per cluster in a
	// shared schema (pg). Empty for a backend that separates clusters by prefix (gcs).
	Workspace string
}

// Backend yields the state configuration for one cluster's run. Config is called once, AFTER the
// fence admitted the run and before the first process starts, so an implementation that must
// provision per-run state access (the pg backend's per-run login role) does so only for a run that
// holds the writer slot.
type Backend interface {
	Kind() BackendKind
	Config(ctx context.Context, run RunInfo) (BackendConfig, error)
}

var (
	identRe  = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
	uuidRe   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$`)
)

// PGBackend keeps state in the fleet manager's Postgres: one schema per provider (pre-created at
// onboarding and owned by the provider's role, so the per-run login role owns nothing), one
// workspace per cluster. The connection string carries the per-run role's password and goes to
// tofu through PG_CONN_STR only.
type PGBackend struct {
	// ConnStr is the per-run role's connection string. Never logged: it is scrubbed from every
	// diagnostic and held out of String.
	ConnStr string
	// Schema is the provider's state schema.
	Schema string
}

// Kind is BackendPG.
func (PGBackend) Kind() BackendKind { return BackendPG }

// String omits the connection string.
func (b PGBackend) String() string { return "PGBackend{schema=" + b.Schema + "}" }

// GoString omits the connection string.
func (b PGBackend) GoString() string { return b.String() }

// Config implements Backend.
func (b PGBackend) Config(_ context.Context, run RunInfo) (BackendConfig, error) {
	clusterUID := run.ClusterUID
	if !uuidRe.MatchString(clusterUID) {
		return BackendConfig{}, errors.New("tofurun: the cluster uid is not a uuid")
	}
	if !identRe.MatchString(b.Schema) {
		return BackendConfig{}, errors.New("tofurun: the state schema is not a plain lower-case identifier")
	}
	if !strings.HasPrefix(b.ConnStr, "postgres://") && !strings.HasPrefix(b.ConnStr, "postgresql://") {
		return BackendConfig{}, errors.New("tofurun: the state connection string must be a postgres:// URL")
	}
	return BackendConfig{
		Kind:      BackendPG,
		Args:      []string{"-backend-config=schema_name=" + b.Schema},
		Env:       map[string]string{"PG_CONN_STR": b.ConnStr},
		Workspace: strings.ToLower(clusterUID),
	}, nil
}

// GCSBackend keeps state in the provider's own bucket under fleet/<cluster_uid>/, written through
// the run's impersonated identity (its credential, not this struct). The prefix is derived, never
// supplied, so one cluster's run cannot be pointed at another's state by a caller.
type GCSBackend struct {
	Bucket string
}

// Kind is BackendGCS.
func (GCSBackend) Kind() BackendKind { return BackendGCS }

// Config implements Backend.
func (b GCSBackend) Config(_ context.Context, run RunInfo) (BackendConfig, error) {
	clusterUID := run.ClusterUID
	if !uuidRe.MatchString(clusterUID) {
		return BackendConfig{}, errors.New("tofurun: the cluster uid is not a uuid")
	}
	if !bucketRe.MatchString(b.Bucket) {
		return BackendConfig{}, fmt.Errorf("tofurun: %q is not a bucket name", b.Bucket)
	}
	return BackendConfig{
		Kind: BackendGCS,
		Args: []string{
			"-backend-config=bucket=" + b.Bucket,
			"-backend-config=prefix=fleet/" + strings.ToLower(clusterUID),
		},
	}, nil
}
