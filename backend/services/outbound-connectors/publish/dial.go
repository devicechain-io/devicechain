// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/devicechain-io/dc-microservice/egress"
)

// maxInboundBytesPerConn caps everything one connection may receive, TLS records
// included. One send's conversation — a handshake, a metadata response, an acknowledgement
// — is far smaller; a destination that keeps talking past it is not delivering, it is
// holding memory or a worker.
const maxInboundBytesPerConn = 1 << 20

// errInboundCap is returned by a connection that received more than
// maxInboundBytesPerConn. It is a delivery failure like any other: retryable.
var errInboundCap = errors.New("publish: the destination sent more than a single send can need")

// dialLog is one send's record of what its dials did. It is shared by every connection the
// send's client opens, from whatever goroutine the client opens it on.
type dialLog struct {
	// ctx is the SEND's context. A client library passes its own context to the dial (for
	// Kafka, one derived from the client, not from the send), so the send's is carried
	// here: every dial is cancelled by it and every connection is closed by it.
	ctx context.Context
	// cancel ends the send. The first refused dial calls it, so a blocked second hop does
	// not leave the rest of the send waiting out its deadline against other addresses.
	cancel context.CancelCauseFunc

	blocked  atomic.Pointer[error]
	terminal atomic.Pointer[error]
}

// block records a refusal and cancels the send. The first refusal wins the record.
func (l *dialLog) block(err error) {
	l.blocked.CompareAndSwap(nil, &err)
	l.cancel(err)
}

// fail records a terminal target error (ErrPublishConfig) the client library may flatten
// or replace before it reaches Send.
func (l *dialLog) fail(err error) {
	l.terminal.CompareAndSwap(nil, &err)
	l.cancel(err)
}

// explain chooses the error a failed send reports: the refusal if any dial was refused,
// then a recorded terminal target error, then the client's own.
func (l *dialLog) explain(clientErr error) error {
	if b := l.blocked.Load(); b != nil {
		return fmt.Errorf("publish refused: %w", *b)
	}
	if t := l.terminal.Load(); t != nil {
		return *t
	}
	return clientErr
}

// dial returns the ONE function every publish connection is made with.
//
//  1. A network other than tcp, tcp4 or tcp6 is refused as blocked. The egress guard
//     judges IP addresses; a unix socket or anything else has none, so nothing may be
//     dialed that the guard did not see.
//  2. The dial runs through a per-call copy of the guard's dialer, whose control hook
//     counts the addresses attempted and refused. Happy Eyeballs and a multi-address
//     host run several attempts, possibly in parallel.
//  3. The call is BLOCKED only when every attempted address was refused. A hostname that
//     resolves to one refused and one reachable address connects to the reachable one;
//     if that one is down, the error is an ordinary (retryable) dial error that does NOT
//     wrap egress.ErrBlocked — even though net.Dialer may have returned the refusal as
//     its first error.
//  4. A blocked call records the refusal and cancels the whole send.
//  5. Every returned connection is capped (capConn) and closed when the send ends.
func (s *Sender) dial(log *dialLog) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		switch network {
		case "tcp", "tcp4", "tcp6":
		default:
			err := fmt.Errorf("%w: network %q is not TCP", egress.ErrBlocked, network)
			log.block(err)
			return nil, err
		}

		// The dial ends when EITHER the client's context or the send's does.
		dctx, stopDial := context.WithCancelCause(ctx)
		defer stopDial(nil)
		stopWatch := context.AfterFunc(log.ctx, func() { stopDial(context.Cause(log.ctx)) })
		defer stopWatch()

		d := *s.guard.Dialer()
		var tally attemptTally
		d.ControlContext = tally.wrap(s.guard.ControlContext)

		conn, err := d.DialContext(dctx, network, addr)
		if err != nil {
			if refusal, all := tally.allRefused(); all {
				log.block(refusal)
				return nil, refusal
			}
			if errors.Is(err, egress.ErrBlocked) {
				// Some address was refused and another was attempted and failed on its own.
				// The destination is not wholly refused, so this must not classify as
				// blocked: flatten the refusal out of the chain.
				return nil, fmt.Errorf("dial %s: %s", addr, err.Error())
			}
			return nil, err
		}
		return newCapConn(log.ctx, conn), nil
	}
}

// attemptTally counts the addresses one dial attempted and the ones the guard refused.
type attemptTally struct {
	mu       sync.Mutex
	attempts int
	refused  int
	first    error
}

func (t *attemptTally) wrap(control func(context.Context, string, string, syscall.RawConn) error) func(context.Context, string, string, syscall.RawConn) error {
	return func(ctx context.Context, network, address string, c syscall.RawConn) error {
		err := control(ctx, network, address, c)
		t.mu.Lock()
		defer t.mu.Unlock()
		t.attempts++
		if err != nil {
			t.refused++
			if t.first == nil {
				t.first = err
			}
		}
		return err
	}
}

// allRefused reports the first refusal when at least one address was attempted and every
// attempted address was refused.
func (t *attemptTally) allRefused() (error, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.attempts > 0 && t.refused == t.attempts {
		return t.first, true
	}
	return nil, false
}

// capConn bounds one publish connection: it is closed when the send's context ends, and it
// refuses to deliver more than maxInboundBytesPerConn to the client above it.
type capConn struct {
	net.Conn
	stop      func() bool
	mu        sync.Mutex
	remaining int
}

func newCapConn(sendCtx context.Context, conn net.Conn) *capConn {
	c := &capConn{Conn: conn, remaining: maxInboundBytesPerConn}
	// No connection outlives the send, whatever the client library does asynchronously
	// (a reconnect goroutine, a teardown it never finishes).
	c.stop = context.AfterFunc(sendCtx, func() { _ = conn.Close() })
	return c
}

func (c *capConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	left := c.remaining
	c.mu.Unlock()
	if left <= 0 {
		return 0, errInboundCap
	}
	if len(p) > left {
		p = p[:left]
	}
	n, err := c.Conn.Read(p)
	c.mu.Lock()
	c.remaining -= n
	c.mu.Unlock()
	return n, err
}

func (c *capConn) Close() error {
	c.stop()
	return c.Conn.Close()
}
