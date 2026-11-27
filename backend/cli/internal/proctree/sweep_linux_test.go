// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proctree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeProc is a procfs root with n process entries, none of them dcctl's, each with an
// environ that does not carry the mark. Their pids are past any pid_max, so nothing can
// be signalled through them.
func fakeProc(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < n; i++ {
		dir := filepath.Join(root, strconv.Itoa(1<<30+i))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "environ"), []byte("HOME=/root\x00PATH=/bin\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Not processes: skipped without a read.
	_ = os.Mkdir(filepath.Join(root, "self"), 0o700)
	_ = os.WriteFile(filepath.Join(root, "uptime"), []byte("1 1"), 0o600)
	return root
}

func withProcRoot(t *testing.T, root string) {
	t.Helper()
	old := procRoot
	t.Cleanup(func() { procRoot = old })
	procRoot = root
}

const testMark = sweepEnv + "=0123456789abcdef0123456789abcdef"

// A sweep that has scanned everything says so.
func TestASweepThatFinishesIsComplete(t *testing.T) {
	withProcRoot(t, fakeProc(t, 200))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !sweep(ctx, testMark) {
		t.Fatal("a sweep with time to spare reads incomplete")
	}
}

// 🔴 A SWEEP IS BOUNDED: past its deadline it stops, however many processes are left, and
// says it is incomplete rather than claiming clean.
func TestASweepStopsAtItsDeadlineAndSaysSo(t *testing.T) {
	withProcRoot(t, fakeProc(t, 4000))
	// Long enough to get the slot and start, far too short to read 4000 environments.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Millisecond)
	defer cancel()
	start := time.Now()
	complete := sweep(ctx, testMark)
	if complete {
		t.Error("a sweep past its deadline reads complete")
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("a sweep past its deadline took %v", took)
	}
}

// 🔴 AN ENVIRON IS READ ONLY UP TO environCap: the mark is a short, fixed-length entry,
// and an environment of megabytes is not read to its end. A mark past the cap is not
// seen; one inside it is.
func TestAnEnvironIsReadOnlyUpToTheCap(t *testing.T) {
	root := t.TempDir()
	withProcRoot(t, root)
	write := func(pid int, env string) {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "environ"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pad := strings.Repeat("X", 4*environCap)
	write(1, "PAD="+pad+"\x00"+testMark+"\x00")
	write(2, testMark+"\x00PAD="+pad+"\x00")
	write(3, "A=1\x00"+testMark)
	write(4, "A=1\x00"+testMark+"0\x00") // a longer value is not the mark
	// The cap falls just after what looks like the mark, inside a longer entry: what was
	// cut off is not known, so it is not the mark.
	write(5, "PAD="+strings.Repeat("X", environCap-5-len(testMark))+"\x00"+testMark+"0\x00")
	for pid, want := range map[int]bool{1: false, 2: true, 3: true, 4: false, 5: false} {
		if got := marked(pid, testMark); got != want {
			t.Errorf("pid %d: marked %v, want %v", pid, got, want)
		}
	}
}

// 🔴 SWEEPS NEVER RUN AT ONCE: a second waits for the first, and one whose deadline passes
// while it waits stops, incomplete, without scanning.
func TestSweepsAreSerialised(t *testing.T) {
	withProcRoot(t, fakeProc(t, 10))
	sweepSlot <- struct{}{} // a sweep in progress
	released := false
	defer func() {
		if !released {
			<-sweepSlot
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if sweep(ctx, testMark) {
		t.Error("a sweep that never got its turn reads complete")
	}
	if took := time.Since(start); took < 40*time.Millisecond {
		t.Errorf("the second sweep did not wait for the first (%v)", took)
	}
	<-sweepSlot
	released = true
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if !sweep(ctx2, testMark) {
		t.Error("a sweep after the first ended reads incomplete")
	}
}

// RunWith says when its sweep did not finish: the command ran cleanly, but what it left
// was not all looked for, so the run is not reported clean.
func TestRunWithReportsAnIncompleteSweep(t *testing.T) {
	sweepSlot <- struct{}{} // another sweep holds the slot past this one's budget
	defer func() { <-sweepSlot }()
	err := RunWith(context.Background(), exec.Command("true"), Options{Sweep: true})
	if !errors.Is(err, ErrSweepIncomplete) {
		t.Fatalf("RunWith returned %v", err)
	}
	if err := RunWith(context.Background(), exec.Command("true"), Options{}); err != nil {
		t.Fatalf("without a sweep, RunWith returned %v", err)
	}
}
