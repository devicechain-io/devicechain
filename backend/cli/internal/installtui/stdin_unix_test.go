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

// Bubble Tea v2.1.1, killed or recovering a panic, closes the cancelable reader it built
// over its input while its input loop may still be waiting in it. So the program's input
// over a terminal file must still be a file to Bubble Tea (a descriptor, for raw mode) but
// never one cancelreader will build its reader over.
func TestTheProgramIsNotGivenAReaderBubbleTeaCanClose(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	in := programInputOf(t, newStdinGate(r))
	if _, ok := in.(interface{ Fd() uintptr }); !ok {
		t.Fatalf("the program's input %T has no descriptor: Bubble Tea could not put the terminal in raw mode", in)
	}
	if _, ok := in.(cancelreader.File); ok {
		t.Fatalf("the program's input %T is a cancelreader.File: Bubble Tea would build, and close, its own reader over it", in)
	}
}

// The gate's own reader is stopped once the program has gone: a read under way returns at
// once and takes no input, and what is typed next is the plain prompt's.
func TestStoppingTheProgramCancelsItsReadWithoutTakingInput(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	g := newStdinGate(r)
	tea := programInputOf(t, g)

	type result struct {
		n   int
		err error
	}
	programRead := make(chan result, 1)
	go func() {
		n, err := tea.Read(make([]byte, 64)) // waiting: nothing typed yet
		programRead <- result{n, err}
	}()
	time.Sleep(100 * time.Millisecond)

	stopped := make(chan struct{})
	go func() { g.stopProgram(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stopping the program waited for input: its read was not cancelled")
	}
	select {
	case res := <-programRead:
		if res.n != 0 || !errors.Is(res.err, cancelreader.ErrCanceled) {
			t.Fatalf("the program's cancelled read = %d, %v; want 0, %v", res.n, res.err, cancelreader.ErrCanceled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the program's read never returned")
	}
	if n, err := tea.Read(make([]byte, 8)); n != 0 || err != io.EOF {
		t.Fatalf("a program read after the stop = %d, %v; want 0, EOF", n, err)
	}
	g.stopProgram() // a second stop is harmless

	if _, err := w.Write([]byte("y\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(g.handOver()).ReadString('\n')
	if err != nil || line != "y\n" {
		t.Fatalf("the plain prompt read %q, %v; want %q", line, err, "y\n")
	}
}

// A stop that lands while the read is waking, or before it has begun, still ends it the
// way the program's input loop ends cleanly: cancelled, or at EOF. A reader closed under a
// read still in its wait answers neither (it reads the descriptors the close tore down).
func TestStoppingTheProgramMidReadAlwaysEndsItCleanly(t *testing.T) {
	for i := 0; i < 200; i++ {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		g := newStdinGate(r)
		tea := programInputOf(t, g)
		done := make(chan error, 1)
		go func() {
			_, err := tea.Read(make([]byte, 8))
			done <- err
		}()
		g.stopProgram()
		select {
		case err := <-done:
			if !errors.Is(err, cancelreader.ErrCanceled) && err != io.EOF {
				t.Fatalf("iteration %d: the stopped read ended with %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: the stopped read never returned", i)
		}
		_ = r.Close()
		_ = w.Close()
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
