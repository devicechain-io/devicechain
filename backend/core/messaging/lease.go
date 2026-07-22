// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/google/uuid"
	nats "github.com/nats-io/nats.go"
)

// leaseBucket is the JetStream KV bucket holding partition-ownership leases for
// an instance. It names its entry in the kv inventory, which selects its disk
// ceiling (kv.BucketLeases, a State bucket — a refused write fails an Acquire).
const leaseBucket = kv.BucketLeases

// DefaultLeaseTTL is the platform fencing window every Class-3 lease user passes
// to NewDistributedLease. It MUST be identical across all of them, because they
// share one instance bucket and the bucket's TTL is a single value (a divergent
// TTL is rejected at construction; see NewDistributedLease). It does not need to
// exceed the consumer AckWait: renewal runs on its own goroutine (KeepAlive), so
// a processing-loop stall clearing a batch does not starve renewal — the TTL only
// has to survive a GC pause or a brief NATS blip, which this comfortably does,
// with a renewal interval of <= TTL/3 (~10s).
const DefaultLeaseTTL = 30 * time.Second

var (
	// ErrLeaseHeld is returned by Acquire when another replica already owns the
	// partition. It is not a failure: the caller becomes a WARM STANDBY that binds
	// no input consumer and does nothing but retry acquisition, which is the
	// intended HA posture (ADR-070 decision 4a). Distinguishing it from a real
	// error is why it is a sentinel.
	ErrLeaseHeld = errors.New("messaging: partition lease already held by another owner")
	// ErrNotHolder is returned once this lease is DEFINITIVELY no longer ours — its
	// validity window (last successful renewal + TTL) has elapsed, or a live read
	// shows a different owner, or it was released. The caller must stop consuming
	// and tear down its keyed state (ADR-070 M3 self-eviction), then RELEASE this
	// lease and Acquire a fresh one to recover (Release clears our own now-stale
	// entry so the re-Acquire is not blocked by it). A single failed Renew is NOT
	// this: see Renew.
	ErrNotHolder = errors.New("messaging: lease is no longer held by this owner")
	// ErrStaleEpoch is returned by Fence.RejectIfStale for a downstream write whose
	// epoch predates the newest owner seen for the partition (ADR-070 decision 4b).
	ErrStaleEpoch = errors.New("messaging: write carries a stale ownership epoch")
)

// DistributedLease mints fenced single-owner leases across all replicas of a
// service, backed by NATS JetStream KV optimistic concurrency — the same
// substrate as DistributedLock, but built for a DIFFERENT shape of coordination.
//
// A lock (lock.go) is a short critical section held for the duration of one
// guarded call; a lease is LONG-LIVED partition ownership held for the lifetime
// of a leader, kept alive by renewal and carrying a fence token so a handover
// race cannot corrupt downstream state (ADR-070). Only Class-3 stateful operators
// (DETECT, the Sparkplug adapter) take leases; Class 1/2 services must not — a
// lease there is pure SPOF and a throughput ceiling.
//
// Acquisition is a KV Create, which fails when the key already exists, so exactly
// one replica per partition wins; the bucket's TTL auto-expires a lease whose
// holder crashed or stalled past renewal, freeing the partition for a standby.
type DistributedLease struct {
	kv  nats.KeyValue
	ttl time.Duration
}

// NewDistributedLease returns a DistributedLease over the instance lease bucket,
// creating it with the given TTL if needed.
//
// The lease bucket is SHARED across every Class-3 operator in the instance, and a
// KV bucket has one TTL — the fencing window everything is sized against — so all
// callers must pass the SAME ttl (DefaultLeaseTTL). KeyValueStore reconciles only
// an existing bucket's byte ceiling, not its TTL, so a second caller requesting a
// different TTL would otherwise SILENTLY inherit the first's window — a wrong,
// unenforceable fencing window. We fail closed on that instead: a divergent TTL is
// a loud startup error, not a silent misconfiguration.
func (nmgr *NatsManager) NewDistributedLease(ttl time.Duration) (*DistributedLease, error) {
	bucket := sanitizeName(fmt.Sprintf("%s_%s", nmgr.Microservice.InstanceId, leaseBucket))
	store, err := nmgr.KeyValueStore(leaseBucket, bucket, ttl)
	if err != nil {
		return nil, err
	}
	status, err := store.Status()
	if err != nil {
		return nil, err
	}
	if status.TTL() != ttl {
		return nil, fmt.Errorf("messaging: lease bucket %q already exists with TTL %s but this caller "+
			"requested %s — every Class-3 lease user in an instance must agree on the lease TTL, since "+
			"it is the shared fencing window. Align the TTL (DefaultLeaseTTL) or drain the bucket",
			bucket, status.TTL(), ttl)
	}
	return &DistributedLease{kv: store, ttl: ttl}, nil
}

// Acquire attempts to take ownership of a partition. On success it returns a held
// Lease carrying the ownership epoch; if another replica already owns the
// partition it returns ErrLeaseHeld and the caller becomes a warm standby.
//
// The partition key namespaces ownership within the shared instance bucket, so it
// must be globally unique across every Class-3 operator in the instance (e.g.
// "detect:<tenant>", "sparkplug"), the same discipline DistributedLock.WithLock
// applies to lock names.
func (l *DistributedLease) Acquire(partition string) (*Lease, error) {
	key := kvKey(partition)
	holder := uuid.NewString()
	rev, err := l.kv.Create(key, []byte(holder))
	if errors.Is(err, nats.ErrKeyExists) {
		return nil, ErrLeaseHeld
	}
	if err != nil {
		return nil, err
	}
	return &Lease{kv: l.kv, key: key, holder: holder, epoch: rev, ttl: l.ttl, rev: rev, lastRenew: time.Now()}, nil
}

// Lease is one acquired ownership of a partition. It is safe for concurrent use by
// a single KeepAlive renewer goroutine alongside a processing loop calling
// AmITheHolder — do NOT call Renew from more than one goroutine (KeepAlive is that
// goroutine).
type Lease struct {
	kv     nats.KeyValue
	key    string
	holder string        // uuid identifying THIS acquisition; a takeover writes a different one
	ttl    time.Duration // the fencing window, for validity checks (see stillValid)

	// epoch is the KV revision at Acquire, and it is IMMUTABLE for this ownership
	// (ADR-070 M1). It is the token stamped onto every downstream write and checked
	// by Fence.RejectIfStale. It must NOT track the current KV revision: Renew
	// writes to refresh the TTL and thereby increments the revision, so advertising
	// the current revision would make the owner's own renewals fence its own
	// in-flight writes. KV revisions are monotonic within one stream incarnation,
	// so a later owner's Acquire epoch always exceeds an earlier owner's — a stream
	// recreate / KV restore resets that and is a fence-invalidation event (quiesce
	// and re-stamp; see ADR-070 decision 4b DR caveat). Written once at
	// construction, so Epoch() reads it without the mutex.
	epoch uint64

	mu sync.Mutex
	// rev is the latest KV revision of our entry, used for the CAS on the next
	// Renew/Release. Unlike epoch it ADVANCES on every successful Renew.
	rev uint64
	// lastRenew anchors the validity window: we still hold the lease until
	// lastRenew+ttl even when NATS is briefly unreachable, because no standby can
	// Acquire our partition until the server-side entry TTL-expires. Refreshed on
	// every successful Renew.
	lastRenew time.Time
	released  bool
}

// Epoch returns the immutable ownership epoch to stamp onto downstream writes.
func (lease *Lease) Epoch() uint64 { return lease.epoch }

// Renew refreshes the lease TTL, extending ownership. It is a revision-checked KV
// Update: on success it resets the entry's age (and thus its TTL), advances the
// CAS revision for the next Renew, and re-anchors the validity window — but it
// PRESERVES the advertised epoch (M1).
//
// A non-nil return means the renewal did not land. That is NOT by itself proof of
// lost ownership: it may be a transient NATS blip, during which we still hold the
// lease until our validity window elapses (no standby can Acquire until the
// server expires the entry). KeepAlive is what converts a persistent failure into
// definitive loss at the window boundary; a caller renewing by hand should do the
// same rather than self-evict on one failed Renew.
func (lease *Lease) Renew() error {
	lease.mu.Lock()
	if lease.released {
		lease.mu.Unlock()
		return ErrNotHolder
	}
	rev := lease.rev
	lease.mu.Unlock()

	newRev, err := lease.kv.Update(lease.key, []byte(lease.holder), rev)
	if err != nil {
		return err
	}

	lease.mu.Lock()
	lease.rev = newRev
	lease.lastRenew = time.Now()
	lease.mu.Unlock()
	return nil
}

// KeepAlive renews the lease on this goroutine at the given interval until ctx is
// cancelled (returns nil) or ownership is DEFINITIVELY lost (returns ErrNotHolder).
// Callers run it in a DEDICATED goroutine, never inline on the processing loop
// (ADR-070 M4): a loop that stalls clearing a batch must not also starve renewal.
// interval must be well under the lease TTL (<= TTL/3).
//
// A single failed renewal does not end it: a transient NATS blip is ridden out —
// we keep the lease until its validity window (last success + TTL) elapses,
// retrying on each tick — so a shared broker hiccup does not evict every owner at
// once (which, together with re-Acquire hitting our own not-yet-expired entry,
// would be a fleet-wide, TTL-long outage). Only once the window has elapsed with
// no successful renewal is loss certain. On a genuine takeover, renewals keep
// failing and this converges within one TTL; the processing loop's AmITheHolder
// check catches a live takeover sooner.
func (lease *Lease) KeepAlive(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := lease.Renew(); err != nil && !lease.stillValid() {
				return ErrNotHolder
			}
		}
	}
}

// AmITheHolder reports whether this lease still owns its partition. The active
// owner must call it every read-loop iteration (or batch) and tear down its
// consumer and keyed state the instant it returns false (ADR-070 M3): the epoch
// fence stops a stale WRITE, but only self-eviction stops a zombie owner from
// going on consuming half of a shared durable substream.
//
// A LIVE read that shows a different owner (or a vanished entry) is definitive
// loss → false. A read we cannot complete (a NATS blip) is NOT: we still hold the
// lease until our validity window elapses, so within it we report true — the same
// ride-out KeepAlive does, and for the same reason. Only a transient failure PAST
// the window reports loss.
func (lease *Lease) AmITheHolder() (bool, error) {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return false, nil
	}
	entry, err := lease.kv.Get(lease.key)
	if errors.Is(err, nats.ErrKeyNotFound) {
		return false, nil // definitively gone (expired or deleted)
	}
	if err != nil {
		// Transient: cannot confirm right now. We still hold it until the window
		// elapses; past that, loss is certain.
		if lease.stillValidLocked() {
			return true, nil
		}
		return false, err
	}
	// Only our own uuid means we still hold it — and while we do, no one else could
	// have Created (Create fails on an existing key), so no higher epoch exists.
	return string(entry.Value()) == lease.holder, nil
}

// stillValid reports whether the lease is within its validity window: held, not
// released, and last successfully renewed less than a TTL ago.
func (lease *Lease) stillValid() bool {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.stillValidLocked()
}

func (lease *Lease) stillValidLocked() bool {
	return !lease.released && time.Since(lease.lastRenew) < lease.ttl
}

// Release relinquishes the lease. It is a revision-checked delete of exactly the
// entry we created, so if our lease already expired and a standby took over, this
// is a no-op that cannot drop the new owner's hold — the same guard
// DistributedLock uses on release. It is idempotent (a second call is a no-op).
//
// Call it on BOTH the normal shutdown path and the self-eviction path (after
// KeepAlive/AmITheHolder report loss): it clears our own now-stale entry so a
// subsequent Acquire is not blocked by it. A returned error is informational —
// our own hold is relinquished regardless; a transient delete failure leaves the
// entry to age out via its TTL (the crash-path handover, no corruption).
func (lease *Lease) Release() error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return nil
	}
	lease.released = true
	return lease.kv.Delete(lease.key, nats.LastRevision(lease.rev))
}

// Fence is the write-side handover-race guard (ADR-070 decision 4b). It lives on a
// DOWNSTREAM consumer of a Class-3 operator's output and remembers the highest
// ownership epoch it has admitted per partition, rejecting any write carrying an
// older epoch — so a zombie old owner waking from a GC pause after a standby has
// taken over cannot flush a stale write past the new owner's.
//
// IMPORTANT — this is an IN-PROCESS, NON-DURABLE fast guard. Its high-water lives
// in memory and does NOT survive a restart, so for a durable downstream (an ALARM
// row, the DeviceState projection) it is not sufficient on its own: a rollout
// would reset the high-water and re-admit a stale write. The AUTHORITATIVE fence
// there must be DURABLE — persist the owning epoch alongside the state and make
// the write conditional on it (e.g. accept only when epoch >= the stored epoch).
// That durable check is the ADR-070 B1 work threaded into RaiseAlarmRequest /
// alarm_contributor and the DeviceState SessionId guard; this Fence is the
// in-memory reference for the same comparison, usable ahead of a durable write.
type Fence struct {
	mu   sync.Mutex
	high map[string]uint64
}

// NewFence returns an empty Fence.
func NewFence() *Fence {
	return &Fence{high: make(map[string]uint64)}
}

// RejectIfStale admits a write for a partition at the given ownership epoch,
// advancing the partition's high-water mark, or returns ErrStaleEpoch if a newer
// owner's epoch has already been admitted. A write AT the current high-water is
// admitted, not rejected: that is the current owner's own in-flight stream at a
// stable epoch, and admitting it is exactly why Renew must preserve the epoch (M1)
// rather than re-stamp a higher one. Partitions are independent.
func (f *Fence) RejectIfStale(partition string, epoch uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if epoch < f.high[partition] {
		return ErrStaleEpoch
	}
	f.high[partition] = epoch
	return nil
}
