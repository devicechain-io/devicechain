// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// recordingTB is the real test with Cleanup, Failed and Logf taken over, so a test can
// see what a fixture logs and decide whether the fixture believes the test failed. Every
// other method is the real test's.
//
// Its cleanups run when runCleanups is called, not when the test ends: the fixture's store
// directories come from the real test's TempDir, which the real test removes at its own
// end, so the servers have to be shut down before then. Defer runCleanups.
type recordingTB struct {
	testing.TB
	failed bool

	mu       sync.Mutex
	cleanups []func()
	logged   []string
}

func (r *recordingTB) Cleanup(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanups = append(r.cleanups, f)
}

func (r *recordingTB) Failed() bool { return r.failed }

func (r *recordingTB) Logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logged = append(r.logged, fmt.Sprintf(format, args...))
}

// runCleanups runs the registered cleanups last in, first out, as testing does, once.
func (r *recordingTB) runCleanups() {
	r.mu.Lock()
	cleanups := r.cleanups
	r.cleanups = nil
	r.mu.Unlock()
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}

func (r *recordingTB) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.logged...)
}

// startsLate is a start hook that starts the server named late only after delay, and
// counts every start it is asked for. A server still waiting when done is closed is never
// started: the test that delayed it has ended, and so has its store directory.
func startsLate(late string, delay time.Duration, starts *atomic.Int32, done <-chan struct{}) func(*natsserver.Server) {
	return func(s *natsserver.Server) {
		starts.Add(1)
		go func() {
			if s.Name() == late {
				select {
				case <-time.After(delay):
				case <-done:
					return
				}
			}
			s.Start()
		}()
	}
}

// oldListenWait is how long this fixture used to give each server to listen before it
// discarded the whole construction and started another, at most three in all.
const oldListenWait = 15 * time.Second

// A server that is slow to open its listeners is waited for on the one construction, not
// discarded along with the others and started again. The delay is well past the wait
// the fixture used to give up after, so this fails if the fixture goes back to it.
func TestAServerThatListensLateIsWaitedForNotRestarted(t *testing.T) {
	const late = oldListenWait + 10*time.Second
	rec := &recordingTB{TB: t}
	defer rec.runCleanups()
	done := make(chan struct{})
	defer close(done)

	var starts atomic.Int32
	h := defaultClusterHooks()
	h.start = startsLate("n2", late, &starts, done)

	began := time.Now()
	servers, _, constructions, err := startJetStreamCluster(rec, 3, h, clusterStartBudget)
	elapsed := time.Since(began)
	defer shutdownServers(servers)
	if err != nil {
		t.Fatalf("a cluster with one server %s late to start did not start: %v", late, err)
	}
	// The negative control: without it, a delay that was never applied would pass.
	if elapsed < late {
		t.Fatalf("the cluster started in %s, before the late server's %s delay was over, so the delay "+
			"was never applied and this proves nothing", elapsed, late)
	}
	if constructions != 1 || starts.Load() != 3 {
		t.Fatalf("the late server cost %d construction(s) and %d server starts; want 1 and 3", constructions, starts.Load())
	}
	if len(servers) != 3 {
		t.Fatalf("got %d servers, want 3", len(servers))
	}
}

// A server that never listens is not a port race, and a new construction cannot cure it:
// the fixture fails on the first construction, naming the server and both listeners.
func TestAServerThatNeverListensIsNamedNotRetried(t *testing.T) {
	rec := &recordingTB{TB: t}
	defer rec.runCleanups()

	var starts atomic.Int32
	h := defaultClusterHooks()
	h.start = func(s *natsserver.Server) {
		starts.Add(1)
		if s.Name() != "n3" {
			go s.Start()
		}
	}
	h.listenWithin = 3 * time.Second

	// Bounded here, not only by go test's own timeout, so a wait that never gives up fails
	// this test by name instead of hanging the package. The bound is the start budget
	// given below plus a margin for the servers that do start to shut down.
	type result struct {
		servers       []*natsserver.Server
		constructions int
		err           error
	}
	got := make(chan result, 1)
	go func() {
		servers, _, constructions, err := startJetStreamCluster(rec, 3, h, 30*time.Second)
		got <- result{servers, constructions, err}
	}()
	var r result
	select {
	case r = <-got:
	case <-time.After(30*time.Second + 30*time.Second):
		t.Fatalf("the start was still waiting on a server that never started, long after its %s "+
			"listening budget: the wait for listeners does not give up", h.listenWithin)
	}
	servers, constructions, err := r.servers, r.constructions, r.err
	defer shutdownServers(servers)
	if err == nil {
		t.Fatal("a cluster with a server that never started was reported started")
	}
	if errors.Is(err, errListenerBind) {
		t.Fatalf("a server that never started was reported as a port that could not be bound: %v", err)
	}
	if want := "n3 has not bound its client and route listeners"; !strings.Contains(err.Error(), want) {
		t.Fatalf("the error does not say %q: %v", want, err)
	}
	if constructions != 1 || starts.Load() != 3 {
		t.Fatalf("a server that never listens cost %d construction(s) and %d server starts; want 1 and 3",
			constructions, starts.Load())
	}
	for _, line := range rec.lines() {
		if strings.Contains(line, "retrying") {
			t.Fatalf("the fixture logged a retry for a failure a retry cannot cure: %s", line)
		}
	}
}

// portsTakingFirst returns a ports hook whose first call hands out held, which the test is
// listening on, as the first server's route port, and whose later calls reserve fresh
// ports as the fixture does.
func portsTakingFirst(held int) func(n int) ([]int, error) {
	var calls atomic.Int32
	return func(n int) ([]int, error) {
		if calls.Add(1) > 1 {
			return reservePorts(n)
		}
		rest, err := reservePorts(n - 1)
		if err != nil {
			return nil, err
		}
		return append([]int{held}, rest...), nil
	}
}

// A route port taken before the server could bind it is the one failure a new
// construction cures. It is reported as that, from the server's own words, without the
// construction waiting out its listening budget; and the fixture then starts a new
// construction on fresh ports, which forms.
func TestATakenRoutePortIsReportedAsABindFailureAndRetriedOnFreshPorts(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("holding a port: %v", err)
	}
	defer l.Close()
	held := l.Addr().(*net.TCPAddr).Port

	t.Run("one construction", func(t *testing.T) {
		rec := &recordingTB{TB: t}
		defer rec.runCleanups()
		h := defaultClusterHooks()
		h.ports = portsTakingFirst(held)

		began := time.Now()
		servers, _, err := tryStartJetStreamCluster(rec, 3, h, clusterStartBudget)
		elapsed := time.Since(began)
		defer shutdownServers(servers)
		if !errors.Is(err, errListenerBind) {
			t.Fatalf("a construction whose route port was taken did not fail as a bind failure: %v", err)
		}
		for _, want := range []string{"n1: ", strconv.Itoa(held), "address already in use"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the bind failure does not carry %q: %v", want, err)
			}
		}
		// Not a guess at how fast a machine is: the bound is the budget the construction
		// would otherwise have waited out.
		if elapsed >= h.listenWithin {
			t.Fatalf("the bind failure took %s, the whole listening budget: it was waited out, not seen", elapsed)
		}
	})

	t.Run("the start", func(t *testing.T) {
		rec := &recordingTB{TB: t}
		defer rec.runCleanups()
		h := defaultClusterHooks()
		h.ports = portsTakingFirst(held) // a fresh hook: its first call hands out the held port

		servers, _, constructions, err := startJetStreamCluster(rec, 3, h, clusterStartBudget)
		defer shutdownServers(servers)
		if err != nil {
			t.Fatalf("the start did not recover from a taken route port: %v", err)
		}
		// The negative control: a hook that failed to hand out the held port would form
		// on the first construction. At least two, not exactly two: a construction on fresh
		// ports can lose the release-to-bind race on its own, which is the very race the
		// retry exists for, and is then retried in its turn.
		if constructions < 2 {
			t.Fatalf("the start took %d construction(s); want at least 2, the first failing on the taken port", constructions)
		}
		var retried bool
		for _, line := range rec.lines() {
			if strings.Contains(line, "cluster construction 1 could not bind a port") &&
				strings.Contains(line, strconv.Itoa(held)) {
				retried = true
			}
		}
		if !retried {
			t.Fatalf("the start did not log why it retried, naming the taken port %d; it logged %q", held, rec.lines())
		}
	})
}

// A test that fails prints what each fixture server logged at warning level and above,
// under that server's name; a test that passes prints none of it. Reached through
// StartJetStreamCluster itself, so the printing is tested as wired, not only as written.
func TestAFailedTestPrintsEachServersWarningsAndAPassingOneDoesNot(t *testing.T) {
	const marker = "dctest marker 42"
	for _, failed := range []bool{true, false} {
		t.Run(fmt.Sprintf("failed=%v", failed), func(t *testing.T) {
			rec := &recordingTB{TB: t, failed: failed}
			defer rec.runCleanups()
			servers := StartJetStreamCluster(rec, 3)
			servers[0].Warnf("dctest marker %d", 42)
			rec.runCleanups()

			var own, others []string
			for _, line := range rec.lines() {
				switch {
				case strings.HasPrefix(line, "nats server n1 "):
					own = append(own, line)
				case strings.HasPrefix(line, "nats server "):
					others = append(others, line)
				}
			}
			if !failed {
				if len(own)+len(others) != 0 {
					t.Fatalf("a passing test printed the servers' logs: %q", append(own, others...))
				}
				return
			}
			if len(own) != 1 || !strings.Contains(own[0], "[WRN] "+marker) {
				t.Fatalf("the failed test did not print n1's warning %q under n1's name; it printed %q", marker, own)
			}
			for _, line := range others {
				if strings.Contains(line, marker) {
					t.Fatalf("n1's warning was printed under another server's name: %s", line)
				}
			}
		})
	}
}

// What a server logs is kept per level, the line a route bind failure hinges on is
// recognized, and nothing else is taken for one.
func TestServerLogKeepsWarningsAndRecognizesOnlyARouteBindFailure(t *testing.T) {
	routeBind := func(l *serverLog) {
		l.Fatalf("Error listening on router port: %d - %v", 1234,
			errors.New("listen tcp 127.0.0.1:1234: bind: address already in use"))
	}
	for _, tc := range []struct {
		name     string
		feed     func(*serverLog)
		wantBind string
	}{
		{"a route bind failure", routeBind,
			"[FTL] Error listening on router port: 1234 - listen tcp 127.0.0.1:1234: bind: address already in use"},
		{"a stream create failure", func(l *serverLog) {
			l.Warnf("Stream create failed for '%s > %s': %v", "$G", "KV_x", errors.New("boom"))
		}, ""},
		{"nothing logged", func(*serverLog) {}, ""},
		{"notices are dropped", func(l *serverLog) {
			l.Noticef("Error listening on router port: %d", 1234)
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &serverLog{name: "n1"}
			tc.feed(l)
			if got := l.bindFailure(); got != tc.wantBind {
				t.Fatalf("bindFailure() = %q, want %q", got, tc.wantBind)
			}
		})
	}

	l := &serverLog{name: "n1"}
	l.Warnf("w %d", 1)
	l.Errorf("e %d", 2)
	l.Debugf("d")
	l.Tracef("t")
	if got, want := strings.Join(l.snapshot(), "|"), "[WRN] w 1|[ERR] e 2"; got != want {
		t.Fatalf("snapshot() = %q, want %q", got, want)
	}
}

// A server that logs more than it can keep keeps its first lines and its last, and the
// route bind failure, wherever it fell, is still recognized.
func TestServerLogKeepsTheFirstAndLastLinesAndTheBindFailure(t *testing.T) {
	l := &serverLog{name: "n1"}
	const total = serverLogHead + serverLogTail + 500
	for i := 0; i < total; i++ {
		if i == serverLogHead+100 {
			l.Fatalf("Error listening on router port: %d - %s", 1234, "address already in use")
			continue
		}
		l.Errorf("line %d", i)
	}
	got := l.snapshot()
	if len(got) != serverLogHead+serverLogTail+1 {
		t.Fatalf("kept %d lines, want %d plus the dropped marker", len(got), serverLogHead+serverLogTail)
	}
	if got[0] != "[ERR] line 0" || got[serverLogHead-1] != fmt.Sprintf("[ERR] line %d", serverLogHead-1) {
		t.Fatalf("the first lines were not kept in order: %q … %q", got[0], got[serverLogHead-1])
	}
	if want := fmt.Sprintf("… %d lines dropped …", total-serverLogHead-serverLogTail); got[serverLogHead] != want {
		t.Fatalf("the dropped marker is %q, want %q", got[serverLogHead], want)
	}
	if first, last := got[serverLogHead+1], got[len(got)-1]; first != fmt.Sprintf("[ERR] line %d", total-serverLogTail) ||
		last != fmt.Sprintf("[ERR] line %d", total-1) {
		t.Fatalf("the last lines were not kept in order: %q … %q", first, last)
	}
	if !strings.Contains(l.bindFailure(), "Error listening on router port: 1234") {
		t.Fatalf("the bind failure was lost with the dropped lines: %q", l.bindFailure())
	}
}

// A fixture's error carries each server's last lines, and still wraps what it wrapped.
func TestWithServerLogsKeepsTheCauseAndAddsEachServersLastLines(t *testing.T) {
	a, b, quiet := &serverLog{name: "n1"}, &serverLog{name: "n2"}, &serverLog{name: "n3"}
	for i := 0; i < serverLogInError+5; i++ {
		a.Warnf("a %d", i)
	}
	b.Errorf("b only")
	err := withServerLogs(fmt.Errorf("n1: %w: x", errListenerBind), []*serverLog{a, b, quiet})
	if !errors.Is(err, errListenerBind) {
		t.Fatalf("the error no longer wraps its cause: %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"n1: [WRN] a 14", "n1: [WRN] a 5", "n2: [ERR] b only"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the error does not carry %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "n1: [WRN] a 4") || strings.Contains(msg, "n3:") {
		t.Fatalf("the error carries more than each server's last %d lines: %s", serverLogInError, msg)
	}
	if plain := errors.New("plain"); withServerLogs(plain, []*serverLog{quiet}) != plain {
		t.Fatal("an error with nothing logged to add was changed")
	}
}

// A route-fault cluster starts through the same step as the plain one, so a taken route
// port is seen there too, from the server's own words, and cured by a new construction.
// Without this, nothing reaches that fixture's start with a port that cannot be bound.
func TestARouteFaultClusterRetriesATakenRoutePortOnFreshPorts(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("holding a port: %v", err)
	}
	defer l.Close()
	held := l.Addr().(*net.TCPAddr).Port

	rec := &recordingTB{TB: t}
	defer rec.runCleanups()
	h := defaultClusterHooks()
	h.ports = portsTakingFirst(held) // n1's cluster port, the one its route listener binds

	servers, faults, _, constructions, err := startJetStreamClusterWithRouteFaults(rec, 3, h, clusterStartBudget)
	if faults != nil {
		defer faults.close()
	}
	defer shutdownServers(servers)
	if err != nil {
		t.Fatalf("the route-fault start did not recover from a taken route port: %v", err)
	}
	if constructions < 2 {
		t.Fatalf("the route-fault start took %d construction(s); want at least 2, the first failing on the taken port",
			constructions)
	}
	var retried bool
	for _, line := range rec.lines() {
		if strings.Contains(line, "route-fault cluster construction 1 could not bind a port") &&
			strings.Contains(line, strconv.Itoa(held)) && strings.Contains(line, "address already in use") {
			retried = true
		}
	}
	if !retried {
		t.Fatalf("the route-fault start did not log a bind failure on the taken port %d as its reason to "+
			"retry; it logged %q", held, rec.lines())
	}
}

// The start budget is the one bound on retrying, and each construction is given only
// what is left of it. A bind failure that never clears ends in an error saying the budget
// is spent, not in a loop; and no construction is offered more time than remains.
func TestRetryOnBindFailureStopsAtTheBudgetAndGivesEachTryWhatIsLeft(t *testing.T) {
	const budget = 300 * time.Millisecond
	const each = 20 * time.Millisecond
	// A try loop that ignored the budget would call past this; the cap turns that into a
	// failure of this test rather than a hang.
	const runaway = 200
	errRunaway := errors.New("called far more often than the budget allows")

	rec := &recordingTB{TB: t}
	var withins []time.Duration
	began := time.Now()
	n, err := retryOnBindFailure(rec, "test", budget, func(within time.Duration) error {
		if len(withins) >= runaway {
			return errRunaway
		}
		withins = append(withins, within)
		// The fixture starts its clock a moment after began, so allow it that moment.
		if within > budget-time.Since(began)+5*time.Millisecond {
			t.Errorf("try %d was offered %s with only %s of the budget left", len(withins), within,
				budget-time.Since(began))
		}
		time.Sleep(each)
		return fmt.Errorf("n1: %w: taken", errListenerBind)
	})
	if errors.Is(err, errRunaway) {
		t.Fatalf("retrying did not stop when the %s budget was spent: %d tries", budget, n)
	}
	if !errors.Is(err, errListenerBind) || !strings.Contains(err.Error(), "the start budget of "+budget.String()+" is spent") {
		t.Fatalf("a bind failure that outlasted the budget ended with %v; want the bind failure, saying the budget is spent", err)
	}
	if n < 2 || n != len(withins) {
		t.Fatalf("reported %d tries and made %d; want the same number, more than one", n, len(withins))
	}
	for i := 1; i < len(withins); i++ {
		if withins[i] >= withins[i-1] {
			t.Fatalf("try %d was offered %s after try %d was offered %s: each should get only what is left",
				i+1, withins[i], i, withins[i-1])
		}
	}
	if len(rec.lines()) != n-1 {
		t.Fatalf("logged %d retries for %d tries: %q", len(rec.lines()), n, rec.lines())
	}

	t.Run("any other error is returned at once", func(t *testing.T) {
		other := errors.New("not formed")
		calls := 0
		n, err := retryOnBindFailure(rec, "test", budget, func(time.Duration) error {
			calls++
			return other
		})
		if err != other || n != 1 || calls != 1 {
			t.Fatalf("got n=%d, %d call(s), err %v; want one call returning %v unchanged", n, calls, err, other)
		}
	})
}
