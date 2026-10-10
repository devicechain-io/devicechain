// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	// DefaultRequestBodyReadTimeout bounds how long a client may take to deliver a
	// request body to a handler wrapped by RequestDeadlines. The bodies these handlers
	// accept are small (an OAuth form, a JSON mint request, a logo of at most a few
	// MiB), so a client that cannot send one in this time is not one worth holding a
	// connection for. It is the same bound the GraphQL handler applies to its body.
	DefaultRequestBodyReadTimeout = 30 * time.Second
	// DefaultRequestExecTimeout bounds one request's execution: the request context
	// ends at this deadline, and the database driver cancels a statement in flight
	// with it. Every handler wrapped today finishes in well under a second.
	DefaultRequestExecTimeout = 60 * time.Second
	// BodyBufferBudget is the most request-body memory BufferBody holds at once, across
	// every handler in the process. Each request reserves its whole ceiling (not what
	// it turns out to send) before reading a byte, and a request that does not fit is
	// answered 503 rather than queued. Without it, the per-request ceiling bounds one
	// request but not how many are buffered together, and a process that runs out of
	// memory takes every endpoint it serves down with it — for user-management, sign-in
	// across the whole instance.
	BodyBufferBudget = 64 << 20
)

// bodyBudget is the process-wide reservation BufferBody draws on. A variable only so a
// test can install a smaller one; nothing else assigns it.
var bodyBudget = newByteBudget(BodyBufferBudget)

// byteBudget is a non-blocking counting reservation of bytes.
type byteBudget struct {
	mu        sync.Mutex
	capacity  int64
	allocated int64
}

func newByteBudget(capacity int64) *byteBudget { return &byteBudget{capacity: capacity} }

func (b *byteBudget) tryAcquire(n int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.allocated+n > b.capacity {
		return false
	}
	b.allocated += n
	return true
}

func (b *byteBudget) release(n int64) {
	b.mu.Lock()
	b.allocated -= n
	b.mu.Unlock()
}

// RequestBounds configures RequestDeadlines and BoundRequests. A zero timeout means the
// package default, so neither can be made unlimited by leaving it unset.
type RequestBounds struct {
	// MaxBodyBytes is the most of a body BoundRequests buffers before handing the
	// request on; it is required there and unused by RequestDeadlines. See BufferBody.
	MaxBodyBytes int64
	// BodyReadTimeout is the deadline on delivering the body. Zero means
	// DefaultRequestBodyReadTimeout.
	BodyReadTimeout time.Duration
	// ExecTimeout is the deadline put on the request context. Zero means
	// DefaultRequestExecTimeout.
	ExecTimeout time.Duration
	// NoExecTimeout leaves the request context without a deadline. It is for a
	// response that is a long-lived stream by design (an SSE stream a client holds
	// open for server-sent messages), which an execution deadline would sever. The
	// body-read deadline still applies.
	NoExecTimeout bool
}

// BoundRequests is RequestDeadlines around BufferBody: the body is buffered to its end
// under the read deadline before next runs. It suits a handler that reads its body
// before deciding anything (an OAuth form, a mint request) or takes none. A handler
// that authenticates first should take RequestDeadlines outside its authentication
// and BufferBody (or its own bounded read) inside it, so an unauthenticated caller
// never gets a buffer.
func BoundRequests(next http.Handler, b RequestBounds) http.Handler {
	return RequestDeadlines(BufferBody(next, b.MaxBodyBytes), b)
}

// RequestDeadlines wraps next with a body-delivery deadline and an execution deadline,
// for the plain HTTP handlers that do not go through the GraphQL handler (which
// applies the same two bounds itself).
//
// Without them only the server's header timeout applies: a client that sends its
// headers promptly and then trickles, or simply withholds, the body it declared holds
// the connection and its goroutine for as long as it likes, and so does a handler
// blocked on a slow dependency.
//
// The read deadline is set before next runs and cleared the moment the body is read
// to its end, wherever that read happens. A body that was NOT read to its end — the
// handler refused before reading it, the read failed, or the body ran past a ceiling —
// gets its deadline moved to now when next returns. That matters as much as the
// deadline itself: net/http discards the unread rest of a body after the handler
// returns, and with the deadline cleared it would wait on a stalled client for ever
// before answering. Expired, the discard fails at once and the connection is closed.
// A ResponseWriter that cannot set a read deadline (a test recorder) is served without
// one.
func RequestDeadlines(next http.Handler, b RequestBounds) http.Handler {
	bodyTimeout := b.BodyReadTimeout
	if bodyTimeout <= 0 {
		bodyTimeout = DefaultRequestBodyReadTimeout
	}
	execTimeout := b.ExecTimeout
	if execTimeout <= 0 {
		execTimeout = DefaultRequestExecTimeout
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !b.NoExecTimeout {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, execTimeout)
			defer cancel()
		}
		// A shallow copy, so the body swap below is not written into the request
		// net/http keeps for itself.
		r = r.WithContext(ctx)
		var tracked *deadlineBody
		if r.Body != nil && r.Body != http.NoBody {
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Now().Add(bodyTimeout))
			tracked = &deadlineBody{ReadCloser: r.Body, rc: rc}
			r.Body = tracked
		}
		next.ServeHTTP(w, r)
		if tracked != nil && !tracked.eof {
			_ = tracked.rc.SetReadDeadline(time.Now())
		}
	})
}

// deadlineBody clears the read deadline once the body has been read to its end, so it
// does not cut the connection afterwards (net/http reads it in the background to
// notice a client that goes away).
type deadlineBody struct {
	io.ReadCloser
	rc  *http.ResponseController
	eof bool
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF && !b.eof {
		b.eof = true
		_ = b.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}

// BufferBody reads the body to its end, up to maxBodyBytes, and hands it to next from
// memory. It relies on RequestDeadlines outside it for the read deadline.
//
// The buffer is reserved against the process-wide BodyBufferBudget first; a request
// that does not fit is answered 503 with Retry-After and the connection closed. A body
// that is not delivered is answered 400 with the connection closed. A body longer than
// maxBodyBytes reaches next cut at maxBodyBytes+1 bytes — so next sees it is too long
// and refuses it in its own terms — and the connection is closed rather than drained.
//
// It panics when maxBodyBytes is not positive: an unbounded read is the thing this
// exists to prevent, so a wrapper built without a ceiling is a programming error that
// should stop the process at registration rather than serve.
func BufferBody(next http.Handler, maxBodyBytes int64) http.Handler {
	if maxBodyBytes <= 0 {
		panic("core.BufferBody: MaxBodyBytes must be positive")
	}
	reserve := maxBodyBytes + 1
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		if !bodyBudget.tryAcquire(reserve) {
			w.Header().Set("Connection", "close")
			w.Header().Set("Retry-After", "1")
			http.Error(w, "the server is busy; retry shortly", http.StatusServiceUnavailable)
			return
		}
		defer bodyBudget.release(reserve)
		body, err := io.ReadAll(io.LimitReader(r.Body, reserve))
		if err != nil {
			w.Header().Set("Connection", "close")
			http.Error(w, "the request body was not received", http.StatusBadRequest)
			return
		}
		if int64(len(body)) > maxBodyBytes {
			// Too long: the handler refuses it, and the rest is never read.
			w.Header().Set("Connection", "close")
		}
		r = r.WithContext(r.Context())
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}
