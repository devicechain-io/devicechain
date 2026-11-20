// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// DefaultPositiveTTL is how long a resolved device is remembered. It is the bound on how long
// uplinks for an identifier keep going to a device that has since lost the alias (removed, or moved
// to another device) as far as THIS replica's route is concerned. It does not decide where an
// event lands: device-management re-checks, at the event, that the device still holds the alias
// the event was resolved from, and refuses the event if it does not. So a stale entry costs
// refused (dead-lettered) uplinks until it expires, never data on the wrong device.
//
// Event-sources cannot see device-management's per-event refusal (the event is published to a
// stream and resolved asynchronously, and a refusal is recorded there), so there is no feedback
// to evict an entry on; the TTL alone bounds the window.
//
// A hit does not refresh the entry: the TTL is that bound, and an entry a busy device kept alive
// by its own traffic would never be checked again. Past it the device is asked again, and only
// when the asking FAILS is the old answer served, up to DefaultDeviceStaleMax.
const DefaultPositiveTTL = 5 * time.Minute

// DefaultDeviceStaleMax is the longest a resolved device is served, counted from when
// device-management last answered for it, when device-management cannot be asked. It matches the
// integration's token and decoder caches, so an outage that leaves the sender authenticated and
// its decoder known leaves its devices known too.
const DefaultDeviceStaleMax = 15 * time.Minute

// DeviceConfig tunes the device lookup cache. A zero field takes its default.
type DeviceConfig struct {
	// Positive is how long a resolved device is served without asking device-management again.
	Positive time.Duration
	// StaleMax is the longest a resolved device is served when device-management cannot be asked,
	// counted from when it answered. Never shorter than Positive.
	StaleMax time.Duration
	// Negative is how long an unregistered identifier is remembered. It is also the bound on
	// how often an unknown device is recorded as a sighting: at most once per Negative per
	// (integration, identifier) per replica.
	Negative time.Duration
	Entries  int
	Now      func() time.Time
}

func (c DeviceConfig) withDefaults() DeviceConfig {
	if c.Positive <= 0 {
		c.Positive = DefaultPositiveTTL
	}
	if c.StaleMax <= 0 {
		c.StaleMax = DefaultDeviceStaleMax
	}
	if c.StaleMax < c.Positive {
		c.StaleMax = c.Positive
	}
	if c.Negative <= 0 {
		c.Negative = time.Minute
	}
	if c.Entries <= 0 {
		c.Entries = 100_000
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Devices resolves a sender's identifier to a device token through device-management, remembering
// the answer.
//
// 🔴 THE CACHE IS NOT AN AUTHORITY. Device-management re-checks the integration's token, that it
// is enabled, that the device's type is in its scope and that the device still holds the alias
// the event was resolved from, when the event reaches its resolver, so a device moved out of
// scope, a rotated token, a disabled integration or an alias removed or moved to another device
// is refused at the next event whatever this holds. What the cache saves is the round trip, and what it bounds is the
// sighting write for an unregistered device.
//
// Only RESOLVED and UNREGISTERED are remembered. A DISABLED or UNAUTHENTICATED answer is about
// the credential, not the device, and is asked again.
//
// 🔴 A RESOLVED DEVICE OUTLIVES ITS FRESHNESS, AND ONLY IT. When asking device-management FAILS
// (it is unreachable, times out, or answers something this replica cannot read), a resolved
// device it answered within StaleMax is served, marked Stale, rather than refusing the uplink 503
// while the token and decoder caches beside this one keep serving. That is safe for the reason
// above: the event is parked on its stream and device-management re-checks the integration, the
// device (that it exists and is not disabled) and the alias when it resolves it, so a device
// disabled, deleted or moved during the outage is refused at the event, never stored. What a stale
// serve costs is a refused event where a 503 would have had the sender retry.
//
// Nothing else is served stale. An ANSWER is never overridden by an older one: unregistered,
// disabled or unauthenticated is returned as it came, and it also drops the positive entry, so a
// later outage cannot bring a device back that device-management has since said is not there. An
// unregistered answer past its minute is asked again and its failure is a 503: dropping the uplink
// of a device that may have been registered since would lose it, where the sender retries a 503.
type Devices struct {
	cfg     DeviceConfig
	backend Backend
	sf      singleflight.Group

	mu      sync.Mutex
	entries map[string]deviceEntry
}

type deviceEntry struct {
	res Resolution
	at  time.Time
}

// NewDevices builds the cached lookup over a backend.
func NewDevices(backend Backend, cfg DeviceConfig) *Devices {
	return &Devices{cfg: cfg.withDefaults(), backend: backend, entries: make(map[string]deviceEntry)}
}

// Lookup is a device lookup's answer. Cached says no call answered it.
type Lookup struct {
	Resolution
	Cached bool
	// Stale says the answer is older than the positive TTL and was served because
	// device-management could not be asked. Age is how old it is.
	Stale bool
	Age   time.Duration
}

// Resolve looks the identifier up. An error is a failure to ask or to understand the answer, and
// must be answered 503: it says nothing about the sender.
func (d *Devices) Resolve(ctx context.Context, tenant, integration, bearer, scheme, value string) (Lookup, error) {
	key := tenant + "\x00" + integration + "\x00" + scheme + "\x00" + value
	if e, fresh, ok := d.get(key, d.cfg.Now()); ok && fresh {
		return Lookup{Resolution: e.res, Cached: true}, nil
	}
	v, err, _ := d.sf.Do(key, func() (any, error) {
		res, err := d.backend.Resolve(context.WithoutCancel(ctx), tenant, integration, bearer, scheme, value)
		if err != nil {
			return nil, err
		}
		if res.Outcome == Resolved || res.Outcome == Unregistered {
			d.put(key, deviceEntry{res: res, at: d.cfg.Now()})
		} else {
			d.drop(key)
		}
		return res, nil
	})
	if err != nil {
		// Read again rather than reuse what was held before the call: a Forget or a definitive
		// answer that landed meanwhile has dropped it, and must win.
		now := d.cfg.Now()
		if e, _, ok := d.get(key, now); ok && e.res.Outcome == Resolved {
			return Lookup{Resolution: e.res, Cached: true, Stale: true, Age: now.Sub(e.at)}, nil
		}
		return Lookup{}, err
	}
	return Lookup{Resolution: v.(Resolution)}, nil
}

func (d *Devices) ttl(o Outcome) time.Duration {
	if o == Resolved {
		return d.cfg.Positive
	}
	return d.cfg.Negative
}

// keep is how long an entry is held at all: a resolved device until StaleMax, an unregistered
// identifier only while it is fresh.
func (d *Devices) keep(o Outcome) time.Duration {
	if o == Resolved {
		return d.cfg.StaleMax
	}
	return d.cfg.Negative
}

// get returns the entry for key, whether it is still fresh, and whether there is one at all.
func (d *Devices) get(key string, now time.Time) (e deviceEntry, fresh, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok = d.entries[key]
	if !ok {
		return deviceEntry{}, false, false
	}
	age := now.Sub(e.at)
	if age >= d.keep(e.res.Outcome) {
		delete(d.entries, key)
		return deviceEntry{}, false, false
	}
	return e, age < d.ttl(e.res.Outcome), true
}

func (d *Devices) drop(key string) {
	d.mu.Lock()
	delete(d.entries, key)
	d.mu.Unlock()
}

func (d *Devices) put(key string, e deviceEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.entries[key]; !ok && len(d.entries) >= d.cfg.Entries {
		evictSome(d.entries, d.cfg.Entries/16+1, func(old deviceEntry) bool { return e.at.Sub(old.at) >= d.keep(old.res.Outcome) })
	}
	d.entries[key] = e
}

// Forget drops every remembered answer for an integration, for a caller that has learned its
// credential no longer holds.
func (d *Devices) Forget(tenant, integration string) {
	prefix := tenant + "\x00" + integration + "\x00"
	d.mu.Lock()
	defer d.mu.Unlock()
	for k := range d.entries {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(d.entries, k)
		}
	}
}
