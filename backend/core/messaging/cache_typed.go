// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"bytes"
	"context"
	"encoding/json"
)

// GetCloned is Get for a caller that wants a *T, answered without decoding when it can be.
//
// The first read of an entry decodes its bytes into a new T, keeps that decoded value in the
// entry beside the bytes, and returns clone of it; later reads of the same entry skip the
// decode and return a fresh clone of the kept value. Everything else is Get's: the key, the
// TTL (the clock starts when the bytes were stored, and attaching a decoded value does not
// restart it), the bucket fallback and breaker, the eviction on Set and Delete, and the
// bound on bytes and entries. A Set or Delete replaces or removes the entry and the
// decoded value with it.
//
// 🔴 clone IS THE WHOLE ALIASING GUARANTEE. The kept value is shared by every caller of the
// entry, so clone must return a value that shares NO memory with its argument: every
// pointer, slice and map reachable from it copied, however deep. A clone that copies a
// struct and leaves one slice shared lets one caller's write into that slice change what
// every later caller reads. It must also return a non-nil result for a non-nil argument
// and must not retain or modify its argument. A key must be read with ONE type T: a read
// of a key whose kept value is some other type decodes the bytes afresh instead.
//
// A decode error is returned as Get returns it, as (nil, false, err).
func GetCloned[T any](ctx context.Context, c *Cache, key string, clone func(*T) *T) (*T, bool, error) {
	if hit, ok := c.local.getHit(key, c.now()); ok {
		return clonedFromHit(c, key, hit, clone)
	}
	value, gen, found, err := c.fetch(ctx, key)
	if err != nil || !found {
		return nil, false, err
	}
	v := new(T)
	if err := json.Unmarshal(value, v); err != nil {
		return nil, false, err
	}
	// A copy of the bytes: see Get.
	if e := c.local.fill(key, bytes.Clone(value), c.now(), gen); e != nil {
		c.local.attach(key, e, v)
	}
	return clone(v), true, nil
}

// GetClonedFromMemory is GetFromMemory for GetCloned: it asks process memory alone, never
// the bucket, and reports found only for a live entry.
func GetClonedFromMemory[T any](c *Cache, key string, clone func(*T) *T) (*T, bool, error) {
	hit, ok := c.local.peekHit(key, c.now())
	if !ok {
		return nil, false, nil
	}
	return clonedFromHit(c, key, hit, clone)
}

func clonedFromHit[T any](c *Cache, key string, hit localHit, clone func(*T) *T) (*T, bool, error) {
	if hit.val != nil {
		if p, ok := hit.val.v.(*T); ok {
			return clone(p), true, nil
		}
	}
	v := new(T)
	if err := json.Unmarshal(hit.data, v); err != nil {
		return nil, false, err
	}
	c.local.attach(key, hit.entry, v)
	return clone(v), true, nil
}
