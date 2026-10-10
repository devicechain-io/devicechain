// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"
)

const (
	// DefaultRequestBodyReadTimeout bounds how long a client may take to deliver a
	// request body to a handler wrapped by BoundRequests. The bodies these handlers
	// accept are small (an OAuth form, a JSON mint request, a logo of at most a few
	// MiB), so a client that cannot send one in this time is not one worth holding a
	// connection for. It is the same bound the GraphQL handler applies to its body.
	DefaultRequestBodyReadTimeout = 30 * time.Second
	// DefaultRequestExecTimeout bounds one request's execution: the request context
	// ends at this deadline, and the database driver cancels a statement in flight
	// with it. Every handler wrapped today finishes in well under a second.
	DefaultRequestExecTimeout = 60 * time.Second
)

// RequestBounds configures BoundRequests. MaxBodyBytes is required; a zero timeout
// means the package default, and neither timeout can be made unlimited by leaving it
// unset.
type RequestBounds struct {
	// MaxBodyBytes is the most of a body BoundRequests reads before handing the
	// request on. A body longer than this is cut at MaxBodyBytes+1 bytes, so the
	// handler still sees that it is too long and refuses it in its own terms, and the
	// connection is closed after the reply rather than drained.
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

// BoundRequests wraps next with a body-delivery deadline and an execution deadline,
// for the plain HTTP handlers that do not go through the GraphQL handler (which
// applies the same two bounds itself).
//
// Without them only the server's header timeout applies: a client that sends its
// headers promptly and then trickles, or simply withholds, the body it declared holds
// the connection and its goroutine for as long as it likes, and so does a handler
// blocked on a slow dependency.
//
// The body is read here, to EOF, under the deadline, and handed to next from memory.
// Reading it to its end matters as much as the deadline does: a client that sends a
// complete form, declares a longer body and stalls would otherwise be waited on by
// the server, with no deadline, when it discards the unread rest after the handler
// returns. A body that is not delivered in time is answered 400 with the connection
// closed, so the server does not wait on the rest of it either. A ResponseWriter that
// cannot set a read deadline (a test recorder) is served without one.
//
// It panics when MaxBodyBytes is not positive: an unbounded read is the thing this
// exists to prevent, so a wrapper built without a ceiling is a programming error that
// should stop the process at registration rather than serve.
func BoundRequests(next http.Handler, b RequestBounds) http.Handler {
	if b.MaxBodyBytes <= 0 {
		panic("core.BoundRequests: MaxBodyBytes must be positive")
	}
	bodyTimeout := b.BodyReadTimeout
	if bodyTimeout <= 0 {
		bodyTimeout = DefaultRequestBodyReadTimeout
	}
	execTimeout := b.ExecTimeout
	if execTimeout <= 0 {
		execTimeout = DefaultRequestExecTimeout
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Now().Add(bodyTimeout))
			body, err := io.ReadAll(io.LimitReader(r.Body, b.MaxBodyBytes+1))
			_ = rc.SetReadDeadline(time.Time{})
			if err != nil {
				w.Header().Set("Connection", "close")
				http.Error(w, "the request body was not received", http.StatusBadRequest)
				return
			}
			if int64(len(body)) > b.MaxBodyBytes {
				// Too long: the handler refuses it, and the rest is never read.
				w.Header().Set("Connection", "close")
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if !b.NoExecTimeout {
			ctx, cancel := context.WithTimeout(r.Context(), execTimeout)
			defer cancel()
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}
