// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofurun

import (
	"context"
	"errors"
	"time"
)

// ErrNoFence: a session needs a Fence. There is no unfenced mode.
var ErrNoFence = errors.New("tofurun: a writer fence is required: there is no unfenced run")

// StateHead is the state's identity and position: its lineage and serial. The zero value is "no
// state yet" (a first create).
type StateHead struct {
	Lineage string
	Serial  int64
}

// RunInfo is what the fence is told about a run before it may start: who, what, under which
// identity, and the digests that bind the run to the code it will execute.
type RunInfo struct {
	ClusterUID  string
	RunUID      string
	RequestID   string
	Kind        Kind
	Class       IdentityClass
	Principal   string
	Backend     BackendKind
	TofuVersion string
	// ProviderLockHash is the sha256 of the root's .terraform.lock.hcl ("" when the root has none).
	ProviderLockHash string
	// ModuleDigest is the digest of the root tree exactly as it is run.
	ModuleDigest string
}

// Outcome is what the fence is told when the run ended.
type Outcome struct {
	Succeeded bool
	Head      StateHead
	// Applied reports that an apply ran (even one that failed part way).
	Applied bool
}

// Fence is the writer exclusion seam. Acquire is called once, before any tofu process or any
// work-directory content exists; an error means no writer may start and the session is not opened.
// Started is called once the run's own state access exists and immediately before the first
// process: the point at which the writer is really alive. Complete is called once when the session closes. The adapter over the writer slot is
// slotfence.Fence; the slot's database, not this package, decides who the one writer is.
type Fence interface {
	Acquire(ctx context.Context, run RunInfo) error
	Started(ctx context.Context, run RunInfo) error
	Complete(ctx context.Context, run RunInfo, out Outcome) error
}

// completeTimeout bounds the final report to the fence, which must run even when the run's own
// context is already cancelled.
const completeTimeout = 30 * time.Second
