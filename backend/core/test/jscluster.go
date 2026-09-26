// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"net"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// StartJetStreamCluster starts an in-process JetStream cluster of size servers and returns
// them once every server has joined the JetStream meta group, shutting them down when tb
// ends. A client connects to any of them with srv.ClientURL().
//
// Two details decide whether this is a fixture or a flake, and both were learned the hard
// way in core/messaging's replica tests, which this lifts:
//
//   - The route ports are reserved together and released together, so a cluster can never be
//     handed the same port twice; the release-to-bind race that remains is ridden out by
//     retrying the whole construction on fresh ports.
//   - Readiness is every server in the meta group, not a meta leader. A leader exists as soon
//     as a quorum does, and in the window before the last peer joins JetStream places an R1
//     stream but refuses an R3 one with "no suitable peers for placement".
func StartJetStreamCluster(tb testing.TB, size int) []*natsserver.Server {
	tb.Helper()
	const attempts = 3
	for attempt := 1; ; attempt++ {
		servers, err := tryStartJetStreamCluster(tb, size)
		if err == nil {
			tb.Cleanup(func() {
				for _, s := range servers {
					s.Shutdown()
				}
			})
			return servers
		}
		if attempt == attempts {
			tb.Fatalf("could not start a %d-node JetStream cluster in %d attempts: %v", size, attempts, err)
		}
		tb.Logf("cluster attempt %d/%d failed (%v); retrying on fresh ports", attempt, attempts, err)
	}
}

func tryStartJetStreamCluster(tb testing.TB, size int) ([]*natsserver.Server, error) {
	listeners := make([]net.Listener, 0, size)
	ports := make([]int, 0, size)
	for i := 0; i < size; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			for _, open := range listeners {
				open.Close()
			}
			return nil, fmt.Errorf("reserving a port: %w", err)
		}
		listeners = append(listeners, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	for _, l := range listeners {
		l.Close()
	}
	routes := ""
	for _, p := range ports {
		routes += fmt.Sprintf("nats-route://127.0.0.1:%d,", p)
	}
	routes = routes[:len(routes)-1]

	servers := make([]*natsserver.Server, 0, size)
	shutdown := func() {
		for _, s := range servers {
			s.Shutdown()
		}
	}
	for i := 0; i < size; i++ {
		srv, err := natsserver.NewServer(&natsserver.Options{
			Host:       "127.0.0.1",
			Port:       -1,
			ServerName: fmt.Sprintf("n%d", i+1),
			JetStream:  true,
			StoreDir:   tb.TempDir(),
			Cluster: natsserver.ClusterOpts{
				Name: "dctest",
				Host: "127.0.0.1",
				Port: ports[i],
			},
			Routes: natsserver.RoutesFromStr(routes),
		})
		if err != nil {
			shutdown()
			return nil, fmt.Errorf("new clustered nats server %d: %w", i, err)
		}
		go srv.Start()
		servers = append(servers, srv)
	}
	for i, srv := range servers {
		if !srv.ReadyForConnections(15 * time.Second) {
			shutdown()
			return nil, fmt.Errorf("clustered nats server %d not ready", i)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, srv := range servers {
			if len(srv.JetStreamClusterPeers()) == size {
				return servers, nil
			}
		}
		if time.Now().After(deadline) {
			shutdown()
			return nil, fmt.Errorf("JetStream meta group never reached %d peers", size)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
