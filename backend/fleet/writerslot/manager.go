// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package writerslot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Manager is one fleet manager replica's handle on the exclusion tables. It acts only while it
// holds the manager lease: every state-changing transaction begins by reading the lease row
// FOR SHARE and requiring this manager's own holder id and epoch.
type Manager struct {
	db       *gorm.DB
	holderID string
	cfg      Config

	// epoch is the lease epoch this manager holds; 0 until Take succeeds. Take writes it while
	// transactions read it, so it is atomic, and each transaction captures it once (see do).
	epoch atomic.Int64

	// afterLease, when set, runs inside every transaction right after the lease check. Tests use it
	// to hold a transaction open and prove the lease row is read FOR SHARE.
	afterLease func()
}

// NewManager returns a manager. It holds no lease until Take.
//
// holderID names the replica (a pod name, for instance). The lease holder id the manager actually
// uses is that name plus a random per-incarnation suffix, minted here: a restarted process with the
// same name is a DIFFERENT holder, so it takes over (and bumps the epoch) instead of silently
// renewing the lease of a previous incarnation that may still be alive, which would leave two
// processes acting under one epoch. HolderID returns the full id.
func NewManager(db *gorm.DB, holderID string, cfg Config) (*Manager, error) {
	if db == nil {
		return nil, errors.New("writerslot: a database is required")
	}
	if strings.TrimSpace(holderID) == "" {
		return nil, errors.New("writerslot: a lease holder id is required")
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, fmt.Errorf("writerslot: mint the incarnation suffix: %w", err)
	}
	holderID = holderID + "#" + hex.EncodeToString(suffix[:])
	if cfg.Premises == nil {
		cfg.Premises = NoPremises{}
	}
	if cfg.ObservationFreshness <= 0 {
		cfg.ObservationFreshness = defaultFreshness
	}
	if cfg.ClockSkewMargin <= 0 {
		cfg.ClockSkewMargin = defaultClockSkew
	}
	// 🔴 The manager's connection logs NOTHING. gorm's logger prints the SQL with its bound values
	// filled in on an error and on a slow statement, and these statements carry confidential values:
	// a cloud operation's error message, and a per-run role's password. The errors the manager
	// returns are the signal; the SQL text is not worth a leak.
	return &Manager{db: db.Session(&gorm.Session{Logger: logger.Discard}), holderID: holderID, cfg: cfg}, nil
}

// HolderID is the lease holder id, which is also the actor id of system-initiated transitions.
func (m *Manager) HolderID() string { return m.holderID }

// LeaseEpoch is the epoch of the lease this manager holds (0 before Take).
func (m *Manager) LeaseEpoch() int64 { return m.epoch.Load() }

// Take takes or renews the singleton manager lease with a compare-and-set against the database
// clock. It succeeds when no manager holds a live lease, or when this manager already does. A
// takeover increments lease_epoch, which deposes the previous holder: its next transaction fails.
func (m *Manager) Take(ctx context.Context, ttl time.Duration) error {
	if ttl < minLeaseTTL {
		return fmt.Errorf("writerslot: lease ttl %v is below the %v minimum", ttl, minLeaseTTL)
	}
	var epoch *int64
	// ON CONFLICT ... DO UPDATE ... WHERE is the compare-and-set: the row is rewritten only when
	// this holder owns it already or its expiry has passed. Otherwise nothing is returned.
	res := m.db.WithContext(ctx).Raw(`
		INSERT INTO manager_lease (id, holder_id, lease_epoch, expires_at, updated_at)
		VALUES (1, ?, 1, clock_timestamp() + (? * interval '1 millisecond'), clock_timestamp())
		ON CONFLICT (id) DO UPDATE SET
			holder_id   = EXCLUDED.holder_id,
			lease_epoch = CASE WHEN manager_lease.holder_id = EXCLUDED.holder_id AND manager_lease.expires_at >= clock_timestamp()
			                   THEN manager_lease.lease_epoch ELSE manager_lease.lease_epoch + 1 END,
			expires_at  = EXCLUDED.expires_at,
			updated_at  = EXCLUDED.updated_at
		WHERE manager_lease.holder_id = EXCLUDED.holder_id OR manager_lease.expires_at < clock_timestamp()
		RETURNING lease_epoch`, m.holderID, ttl.Milliseconds()).Scan(&epoch)
	if res.Error != nil {
		return fmt.Errorf("writerslot: take lease: %w", res.Error)
	}
	if epoch == nil {
		return ErrLeaseHeld
	}
	m.epoch.Store(*epoch)
	return nil
}

// leaseRow is the singleton read FOR SHARE at the top of every transaction.
type leaseRow struct {
	HolderID string
	Epoch    int64 `gorm:"column:lease_epoch"`
	Live     bool
}

// requireLease is step 1 of the lock order: read the lease row FOR SHARE and require this
// manager's own holder id and epoch, still live. A deposed manager's transaction fails HERE, in
// the database. Its external acts are harmless because each is idempotent by a deterministic name
// and is followed by a transaction that will fail.
func (m *Manager) requireLease(tx *gorm.DB, epoch int64) error {
	var l leaseRow
	res := tx.Raw(`SELECT holder_id, lease_epoch, expires_at > clock_timestamp() AS live FROM manager_lease WHERE id = 1 FOR SHARE`).Scan(&l)
	if res.Error != nil {
		return fmt.Errorf("writerslot: read lease: %w", res.Error)
	}
	if res.RowsAffected == 0 || epoch == 0 || l.HolderID != m.holderID || l.Epoch != epoch || !l.Live {
		return ErrLeaseLost
	}
	return nil
}

// txEpochKey is the transaction setting that carries the lease epoch captured when it began.
const txEpochKey = "fleet:lease_epoch"

// txEpoch is the lease epoch this transaction captured when it began. There is deliberately no
// fallback to the manager's current epoch: a renewal landing mid-transaction would then stamp an
// audit row with an epoch the lease check never saw. A transaction that did not start in do has no
// epoch, and the caller refuses.
func txEpoch(tx *gorm.DB) (int64, bool) {
	if v, ok := tx.Get(txEpochKey); ok {
		if e, ok := v.(int64); ok && e > 0 {
			return e, true
		}
	}
	return 0, false
}

// do runs fn in one transaction that starts with the lease check. No Kubernetes, cloud or
// role-management call is made while it runs: the pattern is intent transaction, external act,
// outcome transaction.
func (m *Manager) do(ctx context.Context, fn func(tx *gorm.DB) error) error {
	// The epoch is captured ONCE, so the lease check and every audit row of this transaction agree
	// even if a renewal lands meanwhile.
	epoch := m.epoch.Load()
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := m.requireLease(tx, epoch); err != nil {
			return err
		}
		// Store on the statement's settings: tx.Set returns a NEW handle and the transaction's own
		// would keep no epoch (the value was silently dropped here before).
		tx.Statement.Settings.Store(txEpochKey, epoch)
		if m.afterLease != nil {
			m.afterLease()
		}
		return fn(tx)
	})
}

// actorOrSystem fills in the reserved system actor.
func (m *Manager) actorOrSystem(a Actor) (Actor, error) {
	if a.ID == "" && a.Issuer == "" {
		return Actor{ID: m.holderID, Issuer: reservedSystemIssuer}, nil
	}
	if a.ID == "" || a.Issuer == "" {
		return Actor{}, errors.New("writerslot: an actor needs both an id and an issuer (opaque, immutable strings)")
	}
	return a, nil
}

// slotRow is writer_slots as read.
type slotRow struct {
	ClusterUID            string
	Epoch                 int64
	State                 string
	HolderRunUID          *string
	DoubtedFrom           *string
	UncertainSince        *time.Time
	RefreshCutAt          *time.Time
	CloudCutProof         *string
	CloudCutAt            *time.Time
	StateCutMethod        *string
	StateCutAt            *time.Time
	Containment           *string
	ContainmentObservedAt *time.Time
	QuarantineReason      *string
}

// identityRow is cloud_identities as read.
type identityRow struct {
	ClusterUID      string
	ManagementMode  string
	Cloud           string
	ProjectID       string
	Location        string
	CloudName       string
	OwnershipNonce  *string
	StateBackend    *string
	Lifecycle       string
	TombstonedAt    *time.Time
	FirstSubmitAt   *time.Time
	CloudResourceID *string
}

// runRow is runs as read.
type runRow struct {
	ID                uint
	RunUID            string
	ClusterUID        string
	Epoch             int64
	Kind              string
	Phase             string
	PodUID            *string
	JobUID            *string
	NodeName          *string
	CredentialMode    string
	KsaTokenTTLSec    int `gorm:"column:ksa_token_ttl_s"`
	StsTokenTTLSec    int `gorm:"column:sts_token_ttl_s"`
	AccessTokenTTLSec int `gorm:"column:access_token_ttl_s"`
	StateRoleName     *string
}

func in(v string, set ...string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

var (
	allStates = []string{StateFree, StateHeld, StateUncertain, StateExcluded, StateRecovering, StateQuarantined}
	allKinds  = []string{KindCreate, KindChange, KindUpgrade, KindDriftCheck, KindDestroy, KindRecover}
	allPhases = []string{PhaseIntent, PhaseJobCreating, PhaseRunning, PhaseExitedOK, PhaseExitedFailed, PhaseUncertain, PhaseExcluded, PhaseReconciled}
)

// lockSlot is step 2: the slot row FOR UPDATE. Unknown values fail closed here, once, for every path
// that reads a slot: acquire, observe, exit, release, doubt, prove, quarantine.
func lockSlot(tx *gorm.DB, clusterUID string) (*slotRow, error) {
	var s slotRow
	res := tx.Raw(`SELECT cluster_uid, epoch, state, holder_run_uid, doubted_from, uncertain_since, refresh_cut_at, cloud_cut_proof, cloud_cut_at,
		state_cut_method, state_cut_at, containment, containment_observed_at, quarantine_reason
		FROM writer_slots WHERE cluster_uid = ? FOR UPDATE`, clusterUID).Scan(&s)
	if res.Error != nil {
		return nil, fmt.Errorf("writerslot: read slot: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	if !in(s.State, allStates...) {
		return nil, unknown("writer_slots.state", s.State)
	}
	if s.CloudCutProof != nil && !in(*s.CloudCutProof, ProofTermination, ProofExpiry) {
		return nil, unknown("writer_slots.cloud_cut_proof", *s.CloudCutProof)
	}
	if s.StateCutMethod != nil && !in(*s.StateCutMethod, CutProcessTerminated, CutPGRoleRevoked, CutCloudCredential) {
		return nil, unknown("writer_slots.state_cut_method", *s.StateCutMethod)
	}
	if s.DoubtedFrom != nil && !in(*s.DoubtedFrom, StateHeld, StateRecovering) {
		return nil, unknown("writer_slots.doubted_from", *s.DoubtedFrom)
	}
	return &s, nil
}

// lockIdentity is step 4: the cloud_identities row, locked FOR SHARE so a tombstone or lifecycle
// change cannot slip in between the admission read and the commit.
func lockIdentity(tx *gorm.DB, clusterUID string) (*identityRow, error) {
	var id identityRow
	res := tx.Raw(`SELECT cluster_uid, management_mode, cloud, project_id, location, cloud_name, ownership_nonce, state_backend,
		lifecycle, tombstoned_at, first_submit_at, cloud_resource_id
		FROM cloud_identities WHERE cluster_uid = ? FOR SHARE`, clusterUID).Scan(&id)
	if res.Error != nil {
		return nil, fmt.Errorf("writerslot: read identity: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, fmt.Errorf("writerslot: no cloud identity for cluster %s", clusterUID)
	}
	if !in(id.ManagementMode, Managed, ObserveOnly) {
		return nil, unknown("cloud_identities.management_mode", id.ManagementMode)
	}
	if !in(id.Lifecycle, LifecycleIntent, LifecycleActive, LifecycleTearingDown, LifecycleDestroyed) {
		return nil, unknown("cloud_identities.lifecycle", id.Lifecycle)
	}
	if id.StateBackend != nil && !in(*id.StateBackend, BackendGCS, BackendPG) {
		return nil, unknown("cloud_identities.state_backend", *id.StateBackend)
	}
	return &id, nil
}

// lockHead is step 5: the state head row, locked FOR UPDATE, or nil when there is none.
func lockHead(tx *gorm.DB, clusterUID string) (*StateHead, error) {
	var head struct {
		Lineage string
		Serial  int64
	}
	res := tx.Raw(`SELECT lineage, serial FROM state_heads WHERE cluster_uid = ? FOR UPDATE`, clusterUID).Scan(&head)
	if res.Error != nil {
		return nil, fmt.Errorf("writerslot: read state head: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &StateHead{Lineage: head.Lineage, Serial: head.Serial}, nil
}

// loadRun is step 6: the run row, locked FOR UPDATE.
func loadRun(tx *gorm.DB, runUID string) (*runRow, error) {
	var r runRow
	res := tx.Raw(`SELECT id, run_uid, cluster_uid, epoch, kind, phase, pod_uid, job_uid, node_name, credential_mode,
		ksa_token_ttl_s, sts_token_ttl_s, access_token_ttl_s, state_role_name
		FROM runs WHERE run_uid = ? FOR UPDATE`, runUID).Scan(&r)
	if res.Error != nil {
		return nil, fmt.Errorf("writerslot: read run: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, transitionRefused("no run %s", runUID)
	}
	if !in(r.Kind, allKinds...) {
		return nil, unknown("runs.kind", r.Kind)
	}
	if !in(r.Phase, allPhases...) {
		return nil, unknown("runs.phase", r.Phase)
	}
	if !in(r.CredentialMode, ModeKindJWKS, ModeGKEWI) {
		return nil, unknown("runs.credential_mode", r.CredentialMode)
	}
	return &r, nil
}

// slotForRun resolves a run to its cluster WITHOUT locking anything but the run's cluster slot, in
// lock order: the run's cluster is read from an unlocked probe, then the slot is locked, then the
// run row. The probe holds no lock, and the run's cluster never changes.
func slotForRun(tx *gorm.DB, runUID string) (string, error) {
	var cluster string
	res := tx.Raw(`SELECT cluster_uid FROM runs WHERE run_uid = ?`, runUID).Scan(&cluster)
	if res.Error != nil {
		return "", fmt.Errorf("writerslot: find run: %w", res.Error)
	}
	if res.RowsAffected == 0 || cluster == "" {
		return "", transitionRefused("no run %s", runUID)
	}
	return cluster, nil
}

// transition is one row of the append-only audit.
type transition struct {
	ClusterUID     string
	Epoch          int64
	RunUID         *string
	From           *string
	To             string
	CloudCutProof  *string
	StateCutMethod *string
	Evidence       string // JSON; EvidenceMap is marshalled into it by appendTransition
	EvidenceMap    map[string]any
	Reason         string
	Actor          Actor
	RequestID      *string
}

// appendTransition is step 9: insert only.
func (m *Manager) appendTransition(tx *gorm.DB, t transition) error {
	if t.Evidence == "" {
		t.Evidence = "{}"
		if t.EvidenceMap != nil {
			// A marshal failure is returned, not replaced with "{}": an audit row whose evidence
			// silently vanished is worse than a transition that refuses.
			b, err := json.Marshal(t.EvidenceMap)
			if err != nil {
				return fmt.Errorf("writerslot: marshal evidence: %w", err)
			}
			t.Evidence = string(b)
		}
	}
	leaseEpoch, ok := txEpoch(tx)
	if !ok {
		return errors.New("writerslot: appendTransition outside a lease-checked transaction")
	}
	var reason *string
	if t.Reason != "" {
		reason = &t.Reason
	}
	err := tx.Exec(`INSERT INTO writer_slot_transitions
		(cluster_uid, epoch, run_uid, from_state, to_state, cloud_cut_proof, state_cut_method, evidence, reason, actor_id, actor_issuer, request_id, manager_lease_epoch, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, clock_timestamp())`,
		t.ClusterUID, t.Epoch, t.RunUID, t.From, t.To, t.CloudCutProof, t.StateCutMethod, t.Evidence, reason, t.Actor.ID, t.Actor.Issuer, t.RequestID, leaseEpoch).Error
	if err != nil {
		return fmt.Errorf("writerslot: append transition: %w", err)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }
