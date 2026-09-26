// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// notEmpty is the error os.RemoveAll returns when a file appeared in a directory
// between its unlink pass and its rmdir: the failure a JetStream consumer's late flush
// causes.
var notEmpty = &os.PathError{Op: "unlinkat", Path: "store/obs/c", Err: syscall.ENOTEMPTY}

// fakeRemoval scripts remove's answers and counts calls and pauses.
type fakeRemoval struct {
	answers []error // answer i for call i; the last answer repeats
	calls   int
	sleeps  int
}

func (f *fakeRemoval) remove(string) error {
	i := f.calls
	f.calls++
	if i >= len(f.answers) {
		i = len(f.answers) - 1
	}
	return f.answers[i]
}

func (f *fakeRemoval) sleep(d time.Duration) {
	if d != storeDirRemovalStep {
		panic("removeStoreDir paused for an unexpected duration")
	}
	f.sleeps++
}

// A directory that was not empty is retried until it goes: this is the one failure a
// single RemoveAll — all t.TempDir()'s cleanup does — cannot survive.
func TestStoreDirRemovalRetriesADirectoryThatWasNotEmpty(t *testing.T) {
	f := &fakeRemoval{answers: []error{notEmpty, notEmpty, nil}}
	err := removeStoreDir("store", f.remove, storeDirRemovalBudget, f.sleep)
	if err != nil {
		t.Fatalf("removeStoreDir = %v, want nil once the third attempt succeeds", err)
	}
	if f.calls != 3 || f.sleeps != 2 {
		t.Fatalf("calls = %d, sleeps = %d; want 3 attempts with 2 pauses between them", f.calls, f.sleeps)
	}
}

// EEXIST is the other answer POSIX allows rmdir to give for a non-empty directory.
func TestStoreDirRemovalRetriesEEXISTToo(t *testing.T) {
	exist := &os.PathError{Op: "unlinkat", Path: "store/obs/c", Err: syscall.EEXIST}
	f := &fakeRemoval{answers: []error{exist, nil}}
	if err := removeStoreDir("store", f.remove, storeDirRemovalBudget, f.sleep); err != nil {
		t.Fatalf("removeStoreDir = %v, want nil", err)
	}
	if f.calls != 2 {
		t.Fatalf("calls = %d, want 2", f.calls)
	}
}

// A store that never empties fails loudly with the last error once the budget is
// spent, rather than being left behind in silence.
func TestStoreDirRemovalGivesUpWithTheLastError(t *testing.T) {
	f := &fakeRemoval{answers: []error{notEmpty}}
	err := removeStoreDir("store", f.remove, 5*storeDirRemovalStep, f.sleep)
	if !errors.Is(err, syscall.ENOTEMPTY) {
		t.Fatalf("removeStoreDir = %v, want the ENOTEMPTY it kept getting", err)
	}
	// Five pauses spend the budget; the attempt after the fifth is the last.
	if f.calls != 6 || f.sleeps != 5 {
		t.Fatalf("calls = %d, sleeps = %d; want 6 attempts and 5 pauses", f.calls, f.sleeps)
	}
}

// Anything but a non-empty directory is not a late flush, and waiting would only hide
// it: it is returned from the first attempt.
func TestStoreDirRemovalDoesNotRetryOtherErrors(t *testing.T) {
	denied := &os.PathError{Op: "unlinkat", Path: "store/obs/c", Err: syscall.EACCES}
	f := &fakeRemoval{answers: []error{denied, nil}}
	err := removeStoreDir("store", f.remove, storeDirRemovalBudget, f.sleep)
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("removeStoreDir = %v, want EACCES returned as is", err)
	}
	if f.calls != 1 || f.sleeps != 0 {
		t.Fatalf("calls = %d, sleeps = %d; want one attempt and no pause", f.calls, f.sleeps)
	}
}

// On a real filesystem: a writer shaped like the server's consumer flusher — write a
// temporary file, rename it over the state file, repeat — is still running inside the
// store when the test ends, and the store is removed anyway, with the test passing.
// With the store taken from t.TempDir() alone, the subtest fails with the same
// "directory not empty" the embedded-server tests flaked on, because the writer here
// never pauses and lands a file inside RemoveAll's window nearly every time.
//
// The writer stops at its first failure to create a file, which is what happens to the
// real flusher once the directory is gone, and in any case after writerLimit, which is
// well inside the removal budget; so this cannot fail on correct code.
func TestAWriteAfterShutdownDoesNotFailCleanup(t *testing.T) {
	const writerLimit = time.Second
	var dir string
	writerStarted := make(chan struct{})
	writerDone := make(chan error, 1)

	passed := t.Run("store", func(st *testing.T) {
		dir = JetStreamStoreDir(st)
		obs := filepath.Join(dir, "obs", "c")
		if err := os.MkdirAll(obs, 0o755); err != nil {
			st.Fatal(err)
		}
		go func() {
			tmp, state := filepath.Join(obs, "o.dat.tmp"), filepath.Join(obs, "o.dat")
			deadline := time.Now().Add(writerLimit)
			close(writerStarted)
			for time.Now().Before(deadline) {
				f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						writerDone <- nil // the directory is gone: the flusher's way out
					} else {
						writerDone <- err
					}
					return
				}
				_, _ = f.Write([]byte("consumer state"))
				_ = f.Close()
				_ = os.Rename(tmp, state)
			}
			writerDone <- nil
		}()
		// Registered last, so it runs first: the writer is live when removal begins.
		st.Cleanup(func() { <-writerStarted })
	})

	if !passed {
		t.Fatal("the subtest failed: a write racing the store's removal failed its cleanup")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the store %s is still there after cleanup (stat err = %v)", dir, err)
	}
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatalf("the writer failed for a reason other than its directory going: %v", err)
		}
	case <-time.After(writerLimit + 5*time.Second):
		t.Fatal("the writer never stopped")
	}
}
