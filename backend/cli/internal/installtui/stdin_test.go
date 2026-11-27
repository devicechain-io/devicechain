// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"bufio"
	"io"
	"os"
	"testing"
	"time"
)

// When the screen gives stdin up, a read the program already started must not take the
// answer meant for the plain prompt: what it reads after the handover is passed on, and
// from then on there is one reader of stdin.
func TestStdinHandoverLosesNoInput(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	g := newStdinGate(r)
	tea := programInputOf(t, g)

	programRead := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := tea.Read(buf) // blocked: nothing typed yet
		programRead <- buf[:n]
	}()
	time.Sleep(100 * time.Millisecond)

	plain := bufio.NewReader(g.handOver())
	if _, err := w.Write([]byte("y\n")); err != nil {
		t.Fatal(err)
	}
	answer := make(chan string, 1)
	go func() {
		line, _ := plain.ReadString('\n')
		answer <- line
	}()
	select {
	case got := <-answer:
		if got != "y\n" {
			t.Fatalf("the plain prompt read %q, want %q", got, "y\n")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the plain prompt never got the answer: the program's reader took it")
	}
	select {
	case b := <-programRead:
		if len(b) != 0 {
			t.Fatalf("the program's reader returned %q after the handover", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the program's read never returned")
	}
	if n, err := tea.Read(make([]byte, 8)); n != 0 || err != io.EOF {
		t.Fatalf("a program read after the handover = %d, %v; want 0, EOF", n, err)
	}
}

// fifoReader hands each chunk to the read that has waited longest, as a terminal does with
// two blocked readers: a channel serves its waiting receivers in order.
type fifoReader chan []byte

func (f fifoReader) Read(p []byte) (int, error) {
	b, ok := <-f
	if !ok {
		return 0, io.EOF
	}
	return copy(p, b), nil
}

// The program's read was waiting first, so it is given the answer; the plain prompt must
// still receive it, and must not be left waiting on stdin beside it.
func TestTheProgramsWaitingReadPassesTheAnswerOn(t *testing.T) {
	in := make(fifoReader)
	g := newStdinGate(in)
	program := programInputOf(t, g)
	go func() { _, _ = program.Read(make([]byte, 64)) }()
	time.Sleep(100 * time.Millisecond) // the program's read is waiting
	plain := bufio.NewReader(g.handOver())
	answer := make(chan string, 1)
	go func() {
		line, _ := plain.ReadString('\n')
		answer <- line
	}()
	time.Sleep(100 * time.Millisecond) // and so is the prompt
	in <- []byte("y\n")
	select {
	case got := <-answer:
		if got != "y\n" {
			t.Fatalf("the plain prompt read %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the answer went to the program's read and never reached the prompt")
	}
}

// Before the handover the program reads stdin directly.
func TestStdinGatePassesInputToTheProgram(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	g := newStdinGate(r)
	if _, err := w.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	if n, err := programInputOf(t, g).Read(buf); err != nil || string(buf[:n]) != "q" {
		t.Fatalf("read %q, %v", buf[:n], err)
	}
}

func programInputOf(t *testing.T, g *stdinGate) io.Reader {
	t.Helper()
	return g.programInput()
}
