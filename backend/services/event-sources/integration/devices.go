// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// DeviceConfig tunes the device lookup cache. A zero field takes its default.
type DeviceConfig struct {
	// Positive is how long a resolved device is remembered.
	Positive time.Duration
	// Negative is how long an unregistered identifier is remembered. It is also the bound on
	// how often an unknown device is recorded as a sighting: at most once per Negative per
	// (integration, identifier) per replica.
	Negative time.Duration
	Entries  int
	Now      func() time.Time
}

func (c DeviceConfig) withDefaults() DeviceConfig {
	if c.Positive <= 0 {
		c.Positive = time.Hour
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
// is enabled and that the device's type is in its scope when the event reaches its resolver, so
// a device moved out of scope, a rotated token or a disabled integration is refused at the next
// event whatever this holds. What the cache saves is the round trip, and what it bounds is the
// sighting write for an unregistered device.
//
// Only RESOLVED and UNREGISTERED are remembered. A DISABLED or UNAUTHENTICATED answer is about
// the credential, not the device, and is asked again.
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

// Lookup is a device lookup's answer. Cached says no call was made.
type Lookup struct {
	Resolution
	Cached bool
}

// Resolve looks the identifier up. An error is a failure to ask or to understand the answer, and
// must be answered 503: it says nothing about the sender.
func (d *Devices) Resolve(ctx context.Context, tenant, integration, bearer, scheme, value string) (Lookup, error) {
	key := tenant + "\x00" + integration + "\x00" + scheme + "\x00" + value
	now := d.cfg.Now()
	if e, ok := d.get(key, now); ok {
		return Lookup{Resolution: e.res, Cached: true}, nil
	}
	v, err, _ := d.sf.Do(key, func() (any, error) {
		res, err := d.backend.Resolve(context.WithoutCancel(ctx), tenant, integration, bearer, scheme, value)
		if err != nil {
			return nil, err
		}
		if res.Outcome == Resolved || res.Outcome == Unregistered {
			d.put(key, deviceEntry{res: res, at: d.cfg.Now()})
		}
		return res, nil
	})
	if err != nil {
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

func (d *Devices) get(key string, now time.Time) (deviceEntry, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.entries[key]
	if !ok {
		return deviceEntry{}, false
	}
	if now.Sub(e.at) >= d.ttl(e.res.Outcome) {
		delete(d.entries, key)
		return deviceEntry{}, false
	}
	return e, true
}

func (d *Devices) put(key string, e deviceEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.entries[key]; !ok && len(d.entries) >= d.cfg.Entries {
		for k, old := range d.entries {
			if e.at.Sub(old.at) >= d.ttl(old.res.Outcome) {
				delete(d.entries, k)
			}
		}
		for k := range d.entries {
			if len(d.entries) < d.cfg.Entries {
				break
			}
			delete(d.entries, k)
		}
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
