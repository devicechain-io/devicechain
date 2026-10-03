// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"net"
	"strings"
	"testing"
)

// localAddrConn is a connection that reports a local address of the test's choosing, so
// two connections can share one the way the operating system lets two connections to
// different destinations share it.
type localAddrConn struct {
	net.Conn
	local net.Addr
}

func (c localAddrConn) LocalAddr() net.Addr { return c.local }

// loopbackPair returns the two ends of one real loopback connection.
func loopbackPair(t *testing.T) (dialled, accepted net.Conn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := make(chan net.Conn, 1)
	go func() {
		c, _ := l.Accept()
		got <- c
	}()
	dialled, err = net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	accepted = <-got
	if accepted == nil {
		t.Fatal("the loopback listener accepted nothing")
	}
	t.Cleanup(func() {
		dialled.Close()
		accepted.Close()
	})
	return dialled, accepted
}

// attribute answers "" for each end of every route a proxy carries for its pair of
// servers, including one whose connection the proxy has already closed, and a reason for
// everything else: a server's own route listener, another pair's proxy, a dialled
// connection seen at the wrong server or naming the wrong peer, and no address at all.
func TestAttributeAcceptsEveryProxiedEndAndRejectsEverythingElse(t *testing.T) {
	f, err := newRouteMesh(3)
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	names := []string{"n1", "n2", "n3"}

	// A connection the proxy for (n1, n2) dialled to n2, still open.
	in01, out01 := loopbackPair(t)
	if !f.recordDialed(in01, out01, 0, 1) {
		t.Fatal("an open mesh refused to record a dialled connection")
	}
	d01 := out01.LocalAddr().String()

	// A connection the proxy for (n3, n2) dialled to n2, whose pipes have since ended: it
	// goes through the same bookkeeping a real connection's end does.
	in21, out21 := loopbackPair(t)
	if !f.recordDialed(in21, out21, 2, 1) {
		t.Fatal("an open mesh refused to record a dialled connection")
	}
	f.forgetConn(in21, out21)
	d21closed := out21.LocalAddr().String()

	// A server's own route listener: held open, so no proxy can have been given its port.
	routeListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer routeListener.Close()
	real3 := routeListener.Addr().String()

	for _, row := range []struct {
		name   string
		i      int
		peer   string
		remote string
		want   string // "" for carried; otherwise a fragment of the reason
	}{
		{"the dialling end, through the proxy for its pair", 0, "n2", f.proxyAddr(0, 1), ""},
		{"the accepting end of a connection the proxy dialled", 1, "n1", d01, ""},
		{"the accepting end of a connection the proxy has closed", 1, "n3", d21closed, ""},
		{"a server's own route listener", 1, "n3", real3, "neither"},
		{"the proxy listener of another pair, at its dialling server", 0, "n3", f.proxyAddr(0, 1), "not for this pair"},
		{"the proxy listener of another pair, at another server", 2, "n2", f.proxyAddr(0, 1), "not for this pair"},
		{"a dialled connection seen at a server it did not reach", 2, "n1", d01, "neither"},
		{"a dialled connection naming the wrong peer", 1, "n3", d01, "not this pair"},
		{"no address", 0, "n2", "", "not a TCP address"},
	} {
		got := f.attribute(row.i, row.peer, row.remote, names)
		switch {
		case row.want == "" && got != "":
			t.Errorf("%s: %s's route to %s from %q is carried by the mesh, but attribute answered %q",
				row.name, names[row.i], row.peer, row.remote, got)
		case row.want != "" && !strings.Contains(got, row.want):
			t.Errorf("%s: %s's route to %s from %q is not carried by the mesh; attribute answered %q, want "+
				"a reason containing %q", row.name, names[row.i], row.peer, row.remote, got, row.want)
		}
	}
}

// Two connections a proxy dialled to DIFFERENT servers can share a local address, since
// the operating system needs only the whole address pair to differ. Each is still
// attributed to its own pair at the server it reached: one recorded after the other does
// not take its place.
func TestTwoDialledConnectionsSharingALocalAddressAreBothAttributed(t *testing.T) {
	f, err := newRouteMesh(3)
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	names := []string{"n1", "n2", "n3"}

	inA, outA := loopbackPair(t)
	inB, outB := loopbackPair(t)
	shared := outA.LocalAddr()
	// The proxy for (n1, n2) dialled n2, and the proxy for (n1, n3) dialled n3 from the
	// same local address.
	if !f.recordDialed(inA, localAddrConn{Conn: outA, local: shared}, 0, 1) ||
		!f.recordDialed(inB, localAddrConn{Conn: outB, local: shared}, 0, 2) {
		t.Fatal("an open mesh refused to record a dialled connection")
	}
	if why := f.attribute(1, "n1", shared.String(), names); why != "" {
		t.Errorf("n2's route to n1 from %s, dialled by the proxy for (n1, n2), was not attributed: %s", shared, why)
	}
	if why := f.attribute(2, "n1", shared.String(), names); why != "" {
		t.Errorf("n3's route to n1 from %s, dialled by the proxy for (n1, n3), was not attributed: %s", shared, why)
	}
}

// A closed mesh records nothing, and says so.
func TestAClosedMeshRecordsNoDialledConnection(t *testing.T) {
	f, err := newRouteMesh(2)
	if err != nil {
		t.Fatal(err)
	}
	f.close()
	in, out := loopbackPair(t)
	if f.recordDialed(in, out, 0, 1) {
		t.Fatal("a closed mesh recorded a dialled connection")
	}
	if n := f.LiveRouteConnections(); n != 0 {
		t.Fatalf("a closed mesh counts %d live connections after refusing one; want 0", n)
	}
	if why := f.attribute(1, "n1", out.LocalAddr().String(), []string{"n1", "n2"}); why == "" {
		t.Fatal("a connection a closed mesh refused to record was attributed to it")
	}
}
