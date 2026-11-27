// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proctree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// signals records each group Run signals, and whether its leader was still unreaped
// (alive, or a zombie: either way /proc still has it) when it was signalled.
func recordSignals(t *testing.T) func() (calls int, afterReap []int) {
	t.Helper()
	real := signalGroup
	t.Cleanup(func() { signalGroup = real })
	var mu sync.Mutex
	var n int
	var reaped []int
	signalGroup = func(pgid int) error {
		mu.Lock()
		n++
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pgid)); err != nil {
			reaped = append(reaped, pgid)
		}
		mu.Unlock()
		return real(pgid)
	}
	return func() (int, []int) {
		mu.Lock()
		defer mu.Unlock()
		return n, append([]int(nil), reaped...)
	}
}

// 🔴 A GROUP IS NEVER SIGNALLED ONCE ITS LEADER HAS BEEN REAPED. From then on the pgid
// may name someone else's group. Here the leader exits at once, leaving a child that
// holds its output open, and the context ends while Wait is still waiting for that
// output: the leader is reaped by then, so the group must not be signalled at all.
func TestAGroupIsNeverSignalledAfterItsLeaderIsReaped(t *testing.T) {
	signals := recordSignals(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $!")
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	time.AfterFunc(300*time.Millisecond, cancel)
	_ = Run(ctx, cmd)
	if pid, err := strconv.Atoi(strings.TrimSpace(out.String())); err == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	if n, after := signals(); len(after) != 0 || n != 0 {
		t.Fatalf("%d group signal(s), to %v after their leader was reaped; a leader that exited on its own is not chased", n, after)
	}
}

// The counterweight: a context that ends while the leader runs signals its group, once,
// with the leader not yet reaped, and Run returns the context's error.
func TestACancelledRunSignalsTheGroupBeforeReapingItsLeader(t *testing.T) {
	signals := recordSignals(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.Command("sh", "-c", "sleep 30 & sleep 30")
	cmd.WaitDelay = time.Second
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	if err := Run(ctx, cmd); err != context.Canceled {
		t.Fatalf("Run returned %v, want the context's error", err)
	}
	if d := time.Since(start); d > 900*time.Millisecond {
		t.Fatalf("the cancelled group took %v to end; its output should close with it", d)
	}
	if n, after := signals(); n != 1 || len(after) != 0 {
		t.Fatalf("%d group signal(s), %v after the leader was reaped; want exactly one before", n, after)
	}
}
