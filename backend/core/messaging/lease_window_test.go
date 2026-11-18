// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"
	"testing/synctest"
	"time"

	nats "github.com/nats-io/nats.go"
)

// delayedLeaseKV models a write applied at send time with a delayed successful
// reply. Adoption first refuses a stale revision, then reads our newer entry.
type delayedLeaseKV struct {
	nats.KeyValue
	delay    time.Duration
	adopt    bool
	holder   []byte
	sent     time.Time
	revision uint64
}

func (kv *delayedLeaseKV) Create(_ string, value []byte) (uint64, error) {
	kv.holder = value
	kv.sent = time.Now()
	kv.revision = 1
	time.Sleep(kv.delay)
	return kv.revision, nil
}

func (kv *delayedLeaseKV) Update(_ string, _ []byte, _ uint64) (uint64, error) {
	if kv.adopt {
		kv.adopt = false
		kv.revision++ // An earlier renewal landed but lost its reply.
		time.Sleep(100 * time.Millisecond)
		return 0, nats.ErrKeyRevisionMismatch
	}
	kv.sent = time.Now()
	kv.revision++
	time.Sleep(kv.delay)
	return kv.revision, nil
}

type delayedLeaseEntry struct {
	nats.KeyValueEntry
	value    []byte
	revision uint64
}

func (e delayedLeaseEntry) Value() []byte    { return e.value }
func (e delayedLeaseEntry) Revision() uint64 { return e.revision }

func (kv *delayedLeaseKV) Get(_ string) (nats.KeyValueEntry, error) {
	return delayedLeaseEntry{value: kv.holder, revision: kv.revision}, nil
}

func TestLeaseWindowStartsAtSuccessfulWriteSend(t *testing.T) {
	for _, operation := range []string{"acquire", "renew", "adopt"} {
		for _, delay := range []time.Duration{250 * time.Millisecond, 1500 * time.Millisecond} {
			t.Run(operation+"/"+delay.String(), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					const ttl = time.Second
					kv := &delayedLeaseKV{}
					dl := &DistributedLease{kv: kv, ttl: ttl}
					if operation == "acquire" {
						kv.delay = delay
					}
					lease, err := dl.Acquire("detect:delayed-reply")
					if err != nil {
						t.Fatal(err)
					}
					if operation != "acquire" {
						kv.delay = delay
						kv.adopt = operation == "adopt"
						if err := lease.Renew(); err != nil {
							t.Fatal(err)
						}
					}
					// Virtual time moves only in the stub's sleeps, so this also
					// requires adoption to use the SECOND Update's send time.
					if got, want := lease.windowRemaining(), ttl-delay; got != want {
						t.Errorf("remaining window = %s, want %s since successful send %s", got, want, kv.sent)
					}
					h := &Holder{lease: lease}
					h.mine.Store(true)
					if got, want := h.Held(), delay < ttl; got != want {
						t.Errorf("Held after reply = %v, want %v", got, want)
					}
					if remaining := ttl - time.Since(kv.sent); remaining > 0 {
						time.Sleep(remaining)
					}
					if h.Held() {
						t.Error("Held once TTL elapsed since successful send")
					}
				})
			})
		}
	}
}
