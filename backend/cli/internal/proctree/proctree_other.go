// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !unix && !windows

package proctree

import (
	"context"
	"os/exec"
)

// RunWith starts cmd and waits for it, killing it alone if ctx ends first: with neither
// process groups nor job objects, its descendants cannot be ended with it, and the
// Options have nothing to act on.
func RunWith(ctx context.Context, cmd *exec.Cmd, _ Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	}
}
