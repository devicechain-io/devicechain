// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// What the fix records about a dead connection: which detector gave it up, counted ONCE,
// the time it happened (what the alert reads), and a log line saying which. Reached from
// the scenarios in broker_liveness_test.go, at each of their phases.
func init() {
	livenessAttribution = checkLivenessAttribution
}

func checkLivenessAttribution(t *testing.T, logs *dctest.LogSink, nmgr *NatsManager, detectedBy string) {
	t.Helper()
	counted := func(by string) float64 {
		return testutil.ToFloat64(nmgr.metrics.connectionDead.WithLabelValues(by))
	}
	last := testutil.ToFloat64(nmgr.metrics.connectionDeadLast)

	if detectedBy == "" {
		if p, w := counted(deadByPing), counted(deadByWrite); p != 0 || w != 0 || last != 0 {
			t.Fatalf("a healthy connection was counted dead: ping=%v write=%v last=%v", p, w, last)
		}
		return
	}

	// The handler counts asynchronously, after the status change the scenario waited for.
	var want, other, msg string
	switch detectedBy {
	case deadByPing:
		want, other, msg = deadByPing, deadByWrite, "Disconnected from NATS: the server left 2 pings sent 10s apart unanswered"
	case deadByWrite:
		want, other, msg = deadByWrite, deadByPing, "Disconnected from NATS: a write to the server made no progress for 10s"
	default:
		t.Fatalf("unknown detector %q", detectedBy)
	}
	waitFor(t, "the disconnect record naming "+want, func() bool { return ownLog(logs, nmgr, msg) != nil })
	if got := counted(want); got != 1 {
		t.Errorf("nats_connection_dead_total{detected_by=%q} = %v, want 1", want, got)
	}
	// One death is one count: a stale ping racing the closer for the same connection
	// must not count it again under the other label.
	if got := counted(other); got != 0 {
		t.Errorf("nats_connection_dead_total{detected_by=%q} = %v, want 0", other, got)
	}
	if now := float64(time.Now().Unix()); last < now-120 || last > now+1 {
		t.Errorf("nats_connection_dead_last_timestamp_seconds = %v, want about %v", last, now)
	}

	stall := ownLog(logs, nmgr, "A write to the NATS server made no progress")
	if detectedBy == deadByPing {
		if stall != nil {
			t.Errorf("an idle connection given up by its ping logged a write stall: %v", stall)
		}
		return
	}
	if stall == nil {
		t.Fatal("no stall record from the closer for the connection it closed")
	}
	if server, _ := stall["server"].(string); "nats://"+server != nmgr.NatsUrl() {
		t.Errorf("the stall record names server %q, want the one dialled (%s)", server, nmgr.NatsUrl())
	}
}

// fakeConn is a net.Conn whose writes fail with a chosen error, recording what was done.
type fakeConn struct {
	net.Conn
	writeErr error
	closes   int
}

func (f *fakeConn) Write(p []byte) (int, error)      { return 3, f.writeErr }
func (f *fakeConn) Close() error                     { f.closes++; return nil }
func (f *fakeConn) SetDeadline(time.Time) error      { return nil }
func (f *fakeConn) SetWriteDeadline(time.Time) error { return nil }
func (f *fakeConn) RemoteAddr() net.Addr             { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4222} }

// The closer closes on a TRAFFIC write that timed out, once, and on nothing else: not on
// another error (already a dead socket the read loop reports), and not on the shorter
// deadlines that reach the same connection while nats.go is connecting (its whole-connect
// deadline) or already giving the connection up (crypto/tls's close_notify on Close), each
// of which would otherwise count one death twice, or a connect failure as a death.
func TestStallClosingConnClosesOnlyOnAStalledTrafficWrite(t *testing.T) {
	newConn := func(err error) (*stallClosingConn, *fakeConn, *int) {
		f := &fakeConn{writeErr: err}
		stalls := new(int)
		return &stallClosingConn{Conn: f, connection: "t", onStall: func() { *stalls++ }}, f, stalls
	}
	write := func(t *testing.T, c *stallClosingConn, armed time.Duration) {
		t.Helper()
		if armed > 0 {
			_ = c.SetWriteDeadline(time.Now().Add(armed))
		}
		n, err := c.Write([]byte("abc"))
		if n != 3 || !errors.Is(err, c.Conn.(*fakeConn).writeErr) {
			t.Fatalf("Write = (%d, %v), want the wrapped conn's (3, %v) passed through", n, err, c.Conn.(*fakeConn).writeErr)
		}
	}

	t.Run("a traffic write that timed out closes once", func(t *testing.T) {
		c, f, stalls := newConn(os.ErrDeadlineExceeded)
		write(t, c, BrokerWriteTimeout)
		write(t, c, BrokerWriteTimeout)
		if f.closes != 1 || *stalls != 1 {
			t.Fatalf("closes=%d stalls=%d after two timed-out writes, want 1 and 1", f.closes, *stalls)
		}
	})
	t.Run("a timeout under SetDeadline's connect deadline does not", func(t *testing.T) {
		c, f, stalls := newConn(os.ErrDeadlineExceeded)
		_ = c.SetDeadline(time.Now().Add(2 * time.Second)) // nats.go's Opts.Timeout
		write(t, c, 0)
		if f.closes != 0 || *stalls != 0 {
			t.Fatalf("closes=%d stalls=%d for a connect-deadline timeout, want 0 and 0", f.closes, *stalls)
		}
	})
	t.Run("a timeout under close_notify's deadline does not", func(t *testing.T) {
		c, f, stalls := newConn(os.ErrDeadlineExceeded)
		write(t, c, 5*time.Second) // crypto/tls closeNotify
		if f.closes != 0 || *stalls != 0 {
			t.Fatalf("closes=%d stalls=%d for a close_notify timeout, want 0 and 0", f.closes, *stalls)
		}
	})
	t.Run("a write after the deadline was cleared does not", func(t *testing.T) {
		c, f, stalls := newConn(os.ErrDeadlineExceeded)
		_ = c.SetWriteDeadline(time.Now().Add(BrokerWriteTimeout))
		_ = c.SetWriteDeadline(time.Time{})
		write(t, c, 0)
		if f.closes != 0 || *stalls != 0 {
			t.Fatalf("closes=%d stalls=%d with no deadline armed, want 0 and 0", f.closes, *stalls)
		}
	})
	t.Run("another error does not", func(t *testing.T) {
		c, f, stalls := newConn(errors.New("broken pipe"))
		write(t, c, BrokerWriteTimeout)
		if f.closes != 0 || *stalls != 0 {
			t.Fatalf("closes=%d stalls=%d for a non-timeout error, want 0 and 0", f.closes, *stalls)
		}
	})
	t.Run("a nil onStall still closes", func(t *testing.T) {
		f := &fakeConn{writeErr: os.ErrDeadlineExceeded}
		c := &stallClosingConn{Conn: f, connection: "t"}
		write(t, c, BrokerWriteTimeout)
		if f.closes != 1 {
			t.Fatalf("closes=%d, want 1", f.closes)
		}
	})
}

// The figures the HELP text quotes come from the constants, not from a literal of their own.
func TestTheDeadConnectionHelpQuotesTheConstants(t *testing.T) {
	_, reg := cacheMetricsFor(t)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if !strings.HasSuffix(mf.GetName(), "_nats_connection_dead_total") {
			continue
		}
		help := mf.GetHelp()
		for _, want := range []string{"2 pings sent 10s apart", "no progress for 10s"} {
			if !strings.Contains(help, want) {
				t.Errorf("HELP %q does not say %q", help, want)
			}
		}
		if n := len(mf.GetMetric()); n != 2 {
			t.Errorf("%s has %d series at construction, want 2 (ping and write, at 0)", mf.GetName(), n)
		}
		return
	}
	t.Fatal("nats_connection_dead_total is not registered")
}
