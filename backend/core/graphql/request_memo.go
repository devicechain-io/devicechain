// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"sync"
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

// WithRequestMemo returns ctx carrying a fresh, empty request memo.
func WithRequestMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestMemoKey{}, &requestMemo{values: map[any]*memoEntry{}})
}

// Memoize returns the value compute produced for key in this request, computing it on the
// first call. An error is memoized too: a request that failed to read something once
// fails every field that needs it the same way, rather than retrying per row. key should
// be an unexported type of the calling package, as with context keys.
func Memoize[T any](ctx context.Context, key any, compute func() (T, error)) (T, error) {
	memo, _ := ctx.Value(requestMemoKey{}).(*requestMemo)
	if memo == nil {
		return compute()
	}
	memo.mu.Lock()
	entry, ok := memo.values[key]
	if !ok {
		entry = &memoEntry{}
		memo.values[key] = entry
	}
	memo.mu.Unlock()
	entry.once.Do(func() {
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
	delete(memo.values, key)
	memo.mu.Unlock()
}
