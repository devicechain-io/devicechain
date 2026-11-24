// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package tofurun

import (
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// controlProcess puts tofu in its own process group and replaces the context's hard kill with an
// interrupt to the group, followed by a kill of the group after grace. tofu handles the interrupt
// by finishing the operation it is in, so a cancelled apply does not abandon half a cloud call and
// an unwritten state; whatever it spawned (providers, provisioners) is reaped by the group kill.
// The returned stop disarms the kill once the command has ended.
func controlProcess(cmd *exec.Cmd, grace time.Duration) (stop func()) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var (
		mu    sync.Mutex
		timer *time.Timer
	)
	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		_ = syscall.Kill(-pid, syscall.SIGINT)
		mu.Lock()
		timer = time.AfterFunc(grace, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
		mu.Unlock()
		return nil
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
	}
}
