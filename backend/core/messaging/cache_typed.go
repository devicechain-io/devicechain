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
// A type that cannot be changed once built needs no clone: see GetShared.
//
// A decode error is returned as Get returns it, as (nil, false, err).
func GetCloned[T any](ctx context.Context, c *Cache, key string, clone func(*T) *T) (*T, bool, error) {
	return getDecoded(ctx, c, key, decodeJSON[T], clone)
}

// GetClonedFromMemory is GetFromMemory for GetCloned: it asks process memory alone, never
// the bucket, and reports found only for a live entry.
func GetClonedFromMemory[T any](c *Cache, key string, clone func(*T) *T) (*T, bool, error) {
	return getDecodedFromMemory(c, key, decodeJSON[T], clone)
}

// GetShared is GetCloned for a type whose values cannot be changed once built: every read
// of an entry returns the SAME *T, the one decode kept beside the bytes, with no copy.
//
// decode builds a T from the entry's bytes and returns it non-nil, or an error. It runs once per entry (and again only for an
// entry whose kept value is another type, as in GetCloned), so a T may do its one-off work
// there, such as building an index, instead of on every read.
//
// 🔴 T ITSELF IS THE WHOLE ALIASING GUARANTEE, and it must be one the compiler gives: no
// exported field, no method that writes to it, and nothing handed out that shares memory
// with it (a slice, a map, or a pointer to something writable). A T that only promises
// not to be written to belongs in GetCloned. Everything else (key, TTL, bucket fallback,
// eviction, bounds, one type per key, a decode error) is as in GetCloned. A Set or Delete
// replaces or removes the entry, and a caller still holding the old value keeps reading it
// whole: the new value is a new T, never a change to the old one.
func GetShared[T any](ctx context.Context, c *Cache, key string, decode func([]byte) (*T, error)) (*T, bool, error) {
	return getDecoded(ctx, c, key, decode, shared[T])
}

// GetSharedFromMemory is GetFromMemory for GetShared: it asks process memory alone, never
// the bucket, and reports found only for a live entry.
func GetSharedFromMemory[T any](c *Cache, key string, decode func([]byte) (*T, error)) (*T, bool, error) {
	return getDecodedFromMemory(c, key, decode, shared[T])
}

func shared[T any](v *T) *T { return v }

func decodeJSON[T any](raw []byte) (*T, error) {
	v := new(T)
	if err := json.Unmarshal(raw, v); err != nil {
		return nil, err
	}
	return v, nil
}

// getDecoded is the read GetCloned and GetShared share; out is what a caller is handed for
// the kept value.
func getDecoded[T any](ctx context.Context, c *Cache, key string, decode func([]byte) (*T, error),
	out func(*T) *T) (*T, bool, error) {
	if hit, ok := c.local.getHit(key, c.now()); ok {
		return fromHit(c, key, hit, decode, out)
	}
	value, gen, found, err := c.fetch(ctx, key)
	if err != nil || !found {
		return nil, false, err
	}
	v, err := decode(value)
	if err != nil {
		return nil, false, err
	}
	// A copy of the bytes: see Get.
	if e := c.local.fill(key, bytes.Clone(value), c.now(), gen); e != nil {
		c.local.attach(key, e, v)
	}
	return out(v), true, nil
}

func getDecodedFromMemory[T any](c *Cache, key string, decode func([]byte) (*T, error),
	out func(*T) *T) (*T, bool, error) {
	hit, ok := c.local.peekHit(key, c.now())
	if !ok {
		return nil, false, nil
	}
	return fromHit(c, key, hit, decode, out)
}

func fromHit[T any](c *Cache, key string, hit localHit, decode func([]byte) (*T, error),
	out func(*T) *T) (*T, bool, error) {
	if hit.val != nil {
		if p, ok := hit.val.v.(*T); ok {
			return out(p), true, nil
		}
	}
	v, err := decode(hit.data)
	if err != nil {
		return nil, false, err
	}
	c.local.attach(key, hit.entry, v)
	return out(v), true, nil
}
