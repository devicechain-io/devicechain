// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// Restart shuts server i down and starts a new one in its place from o, the options it was
// created with (StartJetStreamClusterConfigured's configure hands them out), so with the same
// name and the same store: what a node that crashed and was brought back by its supervisor
// is. It returns once the new server is listening. The new server has to CATCH UP on what
// the others stored while it was away, which is the window a test of a restarted node is
// usually after.
//
// The proxies forward to the new server from then on, and it takes the old one's place in
// the slice the fixture returned, so the cluster's cleanup shuts it down.
func (f *RouteFaults) Restart(i int, o *natsserver.Options) (*natsserver.Server, error) {
	f.mu.Lock()
	if i < 0 || i >= len(f.servers) {
		f.mu.Unlock()
		return nil, fmt.Errorf("no server %d in a cluster of %d", i, len(f.servers))
	}
	old := f.servers[i]
	f.mu.Unlock()
	old.Shutdown()
	old.WaitForShutdown()

	srv, err := natsserver.NewServer(o.Clone())
	if err != nil {
		return nil, fmt.Errorf("restarting nats server %d: %w", i, err)
	}
	log := attachLog(srv)
	f.mu.Lock()
	f.servers[i] = srv
	f.mu.Unlock()
	go srv.Start()
	if err := awaitListening([]*natsserver.Server{srv}, []*serverLog{log}, clusterListenBudget); err != nil {
		srv.Shutdown()
		return nil, err
	}
	return srv, nil
}
