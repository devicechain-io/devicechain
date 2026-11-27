// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix && !windows

package cmd

import "os/exec"

// procTree on a system with neither process groups nor Job Objects: a plugin is never
// run, since nothing could kill what it starts.
type procTree struct{}

func newProcTree(*exec.Cmd) *procTree { return &procTree{} }
func (*procTree) start() error        { return errNoProcessTree }
func (*procTree) kill()               {}
func (*procTree) close()              {}
