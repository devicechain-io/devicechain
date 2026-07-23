// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// roamingConn is a net.PacketConn whose underlying UDP socket can be swapped mid-flow
// to simulate a client waking on a NEW source address — a NAT rebinding, or a
// queue-mode cellular device that reconnects from a fresh IP/port. Its DTLS session
// state (keys, the negotiated Connection ID) is unchanged; only the source address of
// its outbound datagrams moves. This is the exact condition RFC 9146 Connection ID
// exists to survive, and the instrument the L0 CID proof drives.
//
// Reads always come from the CURRENT inner socket: roam() closes the old socket, which
// unblocks any in-flight ReadFrom with an error, and the wrapper then retries on the
// new socket. Deadlines set by the DTLS stack are carried across a swap so a read that
// was bounded before the roam stays bounded after it.
type roamingConn struct {
	mu           sync.Mutex
	inner        *net.UDPConn
	readDeadline time.Time
}

// newRoamingConn opens the initial loopback UDP socket.
func newRoamingConn(t *testing.T) *roamingConn {
	t.Helper()
	uc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	return &roamingConn{inner: uc}
}

func (r *roamingConn) current() *net.UDPConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner
}

// ReadFrom reads from the current inner socket, transparently retrying on the new
// socket when a roam swapped it out from under a blocked read.
func (r *roamingConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		c := r.current()
		n, addr, err := c.ReadFrom(p)
		if err != nil {
			r.mu.Lock()
			swapped := r.inner != c
			r.mu.Unlock()
			if swapped {
				continue // this socket was retired by roam(); read the new one
			}
			return n, addr, err
		}
		return n, addr, err
	}
}

func (r *roamingConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	return r.current().WriteTo(p, addr)
}

// roam retires the current socket and installs a fresh one on a new local port. Any
// read blocked on the old socket unblocks and re-issues on the new one; the read
// deadline is carried across.
func (r *roamingConn) roam(t *testing.T) {
	t.Helper()
	nc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	r.mu.Lock()
	old := r.inner
	r.inner = nc
	if !r.readDeadline.IsZero() {
		_ = nc.SetReadDeadline(r.readDeadline)
	}
	r.mu.Unlock()
	_ = old.Close()
}

func (r *roamingConn) Close() error {
	return r.current().Close()
}

func (r *roamingConn) LocalAddr() net.Addr {
	return r.current().LocalAddr()
}

func (r *roamingConn) SetDeadline(t time.Time) error {
	_ = r.SetReadDeadline(t)
	return r.SetWriteDeadline(t)
}

func (r *roamingConn) SetReadDeadline(t time.Time) error {
	r.mu.Lock()
	r.readDeadline = t
	c := r.inner
	r.mu.Unlock()
	return c.SetReadDeadline(t)
}

func (r *roamingConn) SetWriteDeadline(t time.Time) error {
	return r.current().SetWriteDeadline(t)
}
