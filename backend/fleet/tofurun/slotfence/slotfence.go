// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package slotfence is the tofurun.Fence that coordinates a run with the writer slot
// (backend/fleet/writerslot): the database, not the runner, decides who the one writer for a
// cluster is. Admission, the observation that the run's pod exists, the exit and the release are
// each one slot transition; a second run for the same cluster is refused at Acquire by the slot.
//
// It lives apart from package tofurun so that the runner image, which holds no database
// credential, does not link the database driver.
package slotfence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-fleet/tofurun"
	"github.com/devicechain-io/dc-fleet/writerslot"
)

// PodEvidence is the Kubernetes half the slot needs: the identity of the run's Job and pod when it
// is running, and the kubelet's report of the pod's containers when it ended. Absence of the pod is
// not representable as evidence (writerslot.PodObservation carries container statuses only).
type PodEvidence interface {
	// Running returns the Job and pod of the run.
	Running(ctx context.Context, runUID string) (jobNamespace, jobUID, podUID, node string, err error)
	// Exited returns the kubelet's observation of the run's recorded pod.
	Exited(ctx context.Context, runUID, podUID string) (writerslot.PodObservation, error)
}

// Config is what the writer slot records about a run beyond what tofurun knows.
type Config struct {
	// CredentialMode is writerslot.ModeKindJWKS or ModeGKEWI.
	CredentialMode string
	// KsaTokenTTL, StsTokenTTL and AccessTokenTTL are the token lifetimes the run's chain is
	// minted with; the slot stores them before the Job exists (each at least a second).
	KsaTokenTTL, StsTokenTTL, AccessTokenTTL time.Duration
	// RunnerImageDigest is recorded with the run.
	RunnerImageDigest string
	// Actor is who requested the run; zero means the manager itself.
	Actor writerslot.Actor
	// Pods supplies the Kubernetes evidence. Required.
	Pods PodEvidence
	// StateRole returns the run's state role name for a pg backend ("" otherwise). Default derives
	// it from the run uid, which is the only name the slot admits.
	StateRole func(runUID string) (string, error)
}

// Fence implements tofurun.Fence over a writerslot.Manager.
type Fence struct {
	mgr *writerslot.Manager
	cfg Config
}

// New returns a Fence. The manager must hold (or be able to take) the manager lease: a deposed
// manager's transitions fail in the database.
func New(mgr *writerslot.Manager, cfg Config) (*Fence, error) {
	if mgr == nil {
		return nil, errors.New("slotfence: a writer slot manager is required")
	}
	if cfg.Pods == nil {
		return nil, errors.New("slotfence: pod evidence is required")
	}
	if cfg.StateRole == nil {
		cfg.StateRole = writerslot.StateRoleName
	}
	return &Fence{mgr: mgr, cfg: cfg}, nil
}

// slotKind maps a runner kind to the slot's. They are the same strings; an unknown one is refused
// rather than passed on.
func slotKind(k tofurun.Kind) (string, error) {
	switch k {
	case tofurun.KindCreate:
		return writerslot.KindCreate, nil
	case tofurun.KindChange:
		return writerslot.KindChange, nil
	case tofurun.KindUpgrade:
		return writerslot.KindUpgrade, nil
	case tofurun.KindDriftCheck:
		return writerslot.KindDriftCheck, nil
	case tofurun.KindDestroy:
		return writerslot.KindDestroy, nil
	case tofurun.KindRecover:
		return writerslot.KindRecover, nil
	}
	return "", fmt.Errorf("slotfence: %w: %q", tofurun.ErrUnknownKind, string(k))
}

// Acquire admits the run: the slot's T1. Whatever the slot refuses (a held slot, a stale or
// missing listing, an unsettled cloud operation, DESTROY in a build that cannot execute one) is
// returned as is, so errors.Is(err, writerslot.ErrNotAdmitted) and the like keep working.
func (f *Fence) Acquire(ctx context.Context, run tofurun.RunInfo) error {
	kind, err := slotKind(run.Kind)
	if err != nil {
		return err
	}
	role := ""
	if run.Backend == tofurun.BackendPG {
		if role, err = f.cfg.StateRole(run.RunUID); err != nil {
			return err
		}
	}
	_, err = f.mgr.Acquire(ctx, writerslot.RunSpec{
		ClusterUID: run.ClusterUID, Kind: kind, RunUID: run.RunUID, RequestID: run.RequestID, Actor: f.cfg.Actor,
		CredentialMode: f.cfg.CredentialMode, CloudIdentityRole: string(run.Class), CloudPrincipal: run.Principal,
		KsaTokenTTL: f.cfg.KsaTokenTTL, StsTokenTTL: f.cfg.StsTokenTTL, AccessTokenTTL: f.cfg.AccessTokenTTL,
		StateRoleName: role, RunnerImageDigest: f.cfg.RunnerImageDigest, TofuVersion: run.TofuVersion,
		ProviderLockHash: run.ProviderLockHash, ModuleDigest: run.ModuleDigest,
	})
	return err
}

// Started records that the run's Job and pod exist: the slot's T2.
func (f *Fence) Started(ctx context.Context, run tofurun.RunInfo) error {
	ns, job, pod, node, err := f.cfg.Pods.Running(ctx, run.RunUID)
	if err != nil {
		return fmt.Errorf("slotfence: observe the run's pod: %w", err)
	}
	return f.mgr.Observe(ctx, run.RunUID, ns, job, pod, node)
}

// Complete records the exit (T3) and releases the slot (T4). A release the slot refuses (an
// unsettled cloud operation, a state role that still exists, a quarantine) leaves the slot held and
// is returned: the next transition is the manager's recovery, never a silent free.
func (f *Fence) Complete(ctx context.Context, run tofurun.RunInfo, out tofurun.Outcome) error {
	// The pod is read by the slot's own record of it, so the evidence names the pod the slot knows.
	_, _, pod, _, err := f.cfg.Pods.Running(ctx, run.RunUID)
	if err != nil {
		return fmt.Errorf("slotfence: read the run's pod: %w", err)
	}
	obs, err := f.cfg.Pods.Exited(ctx, run.RunUID, pod)
	if err != nil {
		return fmt.Errorf("slotfence: observe the run's exit: %w", err)
	}
	if err := f.mgr.Exit(ctx, run.RunUID, writerslot.ExitSpec{
		Pod: obs, Succeeded: out.Succeeded, Head: writerslot.StateHead{Lineage: out.Head.Lineage, Serial: out.Head.Serial},
	}); err != nil {
		return err
	}
	if run.Kind == tofurun.KindRecover {
		return nil // a recovery run is released by the manager's FinishRecovery, with the lock's resolution
	}
	if run.Backend == tofurun.BackendPG {
		if err := f.mgr.DropStateRole(ctx, run.RunUID); err != nil {
			return err
		}
	}
	return f.mgr.Release(ctx, run.RunUID)
}

var _ tofurun.Fence = (*Fence)(nil)
