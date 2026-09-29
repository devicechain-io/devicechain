// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The three behaviours the opt-in profiling listener exists to have, driven through the
// real lifecycle: Microservice.Start runs Starter.Preprocess, ExecuteStart and
// Starter.Postprocess exactly as a service's does.
//
// 🔴 THIS FILE NAMES NOTHING THE LISTENER ADDED — the environment variable is spelled
// as a literal here on purpose — so it compiles against the code from before the
// listener existed, and the first test can be shown to fail there by VALUE (connection
// refused where a 200 is wanted) rather than by a missing identifier. The tests that
// reach into the implementation are in profiler_test.go.
//
// None of these may call t.Parallel: they set a process environment variable and take a
// process-wide profiler.

// profilerEnv is ENV_PROFILER_ADDRESS, spelled out for the reason above.
const profilerEnv = "DC_PROFILER_ADDRESS"

// freeLoopbackPort finds a port nothing is listening on, by binding one and letting it
// go. Another process could take it in between; that race is accepted in exchange for a
// test that addresses a real port.
func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// startedService builds a Microservice whose lifecycle component is the Microservice
// itself — not inertComponent — so ExecuteStart and ExecuteStop are the real ones, and
// starts it. Initialize is skipped (it reads the chart's mount points); the lifecycle is
// put in the state Initialize leaves it in.
func startedService(t *testing.T, callbacks LifecycleCallbacks) *Microservice {
	t.Helper()
	ms := &Microservice{FunctionalArea: "device-state"}
	ms.lifecycle = NewLifecycleManager("test", ms, callbacks)
	ms.lifecycle.State = Initialized
	require.NoError(t, ms.Start(context.Background()))
	t.Cleanup(func() { _ = ms.Stop(context.Background()) })
	return ms
}

// get is a GET with a short timeout that returns the status and body.
func get(t *testing.T, url string) (int, string, error) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}

// dialRefused reports whether nothing is listening at addr.
func dialRefused(addr string) (bool, error) {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err == nil {
		_ = conn.Close()
		return false, nil
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true, nil
	}
	return false, err
}

// ENABLED: the configured address serves the profiling index, and the profiles on it
// are real ones.
func TestAnEnabledServiceServesProfilesOnItsOwnPort(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t))
	t.Setenv(profilerEnv, addr)
	startedService(t, NewNoOpLifecycleCallbacks())

	status, body, err := get(t, "http://"+addr+"/debug/pprof/")
	require.NoError(t, err, "nothing answered on the configured profiling address")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, "/debug/pprof/goroutine")
	assert.Contains(t, body, "/debug/pprof/profile")

	status, body, err = get(t, "http://"+addr+"/debug/pprof/goroutine?debug=1")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.True(t, strings.HasPrefix(body, "goroutine profile: total "),
		"the goroutine profile is not a goroutine profile: %.80q", body)
}

// DISABLED, the default: with the variable unset, nothing listens where the listener
// would have been.
func TestWithNoAddressNothingListens(t *testing.T) {
	port := freeLoopbackPort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// Negative control: the helper can see a listener, so "refused" below is a
	// measurement and not the only answer it knows how to give.
	ln, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	refused, err := dialRefused(addr)
	require.NoError(t, err)
	require.False(t, refused, "dialRefused reported refused against a live listener")
	require.NoError(t, ln.Close())

	// os.Unsetenv rather than a Setenv to "": an empty value is a SET variable, which
	// is a different case (refused at startup). t.Setenv first so the prior value, if
	// any, is restored when the test ends.
	t.Setenv(profilerEnv, "")
	require.NoError(t, os.Unsetenv(profilerEnv))
	startedService(t, NewNoOpLifecycleCallbacks())

	refused, err = dialRefused(addr)
	require.NoError(t, err)
	assert.True(t, refused, "something is listening at %s with the profiler off", addr)
}

// The service's own traffic port never serves the profiles, even while the profiling
// listener is on. What this pins is that no profiling route is mounted on the mux the
// service serves; a service that mounts its own catch-all "/" answers such a path with
// that handler rather than a 404, which is why the body is checked as well.
func TestTheTrafficPortNeverServesProfiles(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t))
	t.Setenv(profilerEnv, addr)
	ms := startedService(t, NewNoOpLifecycleCallbacks())

	ms.RegisterProbes(nil)
	srv := ms.NewHttpServer(0)
	require.NoError(t, srv.Start())
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	traffic := "http://" + srv.Addr()

	for _, path := range []string{"/debug/pprof/", "/debug/pprof/goroutine?debug=1", "/debug/pprof/profile?seconds=1"} {
		status, body, err := get(t, traffic+path)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, status, "traffic port answered %s", path)
		assert.NotContains(t, body, "goroutine profile", "traffic port served a profile at %s", path)
	}

	// In the same service, the listener IS on — so the 404s above are about the port,
	// not about a profiler that never started.
	status, _, err := get(t, "http://"+addr+"/debug/pprof/")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
}
