// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/google/uuid"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

const (
	// lockBucket is the JetStream KV bucket holding lock keys for an instance. It
	// names its entry in the kv inventory, which is what selects its disk ceiling.
	lockBucket = kv.BucketLocks
	// lockRetries bounds how many times an acquire is retried before giving up,
	// and lockBackoff is the wait between attempts. Together they reproduce the
	// former redislock LimitRetry(LinearBackoff(5s), 5) behavior (ADR-007).
	lockRetries = 5
	lockBackoff = 5 * time.Second
)

// DistributedLock provides a mutex across all replicas of a microservice,
// backed by NATS JetStream KV optimistic concurrency (ADR-007 replaces the
// Redis redislock). Acquisition is a KV Create, which fails when the key
// already exists; the bucket's TTL auto-expires a lock whose holder crashed
// without releasing it, so a dead holder cannot wedge the lock forever.
type DistributedLock struct {
	kv nats.KeyValue
}

// NewDistributedLock returns a DistributedLock over the instance lock bucket,
// creating it with the given TTL if needed. The TTL bounds how long a lock
// survives without being released (a crashed holder), so it must comfortably
// exceed the guarded critical section.
func (nmgr *NatsManager) NewDistributedLock(ttl time.Duration) (*DistributedLock, error) {
	bucket := sanitizeName(fmt.Sprintf("%s_%s", nmgr.Microservice.InstanceId, lockBucket))
	store, err := nmgr.KeyValueStore(lockBucket, bucket, ttl)
	if err != nil {
		return nil, err
	}
	return &DistributedLock{kv: store}, nil
}

// WithLock acquires the named lock, runs logic while holding it, and releases
// it afterward. It retries acquisition with a bounded backoff while the lock is
// held by another replica; if it cannot be obtained within the retry budget the
// error is returned and logic never runs (fail-closed). The lock is released
// even if logic returns an error.
func (l *DistributedLock) WithLock(ctx context.Context, name string, logic func(ctx context.Context) error) error {
	key := kvKey(name)
	holder := uuid.NewString()

	log.Info().Str("lock", name).Msg("Acquiring distributed lock...")
	acquired := false
	var rev uint64
	for attempt := 0; attempt <= lockRetries; attempt++ {
		if r, err := l.kv.Create(key, []byte(holder)); err == nil {
			rev = r
			acquired = true
			break
		} else if !errors.Is(err, nats.ErrKeyExists) {
			return err
		}
		if attempt == lockRetries {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(lockBackoff):
		}
	}
	if !acquired {
		return fmt.Errorf("messaging: could not obtain distributed lock %q within %d attempts", name, lockRetries+1)
	}

	defer func() {
		// Release only the exact entry we created: a revision-checked delete is a
		// no-op if our entry already expired (TTL) and another replica re-created
		// the lock, so a Get-then-Delete race can never drop someone else's hold.
		if derr := l.kv.Delete(key, nats.LastRevision(rev)); derr != nil {
			log.Debug().Err(derr).Str("lock", name).
				Msg("Distributed lock already released or taken over; nothing to delete.")
		}
	}()

	log.Info().Str("lock", name).Msg("Lock obtained. Running guarded logic.")
	return logic(ctx)
}
