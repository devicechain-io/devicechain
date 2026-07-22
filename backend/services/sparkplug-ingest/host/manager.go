// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import "sync"

// supervised is the per-source lifecycle a Manager drives. *Client implements it;
// the interface exists so the Manager's start/stop fan-out can be tested without a
// live MQTT connection.
type supervised interface {
	Connect()
	Stop()
}

// Manager supervises one tenant-bound Host Application connection per configured
// source (ADR-069, connection-scoped tenancy). It owns the lifecycle of every
// connection: Start opens them all, Stop closes them all.
//
// Readiness is deliberately NOT gated on any broker being reachable. Each source
// connects OUT to a customer-operated broker that may be briefly unreachable; that
// must degrade the one source, never take the pod down. So the service marks ready
// once the Manager has started supervising (Start returns immediately — the
// per-connection reconnect loops run in the background), and an empty source set is
// a valid, ready, inert configuration.
type Manager struct {
	clients []supervised
}

// NewManager builds a Manager over an already-constructed set of clients.
func NewManager(clients []*Client) *Manager {
	sup := make([]supervised, len(clients))
	for i, c := range clients {
		sup[i] = c
	}
	return &Manager{clients: sup}
}

// Start connects every managed client. Each Connect returns immediately, so Start
// does not block on broker reachability.
func (m *Manager) Start() {
	for _, c := range m.clients {
		c.Connect()
	}
}

// Stop stops every managed client concurrently, each announcing a graceful OFFLINE
// STATE on its own broker. Concurrent so a slow or unreachable broker on one source
// does not serialize the whole shutdown behind another's disconnect quiesce.
func (m *Manager) Stop() {
	var wg sync.WaitGroup
	for _, c := range m.clients {
		wg.Add(1)
		go func(c supervised) {
			defer wg.Done()
			c.Stop()
		}(c)
	}
	wg.Wait()
}
