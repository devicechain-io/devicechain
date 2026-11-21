// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package writerslot is the database half of the fleet manager's writer-exclusion protocol: one
// writer per fleet cluster, with the doubt, proof and quarantine rules that decide when a writer
// that may still be alive is finally excluded.
//
// The protocol exists because a database generation does not fence a writer that survives: the
// cloud never compares it. A slot is therefore released only when BOTH cuts are proven for the
// excluded run, because the runner writes to two things the generation does not guard, the cloud
// and the state backend. There is no waiver and no transition that records one.
//
// This package is database only. The Kubernetes half (observing the pod, the Job contract) and the
// Observer's cloud listing arrive later; they call into the transitions here with evidence, and
// every piece of evidence is a typed value, so "the pod is gone" cannot be mistaken for proof.
package writerslot

import "time"

// Slot states.
const (
	StateFree        = "FREE"
	StateHeld        = "HELD"
	StateUncertain   = "UNCERTAIN"
	StateExcluded    = "EXCLUDED"
	StateRecovering  = "RECOVERING"
	StateQuarantined = "QUARANTINED"
)

// Run kinds.
const (
	KindCreate     = "CREATE"
	KindChange     = "CHANGE"
	KindUpgrade    = "UPGRADE"
	KindDriftCheck = "DRIFT_CHECK"
	KindDestroy    = "DESTROY"
	KindRecover    = "RECOVER"
)

// Run phases.
const (
	PhaseIntent       = "INTENT"
	PhaseJobCreating  = "JOB_CREATING"
	PhaseRunning      = "RUNNING"
	PhaseExitedOK     = "EXITED_OK"
	PhaseExitedFailed = "EXITED_FAILED"
	PhaseUncertain    = "UNCERTAIN"
	PhaseExcluded     = "EXCLUDED"
	PhaseReconciled   = "RECONCILED"
)

// Cloud-cut proofs (cut 1).
const (
	ProofTermination = "TERMINATION"
	ProofExpiry      = "EXPIRY"
)

// State-cut methods (cut 2).
const (
	CutProcessTerminated = "PROCESS_TERMINATED"
	CutPGRoleRevoked     = "PG_ROLE_REVOKED"
	CutCloudCredential   = "CLOUD_CREDENTIAL"
)

// Quarantine reasons.
const (
	QuarantineNoProof            = "NO_PROOF_OBTAINABLE"
	QuarantineIdentityMismatch   = "IDENTITY_MISMATCH"
	QuarantineStateHeadMismatch  = "STATE_HEAD_MISMATCH"
	QuarantineForeignLockHolder  = "FOREIGN_LOCK_HOLDER"
	QuarantineUnsettledOperation = "UNSETTLED_ERRORED_OPERATION"
	QuarantineSecondPod          = "SECOND_POD_FOR_RUN"
	QuarantineCreateBeforeIntent = "CREATE_TIME_BEFORE_INTENT"
)

// Credential modes.
const (
	ModeKindJWKS = "kind_jwks"
	ModeGKEWI    = "gke_wi"
)

// State backends.
const (
	BackendGCS = "gcs"
	BackendPG  = "pg"
)

// Management modes and lifecycles.
const (
	Managed     = "managed"
	ObserveOnly = "observe_only"

	LifecycleIntent      = "INTENT"
	LifecycleActive      = "ACTIVE"
	LifecycleTearingDown = "TEARING_DOWN"
	LifecycleDestroyed   = "DESTROYED"
)

const (
	// reservedSystemIssuer is the issuer of system-initiated transitions; their actor id is the
	// manager's lease holder id.
	reservedSystemIssuer = "fleet-manager"
	defaultFreshness     = 2 * time.Minute
	defaultClockSkew     = 30 * time.Second
	// defaultImportSkew is the margin between the cloud's clock and the database's when judging
	// whether a cluster was created after the intent was recorded.
	defaultImportSkew = 5 * time.Minute
	minLeaseTTL       = 100 * time.Millisecond
)

// Actor identifies who or what decided a transition: opaque, immutable strings, never a username
// or display name. System-initiated transitions use the reserved issuer "fleet-manager" and the
// manager's lease holder id.
type Actor struct {
	ID     string
	Issuer string
}

// Premises answers what the S0 premise record says. EXPIRY is accepted only in a credential mode
// whose refresh cut has been measured; the default refuses, so an unmeasured mode can only end in
// quarantine.
type Premises interface {
	// ExpiryHolds reports whether the premise record has the refresh-cut experiment HOLDS for the
	// credential mode.
	ExpiryHolds(credentialMode string) bool
}

// NoPremises refuses every EXPIRY proof. It is the default: fail closed.
type NoPremises struct{}

// ExpiryHolds always returns false.
func (NoPremises) ExpiryHolds(string) bool { return false }

// Config tunes a Manager. Zero values take safe defaults; Premises is NOT defaulted to anything
// permissive.
type Config struct {
	// Premises decides whether EXPIRY is admissible per credential mode. Default NoPremises.
	Premises Premises
	// ObservationFreshness bounds how old the Observer's last COMPLETE listing may be for a run to
	// be admitted. "Could not list" is not "nothing running". Default two minutes.
	ObservationFreshness time.Duration
	// ClockSkewMargin is added to a run's token lifetimes to make the EXPIRY residual.
	ClockSkewMargin time.Duration
}

// RunSpec is what acquiring the slot records about the run before any Job exists.
type RunSpec struct {
	ClusterUID        string
	Kind              string
	RunUID            string
	RequestID         string
	Actor             Actor
	CredentialMode    string
	CloudIdentityRole string // "apply" or "destroy"
	CloudPrincipal    string
	KsaTokenTTL       time.Duration
	StsTokenTTL       time.Duration
	AccessTokenTTL    time.Duration
	StateRoleName     string // the per-run Postgres role, pg backend only
	RunnerImageDigest string
	TofuVersion       string
	ProviderLockHash  string
	ModuleDigest      string
}

// Run is what Acquire returns.
type Run struct {
	RunUID     string
	ClusterUID string
	Epoch      int64
	Kind       string
}

// ContainerStatus is one container's status as the kubelet reported it.
type ContainerStatus struct {
	Name       string
	Terminated bool
}

// PodObservation is what the Kubernetes half observes about the recorded pod. It carries
// container statuses and nothing else, on purpose: pod absence, pod phase and disruption
// conditions are not representable here because none of them is proof the process stopped.
type PodObservation struct {
	PodUID     string
	Containers []ContainerStatus
	// Running and NodeReady are for reaffirmation only; they are never evidence of termination.
	Running   bool
	NodeReady bool
}

// AllTerminated reports whether the observation covers the pod and EVERY container is terminated.
// An observation with no containers is not one: nothing was observed.
func (p PodObservation) AllTerminated(podUID string) bool {
	if p.PodUID == "" || p.PodUID != podUID || len(p.Containers) == 0 {
		return false
	}
	for _, c := range p.Containers {
		if !c.Terminated {
			return false
		}
	}
	return true
}

// StateHead is the last state the manager knows a run wrote.
type StateHead struct {
	Lineage string
	Serial  int64
}

// ExitSpec is the evidence for the exit of a run.
type ExitSpec struct {
	Pod       PodObservation
	Succeeded bool
	Head      StateHead
}

// CloudCut is cut 1.
type CloudCut struct {
	Proof string // ProofTermination or ProofExpiry
	// Pod is the observation read while the pod object was held by the manager's finalizer. Required
	// for TERMINATION and ignored for EXPIRY.
	Pod PodObservation
}

// StateCut is cut 2.
type StateCut struct {
	Method string // CutProcessTerminated, CutPGRoleRevoked or CutCloudCredential
}

// Proof is both cuts, recorded together: the slot is EXCLUDED only when both are proven.
type Proof struct {
	Cloud CloudCut
	State StateCut
	Actor Actor
}

// LockResolution is what the backend lock looked like when a recovery run finished.
type LockResolution string

// Lock resolutions. Anything else, including the zero value, refuses.
const (
	// LockAbsent: no advisory lock is present for the workspace (pg), or the lock object is gone (gcs).
	LockAbsent LockResolution = "ABSENT"
	// LockForceUnlockedFromExcludedRun: a GCS lock whose recorded holder was the excluded run was
	// force-unlocked. Any other holder is LockForeign, which quarantines.
	LockForceUnlockedFromExcludedRun LockResolution = "FORCE_UNLOCKED_EXCLUDED_RUN"
	// LockForeign: the lock is held by anyone but the excluded run. Never force-unlocked.
	LockForeign LockResolution = "FOREIGN"
)
