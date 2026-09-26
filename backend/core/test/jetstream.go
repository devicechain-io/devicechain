// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

// JetStreamStoreDir returns an empty directory for an embedded nats-server's JetStream
// store, removed when tb finishes. Use it wherever a test would have written
// StoreDir: t.TempDir(), or put a store_dir in a server config file.
//
// 🔴 t.TempDir() ALONE IS NOT SAFE FOR A JETSTREAM STORE. The server writes a consumer's
// state from a flusher goroutine that Shutdown does not wait for, so a write can land in
// obs/<consumer>/ after Shutdown has returned. TempDir's cleanup removes the tree once —
// off Windows it retries nothing — and a file created between its unlink pass and its
// rmdir fails the test with "unlinkat …/obs/<consumer>: directory not empty", in a test
// that passed. WaitForShutdown does not help: Shutdown closes the channel it waits on as
// its own last statement, so called after Shutdown it returns at once.
//
// This removes the store first, retrying while a directory in it was not empty. The
// retry terminates: the flusher writes files but never creates a directory, so once the
// tree is gone its next write fails and it stops.
//
// Call it before the server's Shutdown is registered with tb.Cleanup (cleanups run last
// in, first out), or shut the server down in the test body — the ordering t.TempDir()
// already needed.
func JetStreamStoreDir(tb testing.TB) string {
	tb.Helper()
	// Built on TempDir, so the store keeps Go's per-test naming and location and is
	// still swept by TempDir's own cleanup, which runs after this one and finds nothing.
	dir := tb.TempDir()
	tb.Cleanup(func() {
		if err := removeStoreDir(dir, os.RemoveAll, storeDirRemovalBudget, time.Sleep); err != nil {
			tb.Errorf("remove JetStream store dir %s: %v", dir, err)
		}
	})
	return dir
}

// storeDirRemovalBudget bounds the retry. A consumer's flush is one small file write,
// and the server makes at most one after its consumers have been stopped.
const storeDirRemovalBudget = 5 * time.Second

// storeDirRemovalStep is the pause between attempts.
const storeDirRemovalStep = 10 * time.Millisecond

// removeStoreDir removes dir, retrying only while the failure is a directory that was
// not empty when rmdir reached it (POSIX allows either ENOTEMPTY or EEXIST for that).
// Any other error is returned at once, and the last error is returned when the budget
// is spent — a store that cannot be removed fails the test rather than leaking quietly.
//
// The budget is counted in the pauses taken rather than in wall-clock time, so a test
// can drive it with a fake sleep and get the same answer on any machine.
func removeStoreDir(dir string, remove func(string) error, budget time.Duration, sleep func(time.Duration)) error {
	var waited time.Duration
	for {
		err := remove(dir)
		if err == nil || !(errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)) {
			return err
		}
		if waited >= budget {
			return err
		}
		sleep(storeDirRemovalStep)
		waited += storeDirRemovalStep
	}
}
