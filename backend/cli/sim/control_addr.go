// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"fmt"
	"net/url"
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
// sim names that port, so a running sim's 'sim status' still reaches it. This reserves
// against saved records, not against unrelated listening processes.
func NextControlAddr(name string) (string, error) {
	records, err := List()
	if err != nil {
		return "", err
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
			return "", fmt.Errorf("parse sim %q control address: %w", r.Name, err)
		}
		if p := u.Port(); p != "" {
			taken[p] = true
		}
	}
	if own != nil && own.ControlAddr != "" {
		if u, err := url.Parse(own.ControlAddr); err == nil && u.Port() != "" && !taken[u.Port()] {
			return own.ControlAddr, nil
		}
	}
	for port := firstControlPort; port <= lastControlPort; port++ {
		if !taken[strconv.Itoa(port)] {
			return fmt.Sprintf("http://localhost:%d", port), nil
		}
	}
	return "", fmt.Errorf("all sim control ports %d-%d are taken by saved sims; pass --control-addr to choose an address", firstControlPort, lastControlPort)
}
