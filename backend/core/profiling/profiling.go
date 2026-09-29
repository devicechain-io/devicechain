// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package profiling serves Go runtime profiles over HTTP, for the opt-in profiling
// listener every service can run on a port of its own (core.Microservice starts it).
//
// 🔴 IT DELIBERATELY DOES NOT IMPORT net/http/pprof, and a reader reaching for that
// package to "simplify" this one should know why. Its init registers five routes on
// http.DefaultServeMux, whatever name it is imported under, and
// hack/check-default-servemux.sh refuses the import for exactly that reason. The
// refusal is right: in this tree the default mux is a mux nothing serves, so the
// registration would be dead weight at best — and on any server that ever fell back to
// it, a profiler mounted on a traffic port. Everything here is built on runtime/pprof
// and runtime/trace, which register nothing, and is mounted only on the private mux
// Handler returns.
//
// The URL shapes match net/http/pprof's, so `go tool pprof` and `go tool trace` work
// against it unchanged. What it serves is a deliberately narrower set; the comments on
// Served and notCollected say what is left out and why.
package profiling

import (
	"bytes"
	"fmt"
	"net/http"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// Prefix is the path every route is served under, as net/http/pprof serves it.
	Prefix = "/debug/pprof/"

	// DefaultCPUSeconds is how long a CPU profile runs when the request names no
	// duration. It is go tool pprof's own default, so an unadorned
	// `go tool pprof <url>/profile` asks for what it would have asked for anyway.
	DefaultCPUSeconds = 30
	// DefaultTraceSeconds is the default for an execution trace, which is far larger
	// per second than a CPU profile.
	DefaultTraceSeconds = 1
	// MaxSeconds bounds both. A profile holds a request (and, for a CPU profile, the
	// process's one profiler) for its whole duration, so an unbounded one is a way to
	// deny the next person the profiler.
	MaxSeconds = 60
)

// Served is the exact set of named runtime profiles this endpoint answers, sorted. The
// index lists these plus "profile" (CPU) and "trace".
//
// It is an ALLOW list rather than "whatever pprof.Lookup finds", because pprof.Lookup
// also finds the two profiles in notCollected, which exist and are always empty here.
var Served = []string{"allocs", "goroutine", "heap", "threadcreate"}

// notCollected are runtime profiles that EXIST but are always empty in these services,
// because their sampling rate is zero: nothing calls runtime.SetBlockProfileRate or
// runtime.SetMutexProfileFraction. Serving one would return a valid, empty profile, and
// an empty contention profile reads as "no contention" — a plausible wrong answer. They
// answer 404 and say why instead. Turning their sampling on is a runtime-cost decision
// of its own, not something a profiling endpoint should do as a side effect.
var notCollected = map[string]string{
	"block": "the block profile is not collected: its sampling rate is off in DeviceChain services, " +
		"so it would be empty and would read as no blocking",
	"mutex": "the mutex profile is not collected: its sampling rate is off in DeviceChain services, " +
		"so it would be empty and would read as no contention",
}

// writeMargin is how long a profile's response may take to reach the client once the
// profile itself has ended. It bounds a client that stops reading part-way: an execution
// trace keeps the process's tracer switched on until every write of it has returned, so
// a write with no deadline to a stalled client would hold the tracer — and the memory
// its buffers use — for as long as the client stays stalled. A variable only so the
// tests need not wait it out; production never changes it.
var writeMargin = 10 * time.Second

// cpuProfileStarted runs once a CPU profile has actually started. It exists for the
// tests alone, which need to know a profile is in progress without racing a second
// request for the one profiler the process has; production leaves it a no-op.
var cpuProfileStarted = func() {}

// Handler is the profiling endpoint, on a mux of its own.
//
// stop is the owner's shutdown signal. A profile in progress when it closes is
// abandoned rather than run to its end, so a service's shutdown does not wait out
// someone's 30-second CPU profile: a CPU profile answers 503, and a trace — whose
// status line has already been sent — has its connection cut, so the client sees an
// error rather than a truncated trace that looks complete.
//
// Abandoning a profile also has to survive a client that has STOPPED READING (a stalled
// kubectl port-forward, a suspended curl). A write to such a client blocks, and
// trace.Stop does not return until every write of the trace has, so an unbounded write
// would hold the handler, keep the tracer on and hold up the owner's shutdown. Two
// bounds cover it: every profile's response carries an overall write deadline
// (oneShotResponse), and a stop moves an abandoned trace's deadline to now. What neither
// reaches — a response that has finished its profile and is still being written when
// stop closes — is the owner's to end by closing the connection, which is why
// core.Microservice follows a short graceful shutdown of this listener with a close.
func Handler(stop <-chan struct{}) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Prefix+"{$}", index)
	mux.HandleFunc("GET "+Prefix+"profile", cpuProfile(stop))
	mux.HandleFunc("GET "+Prefix+"trace", executionTrace(stop))
	mux.HandleFunc("GET "+Prefix+"{name}", named)
	return mux
}

// fail writes a plain-text error. Every refusal here is a sentence, because the person
// reading it is at a terminal with curl or go tool pprof and has nothing else to go on.
func fail(w http.ResponseWriter, code int, format string, args ...any) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.Error(w, fmt.Sprintf(format, args...), code)
}

func index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	var b strings.Builder
	b.WriteString("Go runtime profiles for this service.\n\n")
	for _, name := range Served {
		fmt.Fprintf(&b, "%s%s\n", Prefix, name)
	}
	fmt.Fprintf(&b, "%sprofile?seconds=N   CPU profile, 1-%d seconds (default %d)\n", Prefix, MaxSeconds, DefaultCPUSeconds)
	fmt.Fprintf(&b, "%strace?seconds=N     execution trace, 1-%d seconds (default %d)\n", Prefix, MaxSeconds, DefaultTraceSeconds)
	_, _ = w.Write([]byte(b.String()))
}

// named serves one of the snapshot profiles in Served.
func named(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if why, ok := notCollected[name]; ok {
		fail(w, http.StatusNotFound, "%s", why)
		return
	}
	p := pprof.Lookup(name)
	if !slices.Contains(Served, name) || p == nil {
		fail(w, http.StatusNotFound, "unknown profile %q; this endpoint serves: %s, profile, trace",
			name, strings.Join(Served, ", "))
		return
	}
	q := r.URL.Query()
	// A seconds parameter on a snapshot profile asks net/http/pprof for a DELTA over
	// that window, which needs a profile-merging package the standard library does not
	// export. Answering with a plain snapshot instead would hand back something that
	// looks like the delta that was asked for and is not, so it is refused.
	if q.Has("seconds") {
		fail(w, http.StatusBadRequest, "a %s profile over a time window is not served: take two snapshots "+
			"and compare them with `go tool pprof -base <first> <second>`", name)
		return
	}
	debug := 0
	if v := q.Get("debug"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 2 {
			fail(w, http.StatusBadRequest, "debug must be 0 (binary), 1 (text) or 2 (full goroutine stacks), not %q", v)
			return
		}
		debug = n
	}
	if name == "heap" && q.Get("gc") != "" && q.Get("gc") != "0" {
		runtime.GC()
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if debug == 0 {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	_ = p.WriteTo(w, debug)
}

// seconds reads the duration parameter, defaulting it and bounding it to 1..MaxSeconds.
func seconds(r *http.Request, def int) (time.Duration, error) {
	v := r.URL.Query().Get("seconds")
	if v == "" {
		return time.Duration(def) * time.Second, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > MaxSeconds {
		return 0, fmt.Errorf("seconds must be a whole number from 1 to %d, not %q", MaxSeconds, v)
	}
	return time.Duration(n) * time.Second, nil
}

// cpuProfile runs a CPU profile for the requested duration.
//
// It is BUFFERED, which is what makes an abandoned profile a 503 rather than a short
// 200: a CPU profile is written out only when it stops, so writing straight to the
// response would commit the status line with whatever had been gathered when shutdown
// came. The buffer is a compressed profile, which is small.
func cpuProfile(stop <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, err := seconds(r, DefaultCPUSeconds)
		if err != nil {
			fail(w, http.StatusBadRequest, "%s", err)
			return
		}
		// The whole response, profile and all, has until d+writeMargin to reach the client.
		oneShotResponse(w, http.NewResponseController(w), d+writeMargin)
		var buf bytes.Buffer
		if err := pprof.StartCPUProfile(&buf); err != nil {
			fail(w, http.StatusConflict, "a CPU profile is already being taken in this process; try again when it ends")
			return
		}
		cpuProfileStarted()
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			pprof.StopCPUProfile()
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="profile"`)
			_, _ = w.Write(buf.Bytes())
		case <-r.Context().Done():
			pprof.StopCPUProfile()
		case <-stop:
			pprof.StopCPUProfile()
			fail(w, http.StatusServiceUnavailable, "the service is shutting down; the CPU profile was abandoned")
		}
	}
}

// executionTrace streams an execution trace for the requested duration.
//
// It is NOT buffered, unlike the CPU profile: a trace of a busy service runs to
// megabytes a second, and holding up to a minute of one in a memory-limited pod is an
// out-of-memory risk. So the status line goes out first, and an abandoned trace cannot
// become a 503. It aborts the connection instead (http.ErrAbortHandler, which net/http
// recovers without logging), so the client reads an unexpected EOF rather than a
// clean end to a trace that stopped part-way.
//
// 🔴 trace.Stop RETURNS ONLY AFTER EVERY WRITE OF THE TRACE HAS, so a write to a client
// that stopped reading must not be able to block for long before it: the response
// carries an overall deadline of d+writeMargin, and a stop moves it to now before
// calling trace.Stop. The runtime's trace writer ignores write errors and keeps
// draining, so once writes fail it finishes at once. A client that went away needs
// neither: its writes fail on their own.
func executionTrace(stop <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, err := seconds(r, DefaultTraceSeconds)
		if err != nil {
			fail(w, http.StatusBadRequest, "%s", err)
			return
		}
		rc := http.NewResponseController(w)
		oneShotResponse(w, rc, d+writeMargin)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="trace"`)
		if err := trace.Start(w); err != nil {
			w.Header().Del("Content-Disposition")
			fail(w, http.StatusConflict, "an execution trace is already being taken in this process; try again when it ends")
			return
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			trace.Stop()
		case <-r.Context().Done():
			trace.Stop()
		case <-stop:
			_ = rc.SetWriteDeadline(time.Now())
			trace.Stop()
			panic(http.ErrAbortHandler)
		}
	}
}

// oneShotResponse puts an overall write deadline on a profile's response and marks its
// connection to close after it.
//
// The close is what makes the deadline safe to set: net/http does not reset a
// connection's write deadline between requests unless the server has a WriteTimeout,
// and these servers have none, so a kept-alive connection would carry this deadline
// into the next request on it and fail that request's writes.
func oneShotResponse(w http.ResponseWriter, rc *http.ResponseController, within time.Duration) {
	w.Header().Set("Connection", "close")
	_ = rc.SetWriteDeadline(time.Now().Add(within))
}
