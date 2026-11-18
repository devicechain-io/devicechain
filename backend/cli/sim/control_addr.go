// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
)

const (
	firstControlPort = 8090
	lastControlPort  = 8189
)

// NextControlAddr selects the control address for sim name when --control-addr is not
// given: the lowest port in firstControlPort..lastControlPort that no OTHER saved sim
// names. 'sim run' starts dc-simulator on this machine with --port taken from the
// record whatever its host says, so a port counts as taken under any host spelling
// (localhost, 127.0.0.1, [::1] or a name). Re-create reconciles an existing sim: its
// own record never counts against it, and its recorded address is kept while no other
// sim names that port, so a running sim's 'sim status' still reaches it. Pending
// creates also reserve ports; unrelated listening processes do not.
// Call release after saving the record, or on failure. Each call owns its own claim,
// including concurrent re-creates of the same name.
func NextControlAddr(name string) (string, func() error, error) {
	dir, err := stateDir()
	if err != nil {
		return "", nil, err
	}
	records, err := List()
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("create sim state dir: %w", err)
	}
	taken := make(map[string]bool)
	var own *Record
	for _, r := range records {
		if r.Name == name {
			own = r
			continue
		}
		u, err := url.Parse(r.ControlAddr)
		if err != nil {
			return "", nil, fmt.Errorf("parse sim %q control address: %w", r.Name, err)
		}
		if p := u.Port(); p != "" {
			taken[p] = true
		}
	}
	var candidates []string
	if own != nil && own.ControlAddr != "" {
		if u, err := url.Parse(own.ControlAddr); err == nil && u.Port() != "" && !taken[u.Port()] {
			candidates = append(candidates, own.ControlAddr)
		}
	}
	for port := firstControlPort; port <= lastControlPort; port++ {
		if !taken[strconv.Itoa(port)] {
			candidates = append(candidates, fmt.Sprintf("http://localhost:%d", port))
		}
	}
	for _, addr := range candidates {
		u, _ := url.Parse(addr)
		release, ok, err := claimControlPort(dir, u.Port(), name)
		if err != nil {
			return "", nil, err
		}
		if !ok {
			continue
		}
		// A previous holder may have saved and released after our first List.
		// Once claimed, check records again before returning the reservation.
		current, err := List()
		if err != nil {
			return "", nil, errors.Join(err, release())
		}
		conflict := false
		for _, r := range current {
			if r.Name == name {
				continue
			}
			other, err := url.Parse(r.ControlAddr)
			if err != nil {
				return "", nil, errors.Join(fmt.Errorf("parse sim %q control address: %w", r.Name, err), release())
			}
			conflict = conflict || other.Port() == u.Port()
		}
		if !conflict {
			return addr, release, nil
		}
		if err := release(); err != nil {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("all sim control ports %d-%d are taken by saved sims or pending creates; pass --control-addr to choose an address", firstControlPort, lastControlPort)
}
