// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package installtui

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/muesli/cancelreader"
)

// Bubble Tea builds its cancelable reader over the program's input, and from v2.2.0 closes
// it only once its input loop has returned. Its shutdown also reads the replies to queries
// still in flight through a second reader of its own, which it can stop only by cancelling
// it. Both need the input to be a file cancelreader can wait on: given anything else they
// get a reader whose Cancel does nothing, and a shutdown that waits on it hangs until the
// next key.
func TestTheProgramIsGivenAFileBubbleTeaCanCancelItsReadsOn(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	in := programInputOf(t, newStdinGate(r))
	if _, ok := in.(cancelreader.File); !ok {
		t.Fatalf("the program's input %T is not a cancelreader.File: Bubble Tea could not cancel its reads", in)
	}
	cr, err := cancelreader.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	defer cr.Close()
	read := make(chan error, 1)
	go func() {
		_, err := cr.Read(make([]byte, 8)) // waiting: nothing typed
		read <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if !cr.Cancel() {
		t.Fatal("the reader Bubble Tea builds over the program's input cannot cancel a read")
	}
	select {
	case err := <-read:
		if !errors.Is(err, cancelreader.ErrCanceled) {
			t.Fatalf("the cancelled read ended with %v, want %v", err, cancelreader.ErrCanceled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled read never returned")
	}
}

// Once the program has stopped, its reads end at EOF without reading stdin, and what is
// typed next is the plain prompt's.
func TestAStoppedProgramReadsNothing(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	g := newStdinGate(r)
	tea := programInputOf(t, g)
	g.stopProgram()
	g.stopProgram() // a second stop is harmless
	if _, err := w.Write([]byte("y\n")); err != nil {
		t.Fatal(err)
	}
	if n, err := tea.Read(make([]byte, 8)); n != 0 || err != io.EOF {
		t.Fatalf("a program read after the stop = %d, %v; want 0, EOF", n, err)
	}
	line, err := bufio.NewReader(g.handOver()).ReadString('\n')
	if err != nil || line != "y\n" {
		t.Fatalf("the plain prompt read %q, %v; want %q", line, err, "y\n")
	}
}

// When the presenter returns, nothing of the program's is still reading stdin: what is
// typed next is the process's, not a dead program's.
func TestNothingReadsStdinOnceThePresenterHasReturned(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	h := newHarness(t, tuiCaps(true))
	h.p.Stdin = r
	engine := engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		return succeeded, endRun(rt)
	})
	if err := h.result(h.run(engine, installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true})); err != nil {
		t.Fatalf("run: %v; output:\n%q", err, h.out.String())
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		b := make([]byte, 8)
		n, _ := r.Read(b)
		got <- string(b[:n])
	}()
	select {
	case s := <-got:
		if s != "x" {
			t.Fatalf("read %q after the presenter returned, want %q", s, "x")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the byte typed after the presenter returned was taken: the program's reader was still reading stdin")
	}
}
