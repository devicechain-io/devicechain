// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package main

import (
	"errors"
	"os"
)

// platformSupported refuses Windows. Upstream runs there as a Windows service with its own control
// handler (server/service_windows.go), whose reload calls Server.Reload directly — the path that
// silently drops the device TLS setting. Rather than ship a broker that would start and then lose
// the setting on its first reload, this binary does not run there. It still BUILDS there, so the
// package's tests, which need no signals, run on a Windows development machine.
func platformSupported() error {
	return errors.New("dc-nats-server runs on Linux only; on Windows run the upstream nats-server")
}

func notifySignals() chan os.Signal { return nil }

func (b *broker) handleSignals(chan os.Signal) {}
