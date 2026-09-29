// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"

	"github.com/devicechain-io/dc-microservice/profiling"
	"github.com/rs/zerolog/log"
)

// profilerListener is the opt-in profiling listener while it runs: its server, and the
// channel whose closing abandons any profile still in progress. The two are one value
// behind one atomic pointer so there is no order in which they can be observed apart.
type profilerListener struct {
	srv  *HttpServer
	stop chan struct{}
}

// profilerAddress reads ENV_PROFILER_ADDRESS. ok is false when the variable is unset,
// which is the default and means no listener.
//
// A variable that is SET is judged strictly, and anything it cannot use refuses
// startup rather than leaving the listener off: a profiler somebody asked for and did
// not get is discovered only when the port-forward is refused, a long way from here.
//
//   - The host must be an IP literal. A name depends on resolution inside the pod, and
//     an empty host ("":6060") would bind every interface without saying so — spell that
//     0.0.0.0 if it is meant.
//   - The port must be 1-65535. Port 0 would bind SOME port, which nobody could then
//     port-forward to without reading the log.
//   - The port may not be HttpPort, which the service's own server binds a moment later.
//     Refusing it here names the profiler; letting it through would fail the SERVICE's
//     bind instead, and that error would point at the wrong listener.
func profilerAddress() (addr string, ok bool, err error) {
	v, set := os.LookupEnv(ENV_PROFILER_ADDRESS)
	if !set {
		return "", false, nil
	}
	refuse := func(why string) (string, bool, error) {
		return "", false, fmt.Errorf("%s=%q cannot be used: %s. It must be an IP address and a port, "+
			"for example 127.0.0.1:6060, or unset to leave the profiling listener off",
			ENV_PROFILER_ADDRESS, v, why)
	}
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		return refuse(err.Error())
	}
	if host == "" {
		return refuse("the host is empty, which would bind every interface; write 0.0.0.0 if that is meant")
	}
	if _, err := netip.ParseAddr(host); err != nil {
		return refuse(fmt.Sprintf("the host %q is not an IP address", host))
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return refuse(fmt.Sprintf("the port %q is not a number from 1 to 65535", port))
	}
	if n == HttpPort {
		return refuse(fmt.Sprintf("port %d is the service's own HTTP port", HttpPort))
	}
	return v, true, nil
}

// startProfiler starts the profiling listener when ENV_PROFILER_ADDRESS asks for one.
// It serves only the profiling handler, on its own server, so nothing it serves is ever
// on the service's traffic port or its mux.
//
// A listener that cannot bind refuses startup, like the service's own server does.
func (ms *Microservice) startProfiler() error {
	addr, on, err := profilerAddress()
	if err != nil || !on {
		return err
	}
	// transition leaves a service Initialized and startable again after a failed start,
	// with whatever that attempt built still built, so a second start can find the
	// listener already up. It keeps it.
	if ms.profiler.Load() != nil {
		return nil
	}
	stop := make(chan struct{})
	srv := NewHttpServerAt(addr, profiling.Handler(stop), HttpServerOptions{})
	if err := srv.Start(); err != nil {
		return fmt.Errorf("starting the profiling listener (%s): %w", ENV_PROFILER_ADDRESS, err)
	}
	ms.profiler.Store(&profilerListener{srv: srv, stop: stop})

	ev := log.Info()
	msg := "Profiling listener is ON. Reach it with kubectl port-forward."
	if ip, _ := netip.ParseAddrPort(srv.Addr()); !ip.Addr().IsLoopback() {
		ev = log.Warn()
		msg = "Profiling listener is ON and NOT on loopback: it has no authentication, and the chart " +
			"renders no network policy limiting who can connect, so anything that can reach this pod can read its profiles."
	}
	ev.Str("addr", srv.Addr()).Str("path", profiling.Prefix).Msg(msg)
	return nil
}

// stopProfiler closes the profiling listener, if one is running.
//
// It closes the stop channel BEFORE the graceful shutdown, which is what keeps a
// profile in progress from holding the shutdown for up to a minute: the profile is
// abandoned (503, or a cut connection for a trace) and the handler returns at once.
//
// ⚠️ It does not run on every exit path. transition returns before ExecuteStop when a
// service's own Stopper.Preprocess fails, and then this is never reached; the process
// is exiting non-zero at that point, which closes the socket with it.
func (ms *Microservice) stopProfiler(ctx context.Context) error {
	l := ms.profiler.Swap(nil)
	if l == nil {
		return nil
	}
	close(l.stop)
	if err := l.srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("stopping the profiling listener: %w", err)
	}
	return nil
}
