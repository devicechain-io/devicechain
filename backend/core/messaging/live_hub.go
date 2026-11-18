// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
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

// LiveHub decodes live feeds per tenant binding and routes immutable values by key.
// Each subscriber buffers 256 upstream messages as batches of matching values.
type LiveHub[T any] struct {
	name    string
	open    LiveOpener
	binding func(string) (string, error)
	// pinSuffix is the stream suffix a resolved binding is pinned for on the
	// upstream context; empty for hubs with no binding.
	pinSuffix string
	decode    func(Message, string) []Keyed[T]
	max       int
	mu        sync.Mutex // protects only the tenant directory, never held during open
	tenants   map[string]*liveTenant[T]
	dropped   atomic.Uint64
	onDrop    func()
}

type liveSubscriber[T any] struct {
	out  chan []T
	done chan struct{}
	keys map[string]struct{}
}

type liveTenant[T any] struct {
	mu      sync.Mutex // protects all feed generations and the tenant-wide ceiling
	dead    bool
	count   int
	feeds   int
	current *liveFeed[T]
}

type liveFeed[T any] struct {
	dead    bool
	binding string
	ready   chan struct{} // non-nil while opening, closed to release joiners
	cancel  context.CancelFunc
	subs    map[*liveSubscriber[T]]struct{}
	keys    map[string]map[*liveSubscriber[T]]struct{}
	wild    map[*liveSubscriber[T]]struct{}
}

// NewLiveHub builds a hub for one feed. A nonpositive maximum disables the ceiling.
func NewLiveHub[T any](name string, open LiveOpener, decode func(Message, string) []Keyed[T], maxPerTenant int) *LiveHub[T] {
	return &LiveHub[T]{name: name, open: open, decode: decode, max: maxPerTenant, tenants: make(map[string]*liveTenant[T])}
}

// SharedLiveHub returns the manager's hub for stream suffix key, building it on first use.
// The manager supplies the binding resolver and opener; build supplies decoding and limits.
// The hub lives as long as the manager, so a replaced manager takes its hubs with it.
func SharedLiveHub[T any](nmgr *NatsManager, key string, build func() *LiveHub[T]) *LiveHub[T] {
	// Only the winning lazy constructor runs. Losing LoadOrStore candidates have
	// never called build, so they cannot own a hub or an open upstream.
	value, _ := nmgr.liveHubs.LoadOrStore(key, sync.OnceValue(func() *LiveHub[T] {
		h := build()
		// The binding is the tenant's placement group for this suffix: a local
		// lookup, no broker round trip. With shard groups off it is the suffix
		// itself, so it never changes and every subscriber shares one feed. The
		// opener build supplied is kept; the resolved group reaches it pinned on
		// the upstream context, so SubscribeLive listens on exactly that group.
		// Direct NewLiveHub users have no binding and never rebind.
		h.binding = func(tenant string) (string, error) {
			nmgr.freezeStreamRouting()
			return nmgr.tenantSuffix(tenant, key)
		}
		h.pinSuffix = key
		return h
	}))
	lazy, ok := value.(func() *LiveHub[T])
	if !ok {
		panic(fmt.Sprintf("messaging.SharedLiveHub: key %q reused with a different value type", key))
	}
	return lazy()
}

// Dropped counts values discarded because a subscriber buffer was full.
func (h *LiveHub[T]) Dropped() uint64 { return h.dropped.Load() }

// OnDrop installs a nonblocking hook before the hub is used. Called once per dropped value.
func (h *LiveHub[T]) OnDrop(f func()) { h.onDrop = f }

// Subscribe joins a confirmed tenant feed. Empty keys subscribe to every value.
// It buffers 256 upstream messages, each as one ordered batch of matching values.
// Each subscriber owns its batch slices; the values themselves remain shared read-only.
// Cancellation removes the subscriber before closing its buffered channel.
func (h *LiveHub[T]) Subscribe(ctx context.Context, tenant string, keys []string) (<-chan []T, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		binding := tenant
		if h.binding != nil {
			var err error
			binding, err = h.binding(tenant)
			if err != nil {
				return nil, err
			}
		}
		h.mu.Lock()
		t := h.tenants[tenant]
		if t == nil {
			t = &liveTenant[T]{}
			h.tenants[tenant] = t
		}
		h.mu.Unlock()
		// Never hold the directory lock while acquiring the tenant lock. Teardown
		// uses the reverse direction and marks stale entries dead before removal.
		// Neither lock is held across open; ready serializes joiners of that feed.
		t.mu.Lock()
		if t.dead {
			t.mu.Unlock()
			continue
		}
		if h.max > 0 && t.count >= h.max {
			err := limit.Exceeded("live subscriptions", t.count+1, h.max)
			t.mu.Unlock()
			return nil, err
		}
		f := t.current
		if f == nil || f.dead || f.binding != binding {
			// Existing subscribers stay on their old binding until they leave;
			// they may go silent after a move, as before shared hubs existed.
			f = &liveFeed[T]{binding: binding, ready: make(chan struct{}), subs: make(map[*liveSubscriber[T]]struct{}), keys: make(map[string]map[*liveSubscriber[T]]struct{}), wild: make(map[*liveSubscriber[T]]struct{})}
			t.current = f
			t.feeds++
			upctx, cancel := context.WithCancel(context.Background())
			t.mu.Unlock()
			if h.binding != nil {
				upctx = context.WithValue(upctx, livePinKey{}, livePin{tenant: tenant, suffix: h.pinSuffix, live: binding})
			}
			up, err := h.open(upctx, tenant)
			t.mu.Lock()
			close(f.ready)
			f.ready = nil
			if err != nil {
				cancel()
				h.stopLocked(tenant, t, f)
				t.mu.Unlock()
				return nil, err
			}
			f.cancel = cancel
			// Another binding can finish opening while this one is outside the lock.
			if h.max > 0 && t.count >= h.max {
				err := limit.Exceeded("live subscriptions", t.count+1, h.max)
				h.stopLocked(tenant, t, f)
				t.mu.Unlock()
				return nil, err
			}
			go h.run(upctx, tenant, t, f, up)
		} else if f.ready != nil {
			ready := f.ready
			t.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ready:
			}
			continue
		}
		s := &liveSubscriber[T]{out: make(chan []T, liveBuffer), done: make(chan struct{}), keys: make(map[string]struct{})}
		f.subs[s] = struct{}{}
		t.count++
		for _, key := range keys {
			s.keys[key] = struct{}{}
			if f.keys[key] == nil {
				f.keys[key] = make(map[*liveSubscriber[T]]struct{})
			}
			f.keys[key][s] = struct{}{}
		}
		if len(keys) == 0 {
			f.wild[s] = struct{}{}
		}
		t.mu.Unlock()
		go func() {
			select {
			case <-ctx.Done():
			case <-s.done:
				return
			}
			t.mu.Lock()
			h.removeLocked(t, f, s)
			if len(f.subs) == 0 && !f.dead {
				h.stopLocked(tenant, t, f)
			}
			t.mu.Unlock()
		}()
		return s.out, nil
	}
}

func (h *LiveHub[T]) removeLocked(t *liveTenant[T], f *liveFeed[T], s *liveSubscriber[T]) {
	if _, ok := f.subs[s]; !ok {
		return
	}
	delete(f.subs, s)
	t.count--
	delete(f.wild, s)
	for key := range s.keys {
		delete(f.keys[key], s)
		if len(f.keys[key]) == 0 {
			delete(f.keys, key)
		}
	}
	close(s.out)
	close(s.done)
}

func (h *LiveHub[T]) stopLocked(tenant string, t *liveTenant[T], f *liveFeed[T]) {
	f.dead = true
	if f.cancel != nil {
		f.cancel()
	}
	for s := range f.subs {
		h.removeLocked(t, f, s)
	}
	t.feeds--
	if t.feeds != 0 {
		return
	}
	t.dead = true
	h.mu.Lock()
	if h.tenants[tenant] == t {
		delete(h.tenants, tenant)
	}
	h.mu.Unlock()
}

func (h *LiveHub[T]) run(ctx context.Context, tenant string, t *liveTenant[T], f *liveFeed[T], up <-chan Message) {
	defer func() {
		t.mu.Lock()
		if !f.dead {
			h.stopLocked(tenant, t, f)
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
			if f.dead {
				t.mu.Unlock()
				return
			}
			batches := make(map[*liveSubscriber[T]][]T)
			for _, v := range values {
				for s := range f.keys[v.Key] {
					batches[s] = append(batches[s], v.Value)
				}
				for s := range f.wild {
					batches[s] = append(batches[s], v.Value)
				}
			}
			for s, batch := range batches {
				h.send(s, batch)
			}
			t.mu.Unlock()
		}
	}
}

func (h *LiveHub[T]) send(s *liveSubscriber[T], batch []T) {
	select {
	case s.out <- batch:
	default:
		h.dropped.Add(uint64(len(batch)))
		if h.onDrop != nil {
			for range batch {
				h.onDrop()
			}
		}
	}
}

// livePinKey carries a shared hub's resolved placement to SubscribeLive, so the
// feed it opens listens on the very group the hub compared against even if the
// placement moves while the subscription is being confirmed with the broker.
type livePinKey struct{}

type livePin struct{ tenant, suffix, live string }

// pinnedLiveSuffix returns the live suffix pinned for exactly this tenant and suffix.
func pinnedLiveSuffix(ctx context.Context, tenant, suffix string) (string, bool) {
	pin, ok := ctx.Value(livePinKey{}).(livePin)
	if !ok || pin.tenant != tenant || pin.suffix != suffix {
		return "", false
	}
	return pin.live, true
}
