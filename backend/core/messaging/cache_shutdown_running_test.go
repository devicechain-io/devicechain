// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// blockingDeleteStore parks every Delete until its context ends and reports that it did.
type blockingDeleteStore struct {
	*countingStore
	entered   chan struct{}
	cancelled chan error
}

func (s *blockingDeleteStore) Delete(ctx context.Context, _ string, _ ...jetstream.KVDeleteOpt) error {
	s.entered <- struct{}{}
	<-ctx.Done()
	s.cancelled <- ctx.Err()
	return ctx.Err()
}

// A follow-up the timer had already started when the stop began must not outlive the stop
// deadline: the drain that follows closes the connection it is deleting over.
func TestFlushFollowUpsCancelsARunningFollowUpAtTheDeadline(t *testing.T) {
	store := &blockingDeleteStore{newCountingStore(), make(chan struct{}, 4), make(chan error, 4)}
	c := NewCacheOver(store)
	c.delete = time.Hour // only the stop deadline can end the parked delete
	c.fuMu.Lock()
	c.fuPending = map[string]struct{}{"acme|dev": {}}
	c.fuMu.Unlock()
	go c.runFollowUp()
	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the follow-up never reached the store")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go c.flushFollowUps(ctx)
	select {
	case err := <-store.cancelled:
		if err == nil {
			t.Fatal("the running follow-up's delete ended without its context being cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the running follow-up's delete was still in flight 2s after a 100ms stop deadline")
	}
}
