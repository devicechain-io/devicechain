// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/devicechain-io/dc-microservice/limit"
)

// DefaultMaxLiveSubscribersPerTenant bounds subscriptions per tenant and hub per pod.
const DefaultMaxLiveSubscribersPerTenant = 5000

// Keyed routes Value to subscribers registered on Key. Values must be immutable.
type Keyed[T any] struct {
	Key   string
	Value T
}

// LiveOpener returns only after the upstream is ready to receive live messages.
type LiveOpener func(context.Context, string) (<-chan Message, error)

// LiveHub decodes one live feed per tenant and routes immutable values by key.
type LiveHub[T any] struct {
	name    string
	open    LiveOpener
	decode  func(Message, string) []Keyed[T]
	max     int
	mu      sync.Mutex // protects only the tenant directory, never held during open
	tenants map[string]*liveTenant[T]
	dropped atomic.Uint64
	onDrop  func()
}

type liveSubscriber[T any] struct {
	out  chan T
	done chan struct{}
	keys map[string]struct{}
}

type liveTenant[T any] struct {
	mu     sync.Mutex // serializes open, dispatch, removal and channel closure
	dead   bool
	cancel context.CancelFunc
	subs   map[*liveSubscriber[T]]struct{}
	keys   map[string]map[*liveSubscriber[T]]struct{}
	wild   map[*liveSubscriber[T]]struct{}
}

// NewLiveHub builds a hub for one feed. A nonpositive maximum disables the ceiling.
func NewLiveHub[T any](name string, open LiveOpener, decode func(Message, string) []Keyed[T], maxPerTenant int) *LiveHub[T] {
	return &LiveHub[T]{name: name, open: open, decode: decode, max: maxPerTenant, tenants: make(map[string]*liveTenant[T])}
}

// Dropped counts values discarded because a subscriber buffer was full.
func (h *LiveHub[T]) Dropped() uint64 { return h.dropped.Load() }

// OnDrop installs a nonblocking hook before the hub is used. Called once per drop.
func (h *LiveHub[T]) OnDrop(f func()) { h.onDrop = f }

// Subscribe joins a confirmed tenant feed. Empty keys subscribe to every value.
// Cancellation removes the subscriber before closing its buffered channel.
func (h *LiveHub[T]) Subscribe(ctx context.Context, tenant string, keys []string) (<-chan T, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h.mu.Lock()
		t := h.tenants[tenant]
		if t == nil {
			t = &liveTenant[T]{subs: make(map[*liveSubscriber[T]]struct{}), keys: make(map[string]map[*liveSubscriber[T]]struct{}), wild: make(map[*liveSubscriber[T]]struct{})}
			h.tenants[tenant] = t
		}
		h.mu.Unlock()
		// Never hold the directory lock while acquiring the tenant lock. Teardown
		// uses the reverse direction and marks stale entries dead before removal.
		t.mu.Lock()
		if t.dead {
			t.mu.Unlock()
			continue
		}
		if h.max > 0 && len(t.subs) >= h.max {
			err := limit.Exceeded("live subscriptions", len(t.subs)+1, h.max)
			t.mu.Unlock()
			return nil, err
		}
		if t.cancel == nil {
			upctx, cancel := context.WithCancel(context.Background())
			up, err := h.open(upctx, tenant)
			if err != nil {
				cancel()
				h.stopLocked(tenant, t)
				t.mu.Unlock()
				return nil, err
			}
			t.cancel = cancel
			go h.run(upctx, tenant, t, up)
		}
		s := &liveSubscriber[T]{out: make(chan T, liveBuffer), done: make(chan struct{}), keys: make(map[string]struct{})}
		t.subs[s] = struct{}{}
		for _, key := range keys {
			s.keys[key] = struct{}{}
			if t.keys[key] == nil {
				t.keys[key] = make(map[*liveSubscriber[T]]struct{})
			}
			t.keys[key][s] = struct{}{}
		}
		if len(keys) == 0 {
			t.wild[s] = struct{}{}
		}
		t.mu.Unlock()
		go func() {
			select {
			case <-ctx.Done():
			case <-s.done:
				return
			}
			t.mu.Lock()
			h.removeLocked(t, s)
			if len(t.subs) == 0 && !t.dead {
				h.stopLocked(tenant, t)
			}
			t.mu.Unlock()
		}()
		return s.out, nil
	}
}

func (h *LiveHub[T]) removeLocked(t *liveTenant[T], s *liveSubscriber[T]) {
	if _, ok := t.subs[s]; !ok {
		return
	}
	delete(t.subs, s)
	delete(t.wild, s)
	for key := range s.keys {
		delete(t.keys[key], s)
		if len(t.keys[key]) == 0 {
			delete(t.keys, key)
		}
	}
	close(s.out)
	close(s.done)
}

func (h *LiveHub[T]) stopLocked(tenant string, t *liveTenant[T]) {
	t.dead = true
	if t.cancel != nil {
		t.cancel()
	}
	for s := range t.subs {
		h.removeLocked(t, s)
	}
	h.mu.Lock()
	if h.tenants[tenant] == t {
		delete(h.tenants, tenant)
	}
	h.mu.Unlock()
}

func (h *LiveHub[T]) run(ctx context.Context, tenant string, t *liveTenant[T], up <-chan Message) {
	defer func() {
		t.mu.Lock()
		if !t.dead {
			h.stopLocked(tenant, t)
		}
		t.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-up:
			if !ok {
				return
			}
			values := h.decode(msg, tenant)
			t.mu.Lock()
			if t.dead {
				t.mu.Unlock()
				return
			}
			for _, v := range values {
				for s := range t.keys[v.Key] {
					h.send(s, v.Value)
				}
				for s := range t.wild {
					h.send(s, v.Value)
				}
			}
			t.mu.Unlock()
		}
	}
}

func (h *LiveHub[T]) send(s *liveSubscriber[T], value T) {
	select {
	case s.out <- value:
	default:
		h.dropped.Add(1)
		if h.onDrop != nil {
			h.onDrop()
		}
	}
}
