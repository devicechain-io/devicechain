// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// doPost posts to addr through a client with the given timeout and returns the error.
func doPost(t *testing.T, addr string, timeout time.Duration) error {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+"/x", nil)
	require.NoError(t, err)
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

func TestRefusedAtDial_RealRefusedConnect(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close()) // nothing listens: the connect is refused

	err = doPost(t, addr, 2*time.Second)
	require.Error(t, err)
	require.True(t, refusedAtDial(err), "a refused connect is definite: %v", err)
}

func TestRefusedAtDial_ResetAfterWriteIsAmbiguous(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 1)
		_, _ = c.Read(buf) // the request has been written
		_ = c.(*net.TCPConn).SetLinger(0)
		_ = c.Close() // RST
	}()

	err = doPost(t, l.Addr().String(), 2*time.Second)
	require.Error(t, err)
	require.False(t, refusedAtDial(err), "a reset after the request was written stays ambiguous: %v", err)
}

func TestRefusedAtDial_TimeoutIsAmbiguous(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err == nil {
			defer c.Close()
			time.Sleep(time.Second)
		}
	}()

	err = doPost(t, l.Addr().String(), 100*time.Millisecond)
	require.Error(t, err)
	require.True(t, os.IsTimeout(err), "the control must really be a timeout: %v", err)
	require.False(t, refusedAtDial(err), "a response timeout is ambiguous: %v", err)
}

func TestRefusedAtDial_ClassifiesByTypeNotText(t *testing.T) {
	dial := func(e error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connectex", e)}
	}

	require.True(t, refusedAtDial(dial(syscall.ECONNREFUSED)))
	// Windows delivers WSAECONNREFUSED, which is not syscall.ECONNREFUSED there.
	require.True(t, refusedAtDial(fmt.Errorf("wrapped: %w", dial(wsaeconnrefused))))

	// Negative controls: same text, wrong type; right errno, wrong op; other errnos.
	require.False(t, refusedAtDial(errors.New("dial tcp 1.2.3.4:80: connect: connection refused")))
	require.False(t, refusedAtDial(&net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNREFUSED)}))
	require.False(t, refusedAtDial(dial(syscall.ECONNRESET)))
	require.False(t, refusedAtDial(dial(syscall.ETIMEDOUT)))
	require.False(t, refusedAtDial(nil))
}

func ingressRuntime(addr string, devices int) *Runtime {
	ds := make([]DeviceInstance, devices)
	for i := range ds {
		ds[i] = DeviceInstance{Token: renderPattern("dev-{n:05d}", i+1), CredentialId: "cred"}
	}
	return &Runtime{
		Endpoints:  Endpoints{Ingress: "http://" + addr},
		InstanceId: "dc", Tenant: "acme",
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
		Devices:    ds, Identity: NewIdentityLedger(),
	}
}

// The drive's verdict: a refused connect is a clean refusal (the run stays clean-drive
// eligible, the ledger files it refused), while a reset after the write is a Failed emit
// filed ambiguous. The second half is the negative control: the same drive, one
// different failure, and the verdict flips.
func TestEmitAllFilesRefusedConnectCleanAndResetAmbiguous(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	rt := ingressRuntime(addr, 5)
	require.NoError(t, EmitAll(context.Background(), rt, 2, constantMetrics))
	require.EqualValues(t, 0, rt.Stats.Failed.Load(), "a refused connect must not fail the drive")
	require.EqualValues(t, 0, rt.Stats.Backpressured.Load(), "a refused connect is not backpressure")
	require.EqualValues(t, 0, rt.Stats.Shed.Load())
	require.EqualValues(t, 5, rt.Stats.ConnectRefused.Load())
	require.EqualValues(t, 5, rt.Stats.Snapshot(time.Now()).Refused())
	snap := rt.Identity.Snapshot()
	require.EqualValues(t, 5, snap.All.Refused)
	require.EqualValues(t, 0, snap.All.Ambiguous)

	rl, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer rl.Close()
	go func() {
		for {
			c, err := rl.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = c.Read(make([]byte, 1))
				_ = c.(*net.TCPConn).SetLinger(0)
				_ = c.Close()
			}()
		}
	}()
	rt2 := ingressRuntime(rl.Addr().String(), 5)
	require.Error(t, EmitAll(context.Background(), rt2, 2, constantMetrics))
	require.EqualValues(t, 5, rt2.Stats.Failed.Load())
	snap2 := rt2.Identity.Snapshot()
	require.EqualValues(t, 5, snap2.All.Ambiguous)
	require.EqualValues(t, 0, snap2.All.Refused)
}
