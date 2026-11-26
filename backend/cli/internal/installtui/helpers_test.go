// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// engineFunc is an installflow.Engine made of a function.
type engineFunc func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error)

func (f engineFunc) Run(ctx context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
	return f(ctx, rt)
}

var (
	succeeded = installflow.Summary{Outcome: installflow.RunSucceeded, InstallRecord: installflow.RecordInstalled,
		Readiness: installflow.ReadinessReady, Claim: installflow.ClaimReleased, PartialState: installflow.EffectApplied}
	cancelled = installflow.Summary{Outcome: installflow.RunCancelled, InstallRecord: installflow.RecordUnknown,
		Readiness: installflow.ReadinessUnknown, Claim: installflow.ClaimUnknown, PartialState: installflow.EffectMayHaveChanged}
)

// startRun emits run.started and opens install.cluster, as a real install does.
func startRun(rt installflow.Runtime) error {
	if err := rt.Events.Emit(installflow.Event{Payload: installflow.RunStarted{
		Target:   installflow.Target{Mode: "kind-create", Context: installflow.Text("kind-devicechain")},
		Manifest: []installflow.ManifestStep{{ID: installflow.StepInstallInputs}, {ID: installflow.StepInstallCluster}},
	}}); err != nil {
		return err
	}
	return rt.Events.Emit(installflow.Event{Step: installflow.StepInstallCluster, Attempt: 1,
		Payload: installflow.StepStarted{Message: "install.cluster"}})
}

// endRun closes install.cluster, so the run may finish as a success.
func endRun(rt installflow.Runtime) error {
	return rt.Events.Emit(installflow.Event{Step: installflow.StepInstallCluster, Attempt: 1,
		Payload: installflow.StepFinished{Outcome: installflow.StepSucceeded}})
}

// syncBuffer is a bytes.Buffer safe for the program's writer and the test's reader.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitFor polls until cond holds, or fails the test with what was on screen.
func waitFor(t *testing.T, what string, out interface{ String() string }, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; output:\n%q", what, out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func screenShows(out *syncBuffer, s string) func() bool {
	return func() bool { return strings.Contains(out.String(), s) }
}

// harness runs a Presenter on a pipe for keys and a buffer for the screen, with the
// process's signals and exit replaced.
type harness struct {
	t     *testing.T
	keys  *io.PipeWriter
	out   *syncBuffer
	sigs  chan chan<- os.Signal
	exits chan int
	p     Presenter
}

func newHarness(t *testing.T, caps terminal.Capabilities) *harness {
	in, keys := io.Pipe()
	t.Cleanup(func() { _ = keys.Close() })
	h := &harness{t: t, keys: keys, out: &syncBuffer{}, sigs: make(chan chan<- os.Signal, 1), exits: make(chan int, 1)}
	h.p = Presenter{
		Stdin: in, Stdout: h.out, Stderr: h.out, Caps: caps,
		notify: func(c chan<- os.Signal) func() { h.sigs <- c; return func() {} },
		exit:   func(code int) { h.exits <- code },
	}
	return h
}

func (h *harness) press(s string) {
	h.t.Helper()
	if _, err := h.keys.Write([]byte(s)); err != nil {
		h.t.Fatal(err)
	}
}

// run starts the presenter and returns a channel with its result.
func (h *harness) run(e installflow.Engine, req installflow.Request) <-chan error {
	done := make(chan error, 1)
	go func() { done <- h.p.Run(context.Background(), e, req, nil) }()
	return done
}

func (h *harness) result(done <-chan error) error {
	h.t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		h.t.Fatalf("the presenter did not return; output:\n%q", h.out.String())
		return nil
	}
}

func tuiCaps(progressOnly bool) terminal.Capabilities {
	return terminal.Capabilities{Eligible: true, TUI: true, ProgressOnly: progressOnly, Level: terminal.L1}
}
