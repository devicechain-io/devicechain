// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime/trace"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The profiling listener's lifecycle and its address rules. The three behaviours it
// exists for are in profiler_listener_test.go; these reach into the implementation.
// None may call t.Parallel, for the reasons given there.

// It is up for the service's WHOLE start and WHOLE stop — that is what placing it in
// ExecuteStart/ExecuteStop buys — and it is gone once the stop returns.
func TestTheProfilerIsUpForTheServicesWholeStartAndStop(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t))
	t.Setenv(ENV_PROFILER_ADDRESS, addr)

	probe := func() int {
		status, _, err := get(t, "http://"+addr+"/debug/pprof/")
		if err != nil {
			return -1
		}
		return status
	}
	var duringStart, duringStop int
	callbacks := NewNoOpLifecycleCallbacks()
	callbacks.Starter.Postprocess = func(context.Context) error { duringStart = probe(); return nil }
	callbacks.Stopper.Preprocess = func(context.Context) error { duringStop = probe(); return nil }

	ms := startedService(t, callbacks)
	require.NoError(t, ms.Stop(context.Background()))

	assert.Equal(t, http.StatusOK, duringStart, "the listener was not up while the service's own start ran")
	assert.Equal(t, http.StatusOK, duringStop, "the listener was not up while the service's own stop ran")
	assert.Nil(t, ms.profiler.Load(), "a stopped service still holds its profiling listener")
	refused, err := dialRefused(addr)
	require.NoError(t, err)
	assert.True(t, refused, "the profiling listener is still accepting connections after Stop")
}

// A profile in progress when the service stops is abandoned, so the stop does not wait
// for it. A trace is used because runtime/trace says whether one is running, so the test
// can know the request is in progress without racing a second request for the profiler.
func TestStoppingAbandonsAProfileInProgress(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t))
	t.Setenv(ENV_PROFILER_ADDRESS, addr)
	ms := startedService(t, NewNoOpLifecycleCallbacks())

	type result struct {
		status int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/debug/pprof/trace?seconds=30")
		if err != nil {
			done <- result{err: err}
			return
		}
		defer resp.Body.Close()
		_, err = io.ReadAll(resp.Body)
		done <- result{status: resp.StatusCode, err: err}
	}()
	require.Eventually(t, trace.IsEnabled, 5*time.Second, 5*time.Millisecond, "the trace never started")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	began := time.Now()
	require.NoError(t, ms.Stop(ctx))
	assert.Less(t, time.Since(began), 3*time.Second, "Stop waited for the trace instead of abandoning it")

	select {
	case r := <-done:
		assert.Error(t, r.err, "an abandoned trace reached the client as a complete one (status %d)", r.status)
	case <-time.After(5 * time.Second):
		t.Fatal("the trace request never ended")
	}
	assert.False(t, trace.IsEnabled(), "the trace was left running")
}

// A listener that cannot bind refuses the start, naming itself, and the service is left
// where a failed start leaves it.
func TestAProfilerThatCannotBindRefusesTheStart(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer held.Close()
	t.Setenv(ENV_PROFILER_ADDRESS, held.Addr().String())

	ms := &Microservice{}
	ms.lifecycle = NewLifecycleManager("test", ms, NewNoOpLifecycleCallbacks())
	ms.lifecycle.State = Initialized
	err = ms.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profiling listener")
	assert.Contains(t, err.Error(), held.Addr().String())
	assert.Equal(t, Initialized, ms.lifecycle.State)
	assert.Nil(t, ms.profiler.Load())
}

// A malformed address refuses the start rather than leaving the listener off.
func TestAMalformedProfilerAddressRefusesTheStart(t *testing.T) {
	t.Setenv(ENV_PROFILER_ADDRESS, "localhost:6060")
	ms := &Microservice{}
	ms.lifecycle = NewLifecycleManager("test", ms, NewNoOpLifecycleCallbacks())
	ms.lifecycle.State = Initialized
	err := ms.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), ENV_PROFILER_ADDRESS)
	assert.Contains(t, err.Error(), "not an IP address")
	assert.Equal(t, Initialized, ms.lifecycle.State)
}

func TestProfilerAddressRules(t *testing.T) {
	t.Run("unset is off", func(t *testing.T) {
		t.Setenv(ENV_PROFILER_ADDRESS, "")
		require.NoError(t, os.Unsetenv(ENV_PROFILER_ADDRESS))
		addr, on, err := profilerAddress()
		require.NoError(t, err)
		assert.False(t, on)
		assert.Equal(t, "", addr)
	})
	refused := map[string]string{
		"":                                    "missing port",
		":6060":                               "host is empty",
		"localhost:6060":                      "not an IP address",
		"127.0.0.1":                           "missing port",
		"127.0.0.1:0":                         "not a number from 1 to 65535",
		"127.0.0.1:65536":                     "not a number from 1 to 65535",
		"127.0.0.1:+1":                        "not a number from 1 to 65535",
		"127.0.0.1:x":                         "not a number from 1 to 65535",
		"127.0.0.1:" + strconv.Itoa(HttpPort): "the service's own HTTP port",
	}
	for v, why := range refused {
		t.Run("refuses "+v, func(t *testing.T) {
			t.Setenv(ENV_PROFILER_ADDRESS, v)
			addr, on, err := profilerAddress()
			require.Error(t, err)
			assert.Contains(t, err.Error(), why)
			assert.Contains(t, err.Error(), ENV_PROFILER_ADDRESS)
			assert.False(t, on)
			assert.Equal(t, "", addr)
		})
	}
	for _, v := range []string{"127.0.0.1:6060", "0.0.0.0:6060", "[::1]:6060", "10.0.0.5:7000"} {
		t.Run("accepts "+v, func(t *testing.T) {
			t.Setenv(ENV_PROFILER_ADDRESS, v)
			addr, on, err := profilerAddress()
			require.NoError(t, err)
			assert.True(t, on)
			assert.Equal(t, v, addr)
		})
	}
}
