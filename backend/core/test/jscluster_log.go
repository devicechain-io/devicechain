// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// serverLogHead and serverLogTail are how many lines a serverLog keeps: the first
// serverLogHead lines a server logs and the last serverLogTail after them. The first
// lines are kept as well as the last because the line a failure hinges on is often the
// earliest one, followed by a stream of consequences (a peer that cannot be reached is
// reported again every few seconds) that would push it out of a ring of the latest.
const (
	serverLogHead = 64
	serverLogTail = 192
)

// serverLogInError is how many of each server's last lines a fixture's error carries. The
// whole of what was kept is printed when the test fails (reportServerLogsOnFailure).
const serverLogInError = 10

// routeBindFailure is how a server reports that its route listener could not be opened
// ("Error listening on router port: <port> - <err>"). The fixtures give every listener
// port -1, which the operating system picks as it binds, so no port can be taken from
// under them; what this line reports is a listener that cannot open at all, and a
// fixture fails at once on it (errListenerFailed) rather than wait out its budget.
const routeBindFailure = "Error listening on router port"

// serverLog is a natsserver.Logger that keeps what one server logs at warning level and
// above. The fixtures' servers have no logger otherwise, and the server's own reasons are
// then lost: a route listener that cannot bind is reported only through Fatalf, and a
// clustered stream create that fails in the server's store is reported to the client as
// the fixed sentence "error creating store for stream", with the real cause written only
// to the server's log ("Stream create failed for …").
type serverLog struct {
	name string

	mu      sync.Mutex
	head    []string
	tail    []string // a ring once it is full; next is its oldest line
	next    int
	dropped int
	bind    string // the first route bind failure, kept whatever else is dropped
}

// attachLog gives srv a serverLog and returns it. Call it before srv.Start: a listener
// that cannot bind is reported from inside Start.
func attachLog(srv *natsserver.Server) *serverLog {
	l := &serverLog{name: srv.Name()}
	srv.SetLoggerV2(l, false, false, false)
	return l
}

func (l *serverLog) Noticef(string, ...any) {}
func (l *serverLog) Debugf(string, ...any)  {}
func (l *serverLog) Tracef(string, ...any)  {}

func (l *serverLog) Warnf(format string, v ...any)  { l.add("WRN", format, v...) }
func (l *serverLog) Errorf(format string, v ...any) { l.add("ERR", format, v...) }

// Fatalf keeps the line and returns. It must not exit or panic: the server calls it for a
// listener that could not bind and carries on, and it is the fixture's job to notice.
func (l *serverLog) Fatalf(format string, v ...any) { l.add("FTL", format, v...) }

func (l *serverLog) add(level, format string, v ...any) {
	line := "[" + level + "] " + fmt.Sprintf(format, v...)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.bind == "" && strings.Contains(line, routeBindFailure) {
		l.bind = line
	}
	switch {
	case len(l.head) < serverLogHead:
		l.head = append(l.head, line)
	case len(l.tail) < serverLogTail:
		l.tail = append(l.tail, line)
	default:
		l.tail[l.next] = line
		l.next = (l.next + 1) % serverLogTail
		l.dropped++
	}
}

// snapshot returns the kept lines in the order they were logged, with a line saying how
// many were dropped between the first and the last when any were.
func (l *serverLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.head)+len(l.tail)+1)
	out = append(out, l.head...)
	if l.dropped > 0 {
		out = append(out, fmt.Sprintf("… %d lines dropped …", l.dropped))
	}
	out = append(out, l.tail[l.next:]...)
	out = append(out, l.tail[:l.next]...)
	return out
}

// bindFailure returns the first line reporting that the server's route listener could
// not be opened, or "" when there is none.
func (l *serverLog) bindFailure() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bind
}

// withServerLogs returns err with the last serverLogInError lines of every server that
// logged anything appended, still wrapping err.
func withServerLogs(err error, logs []*serverLog) error {
	var b strings.Builder
	for _, l := range logs {
		lines := l.snapshot()
		if len(lines) > serverLogInError {
			lines = lines[len(lines)-serverLogInError:]
		}
		for _, line := range lines {
			fmt.Fprintf(&b, "\n\t%s: %s", l.name, line)
		}
	}
	if b.Len() == 0 {
		return err
	}
	return fmt.Errorf("%w; the servers last logged:%s", err, b.String())
}

// reportServerLogsOnFailure registers a cleanup that, when tb has failed, logs every line
// each server kept. Register it after the servers' shutdown so it runs first; the lines are
// kept either way, since a test may shut its cluster down itself.
func reportServerLogsOnFailure(tb testing.TB, logs []*serverLog) {
	tb.Cleanup(func() {
		if !tb.Failed() {
			return
		}
		for _, l := range logs {
			lines := l.snapshot()
			if len(lines) == 0 {
				continue
			}
			tb.Logf("nats server %s logged %d line(s) at warning level or above:\n\t%s",
				l.name, len(lines), strings.Join(lines, "\n\t"))
		}
	})
}
