// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"sync"

	"github.com/devicechain-io/dc-microservice/core"
)

// A request memo holds values a resolver computed once for the whole of ONE GraphQL
// request, so a field resolved on every row of a list does not repeat the same read per
// row. It is the shape for per-request data that is the same for every row: the caller
// tenant's configuration, say, where re-reading it per row would be an N+1 that returns
// N identical answers.
//
// It lives exactly as long as the request: HttpHandler installs a fresh one per request,
// so nothing survives to be served stale to a later request (or to another tenant, since
// one request is one caller). A context with no memo (a unit test, a subscription, which
// is long-lived and must not cache) simply computes the value every time: Memoize is an
// optimisation, never a source of truth.
//
// Keys are TENANT-QUALIFIED: Memoize and ForgetMemo pair the caller's key with the tenant
// in ctx, so a context re-scoped to another tenant inside one request can never read the
// first tenant's value.
//
// 🔴 A MEMOIZED VALUE IS SHARED by every caller in the request, so it must be treated as
// immutable. A caller that needs to change a map or slice it got back copies it first.
type requestMemo struct {
	mu     sync.Mutex
	values map[any]*memoEntry
}

type memoEntry struct {
	once  sync.Once
	value any
	err   error
}

type requestMemoKey struct{}

// tenantKey is a memo key qualified by the tenant in ctx ("" when there is none).
type tenantKey struct {
	tenant string
	key    any
}

func qualify(ctx context.Context, key any) tenantKey {
	tenant, _ := core.TenantFromContext(ctx)
	return tenantKey{tenant: tenant, key: key}
}

// WithRequestMemo returns ctx carrying a fresh, empty request memo.
func WithRequestMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestMemoKey{}, &requestMemo{values: map[any]*memoEntry{}})
}

// Memoize returns the value compute produced for key in this request, computing it on the
// first call. An error is memoized too: a request that failed to read something once
// fails every field that needs it the same way, rather than retrying per row. A compute
// that panics is memoized as an error, so a panic cannot leave an entry that later calls
// read as a computed zero value. key should be an unexported type of the calling package,
// as with context keys; it is qualified by the tenant in ctx.
func Memoize[T any](ctx context.Context, key any, compute func() (T, error)) (T, error) {
	memo, _ := ctx.Value(requestMemoKey{}).(*requestMemo)
	if memo == nil {
		return compute()
	}
	k := qualify(ctx, key)
	memo.mu.Lock()
	entry, ok := memo.values[k]
	if !ok {
		entry = &memoEntry{}
		memo.values[k] = entry
	}
	memo.mu.Unlock()
	entry.once.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				entry.value, entry.err = nil, fmt.Errorf("request memo: computing %T panicked: %v", key, r)
			}
		}()
		entry.value, entry.err = compute()
	})
	if entry.err != nil {
		var zero T
		return zero, entry.err
	}
	return entry.value.(T), nil
}

// ForgetMemo drops what this request memoized for key, so the next Memoize computes it
// again. A mutation that changes what a key was computed from calls it: a document's
// mutation fields run in order, and a field after the change must not read the value from
// before it. A context with no memo has nothing to forget.
func ForgetMemo(ctx context.Context, key any) {
	memo, _ := ctx.Value(requestMemoKey{}).(*requestMemo)
	if memo == nil {
		return
	}
	memo.mu.Lock()
	delete(memo.values, qualify(ctx, key))
	memo.mu.Unlock()
}
