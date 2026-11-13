// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

type memoTestKey struct{}

func TestMemoizeComputesOncePerMemo(t *testing.T) {
	var calls atomic.Int32
	compute := func() (int, error) { return int(calls.Add(1)), nil }

	ctx := WithRequestMemo(context.Background())
	for i := 0; i < 3; i++ {
		v, err := Memoize(ctx, memoTestKey{}, compute)
		if err != nil || v != 1 {
			t.Fatalf("call %d: got %d, %v; want the first computation (1)", i, v, err)
		}
	}
	// A second memo (a second request) computes afresh.
	if v, _ := Memoize(WithRequestMemo(context.Background()), memoTestKey{}, compute); v != 2 {
		t.Fatalf("a new memo reused another's value: got %d", v)
	}
	// No memo at all computes every time.
	Memoize(context.Background(), memoTestKey{}, compute)
	Memoize(context.Background(), memoTestKey{}, compute)
	if got := calls.Load(); got != 4 {
		t.Fatalf("a context with no memo must compute every call: %d computations, want 4", got)
	}
}

func TestMemoizeRemembersAnError(t *testing.T) {
	var calls int
	boom := errors.New("boom")
	ctx := WithRequestMemo(context.Background())
	for i := 0; i < 2; i++ {
		if _, err := Memoize(ctx, memoTestKey{}, func() (string, error) { calls++; return "", boom }); !errors.Is(err, boom) {
			t.Fatalf("call %d: got %v, want boom", i, err)
		}
	}
	if calls != 1 {
		t.Fatalf("a failed computation was retried within the request: %d calls", calls)
	}
}

// memoRoot resolves `twice` by memoizing the same key from two fields of one request.
type memoRoot struct{ calls *atomic.Int32 }

func (r *memoRoot) A(ctx context.Context) (int32, error) { return r.memo(ctx) }
func (r *memoRoot) B(ctx context.Context) (int32, error) { return r.memo(ctx) }
func (r *memoRoot) memo(ctx context.Context) (int32, error) {
	return Memoize(ctx, memoTestKey{}, func() (int32, error) { return r.calls.Add(1), nil })
}

// The HTTP handler installs a FRESH memo per request: two fields of one request share a
// computation, and the next request does not see it.
func TestServeHTTPInstallsAMemoPerRequest(t *testing.T) {
	var calls atomic.Int32
	schema := MustParseSchema(`schema { query: Query } type Query { a: Int! b: Int! }`, &memoRoot{calls: &calls})
	srv := httptest.NewServer(NewHttpHandler(schema, map[ContextKey]interface{}{}, nil))
	defer srv.Close()

	post := func() map[string]int32 {
		body, _ := json.Marshal(map[string]string{"query": "{a b}"})
		resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out struct {
			Data map[string]int32
		}
		if err := json.Unmarshal(raw, &out); err != nil || out.Data == nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return out.Data
	}
	first := post()
	if first["a"] != 1 || first["b"] != 1 {
		t.Fatalf("one request computed twice: %v", first)
	}
	if second := post(); second["a"] != 2 || second["b"] != 2 {
		t.Fatalf("a request reused the previous request's memo: %v", second)
	}
}

func TestForgetMemoRecomputes(t *testing.T) {
	var calls int
	compute := func() (int, error) { calls++; return calls, nil }
	ctx := WithRequestMemo(context.Background())
	Memoize(ctx, memoTestKey{}, compute)
	ForgetMemo(ctx, memoTestKey{})
	if v, _ := Memoize(ctx, memoTestKey{}, compute); v != 2 {
		t.Fatalf("a forgotten key was not recomputed: got %d", v)
	}
	ForgetMemo(context.Background(), memoTestKey{}) // no memo: nothing to do, no panic
}

// One request memo holds one value PER TENANT for a key: a context re-scoped to another
// tenant inside a request never reads the first tenant's value.
func TestMemoizeKeepsTenantsApart(t *testing.T) {
	ctx := WithRequestMemo(context.Background())
	acme, globex := core.WithTenant(ctx, "acme"), core.WithTenant(ctx, "globex")
	a, _ := Memoize(acme, memoTestKey{}, func() (string, error) { return "acme-value", nil })
	g, _ := Memoize(globex, memoTestKey{}, func() (string, error) { return "globex-value", nil })
	if a != "acme-value" || g != "globex-value" {
		t.Fatalf("tenants shared a memoized value: acme=%q globex=%q", a, g)
	}
	ForgetMemo(acme, memoTestKey{})
	if v, _ := Memoize(globex, memoTestKey{}, func() (string, error) { return "recomputed", nil }); v != "globex-value" {
		t.Fatalf("forgetting acme's value forgot globex's: %q", v)
	}
	if v, _ := Memoize(acme, memoTestKey{}, func() (string, error) { return "recomputed", nil }); v != "recomputed" {
		t.Fatalf("acme's forgotten value was not recomputed: %q", v)
	}
}

// A computation that panics is memoized as an ERROR, not left half-done: later calls see
// the error, and none of them panics or reads a zero value as if it were real.
func TestMemoizeTurnsAPanicIntoAnError(t *testing.T) {
	ctx := WithRequestMemo(context.Background())
	_, err := Memoize(ctx, memoTestKey{}, func() (int, error) { panic("boom") })
	if err == nil {
		t.Fatal("a panicking computation returned no error")
	}
	v, err := Memoize(ctx, memoTestKey{}, func() (int, error) { return 7, nil })
	if err == nil {
		t.Fatalf("the panicked entry answered %d as if it had computed", v)
	}
}
