// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// stubSession is one signed-in session. signIn is AccessToken's answer; read is
// Query's, given how many reads this session has taken.
type stubSession struct {
	signIn error
	read   func() error
}

func (s *stubSession) AccessToken(context.Context) (string, error) { return "tok", s.signIn }
func (s *stubSession) Query(context.Context, string, string, map[string]any, any) error {
	return s.read()
}

// fakeClock stands still until a sleep advances it.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.t = c.t.Add(d)
	return nil
}

var unauthorized = &userclient.StatusError{URL: "u", StatusCode: 401, Body: "invalid or expired token"}

// tolerant builds a rotationTolerant over a fake clock; newSession is called for the
// first session and for every retry, and the count of calls is returned through n.
func tolerant(window time.Duration, out io.Writer, newSession func(n int) *stubSession) (*rotationTolerant, *fakeClock, *int) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	n := 0
	r := &rotationTolerant{
		fresh: func() signedInQuerier {
			n++
			return newSession(n)
		},
		window: window,
		delay:  time.Second,
		now:    clock.now,
		sleep:  clock.sleep,
		out:    out,
	}
	r.start = r.now()
	r.current = r.fresh()
	return r, clock, &n
}

// Only a 401 on the read is the rotation's signature. Everything else is returned
// untouched, at once, without a second sign-in and without a line of output.
func TestOnlyA401IsRetried(t *testing.T) {
	for name, readErr := range map[string]error{
		"403":           &userclient.StatusError{URL: "u", StatusCode: 403, Body: "forbidden"},
		"500":           &userclient.StatusError{URL: "u", StatusCode: 500, Body: "boom"},
		"GraphQL error": &userclient.GraphQLError{URL: "u", Messages: []string{"unauthorized"}, Codes: []string{""}},
		"transport":     errors.New("userclient: call u: connection refused"),
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			r, _, n := tolerant(45*time.Second, &out, func(int) *stubSession {
				return &stubSession{read: func() error { return readErr }}
			})
			err := r.Query(context.Background(), "u", "q", nil, nil)
			if err != readErr {
				t.Fatalf("got %v, want the read's own error returned unchanged", err)
			}
			if *n != 1 || out.Len() != 0 || r.retries != 0 {
				t.Fatalf("a %s was retried: sessions=%d retries=%d output=%q", name, *n, r.retries, out.String())
			}
		})
	}
}

// A sign-in that never reached the platform is not something a rotation produces:
// inconclusive at once.
func TestASignInThatWasNotReachedIsNotRetried(t *testing.T) {
	var out bytes.Buffer
	r, _, n := tolerant(45*time.Second, &out, func(int) *stubSession {
		return &stubSession{signIn: errors.New("userclient: call u: connection refused")}
	})
	assertCode(t, r.Query(context.Background(), "u", "q", nil, nil), exitSetup)
	if *n != 1 || out.Len() != 0 {
		t.Fatalf("an unreachable sign-in was retried: sessions=%d output=%q", *n, out.String())
	}
}

// 🔴 ONE budget from the start of verify. Read 1 is refused until +40s; read 2 is
// refused for good. Read 2 must be DENIED inside the same 45s, not given 45s of its own.
func TestTheWindowIsOneBudgetNotPerRead(t *testing.T) {
	var clock *fakeClock
	r, clock, _ := tolerant(45*time.Second, io.Discard, func(int) *stubSession {
		return &stubSession{read: func() error {
			if clock.t.Sub(time.Unix(1_000_000, 0)) < 40*time.Second {
				return unauthorized
			}
			return nil
		}}
	})
	if err := r.Query(context.Background(), "first", "q", nil, nil); err != nil {
		t.Fatalf("read 1 should have been ridden out: %v", err)
	}
	r.fresh = func() signedInQuerier { return &stubSession{read: func() error { return unauthorized }} }
	r.current = r.fresh()

	assertCode(t, r.Query(context.Background(), "second", "q", nil, nil), exitDenied)
	if spent := clock.t.Sub(r.start); spent > 45*time.Second {
		t.Fatalf("read 2 was denied at +%s: the window restarted per read", spent)
	}
}

// The bound is not overrun: a retry starts only if its wait still fits.
func TestTheBoundIsNotOverrun(t *testing.T) {
	for _, window := range []time.Duration{10 * time.Second, 10*time.Second + 500*time.Millisecond} {
		r, clock, _ := tolerant(window, io.Discard, func(int) *stubSession {
			return &stubSession{read: func() error { return unauthorized }}
		})
		assertCode(t, r.Query(context.Background(), "u", "q", nil, nil), exitDenied)
		if r.retries != 10 {
			t.Errorf("window %s: %d retries, want 10", window, r.retries)
		}
		if spent := clock.t.Sub(r.start); spent > window {
			t.Errorf("window %s: waited until +%s", window, spent)
		}
	}
}

// A zero window is no tolerance: the first 401 is DENIED, with no retry and no line.
func TestAZeroWindowRetriesNothing(t *testing.T) {
	var out bytes.Buffer
	r, _, n := tolerant(0, &out, func(int) *stubSession {
		return &stubSession{read: func() error { return unauthorized }}
	})
	assertCode(t, r.Query(context.Background(), "u", "q", nil, nil), exitDenied)
	if *n != 1 || r.retries != 0 || out.Len() != 0 {
		t.Fatalf("a zero window retried: sessions=%d retries=%d output=%q", *n, r.retries, out.String())
	}
}

// Every retry is printed with its elapsed time, and a pass that retried says so.
func TestEveryRetryIsPrintedWithItsElapsedTime(t *testing.T) {
	var out bytes.Buffer
	r, _, _ := tolerant(45*time.Second, &out, func(n int) *stubSession {
		return &stubSession{read: func() error {
			if n < 3 {
				return unauthorized
			}
			return nil
		}}
	})
	if note := r.settledNote(); note != "" {
		t.Fatalf("nothing retried yet, and the note says %q", note)
	}
	if err := r.Query(context.Background(), "http://x/api/a/graphql", "q", nil, nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	line := regexp.MustCompile(`^  retry   401 from http://x/api/a/graphql at \+(\d+(\.\d+)?)s \(retry ([12]), window 45s\); signing in again$`)
	want := []string{"0", "1"}
	if len(lines) != 2 {
		t.Fatalf("got %d retry lines, want 2:\n%s", len(lines), out.String())
	}
	for i, l := range lines {
		m := line.FindStringSubmatch(l)
		if m == nil || m[1] != want[i] {
			t.Fatalf("retry line %d is %q, want elapsed +%ss", i+1, l, want[i])
		}
	}
	if note := r.settledNote(); !strings.HasPrefix(note, "2 request(s) were refused") || !strings.Contains(note, "the last at +1s") {
		t.Fatalf("settledNote = %q", note)
	}
}

// A sign-in the platform refused is retried, and printed as a sign-in.
func TestARefusedSignInIsRetriedAndPrinted(t *testing.T) {
	var out bytes.Buffer
	refused := &userclient.GraphQLError{URL: "u", Messages: []string{"invalid or expired token"}, Codes: []string{""}}
	r, _, n := tolerant(45*time.Second, &out, func(n int) *stubSession {
		if n == 1 {
			return &stubSession{signIn: refused}
		}
		return &stubSession{read: func() error { return nil }}
	})
	if err := r.Query(context.Background(), "u", "q", nil, nil); err != nil {
		t.Fatal(err)
	}
	if *n != 2 || !strings.HasPrefix(out.String(), "  retry   sign-in refused at +0s (retry 1, window 45s): ") {
		t.Fatalf("sessions=%d output=%q", *n, out.String())
	}
}

// A wait cut short says nothing about the platform, and the real wait honours ctx.
func TestACancelledWaitIsInconclusive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := sleepCtx(ctx, 10*time.Second); err == nil || time.Since(start) > time.Second {
		t.Fatalf("sleepCtx ignored a cancelled context: err=%v after %s", err, time.Since(start))
	}

	r, _, _ := tolerant(45*time.Second, io.Discard, func(int) *stubSession {
		return &stubSession{read: func() error { return unauthorized }}
	})
	r.sleep = sleepCtx
	assertCode(t, r.Query(ctx, "u", "q", nil, nil), exitSetup)
}
