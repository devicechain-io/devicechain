// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package tofurun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// A cancelled command is interrupted, not left to the end of the grace: tofu exits on the interrupt.
func TestCancelInterruptsRatherThanWaitingForTheGrace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sleep", "60")
	stop := controlProcess(cmd, 30*time.Second)
	defer stop()
	cmd.WaitDelay = 40 * time.Second
	require.NoError(t, cmd.Start())
	cancel()
	start := time.Now()
	_ = cmd.Wait()
	require.Less(t, time.Since(start), 5*time.Second, "the interrupt stopped it; the 30s grace was not waited out")
}

// A process that ignores the interrupt is killed, group and all, once the grace has passed.
func TestCancelKillsAStubbornGroupAfterTheGrace(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", `trap '' INT; sleep 60 & echo $! > `+pidFile+`; wait`)
	stop := controlProcess(cmd, 1*time.Second)
	defer stop()
	cmd.WaitDelay = 20 * time.Second
	require.NoError(t, cmd.Start())
	require.Eventually(t, func() bool { b, _ := os.ReadFile(pidFile); return len(b) > 0 }, 5*time.Second, 20*time.Millisecond)
	cancel()
	start := time.Now()
	_ = cmd.Wait()
	took := time.Since(start)
	require.GreaterOrEqual(t, took, 900*time.Millisecond, "the interrupt was ignored, so only the grace's end stops it")
	require.Less(t, took, 10*time.Second)
	b, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	require.Eventually(t, func() bool { return !alive(pid) }, 3*time.Second, 50*time.Millisecond)
}
