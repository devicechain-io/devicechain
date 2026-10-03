// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/server"
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

// routeInfoAt is a route Routez could list: server peer's route over a connection whose
// remote address is addr ("" for a connection that is closing).
func routeInfoAt(t *testing.T, peer, addr string) *natsserver.RouteInfo {
	t.Helper()
	if addr == "" {
		return &natsserver.RouteInfo{RemoteName: peer}
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return &natsserver.RouteInfo{RemoteName: peer, IP: host, Port: p}
}

// classifyRoutes puts a route with no address (its connection is closing) among the
// unaddressed and NEVER among the bypasses, leaves a route the mesh carries out of both,
// and names a route around the proxies once however many connections of its pool report
// the same address.
func TestClassifyRoutesKeepsAClosingRouteOutOfTheBypasses(t *testing.T) {
	f, err := newRouteMesh(3)
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	names := []string{"n1", "n2", "n3"}

	routeListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer routeListener.Close()
	direct := routeListener.Addr().String()

	for _, row := range []struct {
		name            string
		routes          []*natsserver.RouteInfo
		wantBypasses    int
		wantUnaddressed int
	}{
		{"a route whose connection is closing", []*natsserver.RouteInfo{routeInfoAt(t, "n2", "")}, 0, 1},
		{"a route the mesh carries", []*natsserver.RouteInfo{routeInfoAt(t, "n2", f.proxyAddr(0, 1))}, 0, 0},
		{"a carried route beside a closing one", []*natsserver.RouteInfo{
			routeInfoAt(t, "n2", f.proxyAddr(0, 1)), routeInfoAt(t, "n3", ""),
		}, 0, 1},
		{"a route around the proxies, listed for three connections of its pool", []*natsserver.RouteInfo{
			routeInfoAt(t, "n3", direct), routeInfoAt(t, "n3", direct), routeInfoAt(t, "n3", direct),
		}, 1, 0},
	} {
		bypasses, unaddressed := f.classifyRoutes(0, row.routes, names)
		if len(bypasses) != row.wantBypasses || len(unaddressed) != row.wantUnaddressed {
			t.Errorf("%s: classifyRoutes found %d bypasses %v and %d unaddressed routes %v; want %d and %d",
				row.name, len(bypasses), bypasses, len(unaddressed), unaddressed, row.wantBypasses, row.wantUnaddressed)
		}
	}
}

// settled holds only for a look at a cluster whose routes are all attributable, complete
// and counted twice by the proxies; take away any one of those and it does not.
func TestRouteCheckIsSettledOnlyWhenEveryConditionHolds(t *testing.T) {
	full := 2 * routeConnsPerPeer // each of three servers, routed to two peers
	base := func() routeCheck {
		return routeCheck{
			counts:  []int{full, full, full},
			remotes: []int{2, 2, 2},
			proxied: 3 * full / 2,
		}
	}
	if !base().settled() {
		t.Fatalf("a cluster with every route complete, attributable and carried is not settled: %+v", base())
	}
	for _, row := range []struct {
		name   string
		change func(*routeCheck)
	}{
		{"a route with no address", func(c *routeCheck) { c.unaddressed = []string{"n1's route to n2"} }},
		{"one more route connection than the proxies carry", func(c *routeCheck) {
			// a route around the proxies at both ends, with a proxied connection that is not
			// (yet) a route: the counts stay complete, and only the total disagrees.
			c.proxied--
		}},
		{"a reading error", func(c *routeCheck) { c.err = errors.New("routez failed") }},
		{"a server short of its complete set", func(c *routeCheck) { c.counts[1]-- }},
		{"a server not routed to every other one", func(c *routeCheck) { c.remotes[2] = 1 }},
	} {
		c := base()
		row.change(&c)
		if c.settled() {
			t.Errorf("%s: settled answered true for %+v", row.name, c)
		}
	}
}
