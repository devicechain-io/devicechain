// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installjson

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
)

const procoutSecret = "dcCanaryPluginSecret4Tg8Wq"

// TestPluginChild is a stand-in credential plugin: started by the test below, it
// prints its credential on the stderr it inherited, as a plugin debugging itself
// does. It does nothing unless started that way.
func TestPluginChild(t *testing.T) {
	if v := os.Getenv("DC_PROCOUT_HOLD"); v != "" {
		// A plugin that prints half a line, leaves a helper holding stderr, and is
		// gone: here, one process that does the first and then never closes it.
		fmt.Fprintf(os.Stderr, "plugin: partial %s", v)
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	if v := os.Getenv("DC_PROCOUT_CHILD"); v != "" {
		fmt.Fprintf(os.Stderr, "plugin: using token %s\n", v)
		os.Exit(0)
	}
}

// 🔴 WHAT REACHES THE PROCESS'S OWN STREAMS IS REDACTED TOO, in json mode: a child
// started with os.Stderr (client-go starts a kubeconfig exec credential plugin that
// way), a write to os.Stderr, a print to os.Stdout. And the error the command prints
// is redacted, tool diagnostics appended to it included.
func TestProcessOutputAndTheErrorAreRedacted(t *testing.T) {
	real := filepath.Join(t.TempDir(), "stderr")
	f, err := os.Create(real)
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = f, f
	t.Cleanup(func() { os.Stdout, os.Stderr = origOut, origErr; _ = f.Close() })

	e := engineFunc(func(_ context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
		emit(t, rt, installflow.Event{Payload: installflow.RunStarted{Target: installflow.Target{Mode: "managed-kind"}, Manifest: manifest()}})
		if err := rt.Events.AddSecret(procoutSecret); err != nil {
			t.Error(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestPluginChild$", "-test.count=1")
		cmd.Env = append(os.Environ(), "DC_PROCOUT_CHILD="+procoutSecret)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Errorf("the plugin stand-in: %v", err)
		}
		fmt.Fprintln(os.Stderr, "direct stderr "+procoutSecret)
		fmt.Println("direct stdout " + procoutSecret)
		return installflow.Summary{}, errors.New("apply failed\nError: provider says " + procoutSecret)
	})
	var stream bytes.Buffer
	runErr := Presenter{Stdout: &stream, RunID: "procout", Clock: testkit.NewManualClock(start), RedactProcessOutput: true}.Run(
		context.Background(), e, installflow.Request{Operation: installflow.OperationInstall}, nil)
	if os.Stdout != f || os.Stderr != f {
		t.Fatal("the process's streams were not given back")
	}
	if runErr == nil || strings.Contains(runErr.Error(), procoutSecret) || !strings.Contains(runErr.Error(), "[redacted]") {
		t.Errorf("the returned error: %v", runErr)
	}
	got, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte(procoutSecret)) {
		t.Errorf("the process's stderr carries the secret:\n%s", got)
	}
	for _, want := range []string{"plugin: using token [redacted]", "direct stderr [redacted]", "direct stdout [redacted]"} {
		if !bytes.Contains(got, []byte(want)) {
			t.Errorf("stderr lacks %q:\n%s", want, got)
		}
	}
	if bytes.Contains(stream.Bytes(), []byte(procoutSecret)) {
		t.Errorf("the stream carries the secret")
	}
}

// 🔴 A CHILD THAT KEEPS THE PIPE OPEN NEITHER LOSES WHAT IT WROTE NOR STRANDS THE
// READER. The stand-in writes half a line holding the secret and keeps the pipe; on
// restore, past the drain bound, that half line is written, redacted, and the reader
// stops — restore says so, and returns within its bounds.
func TestRestoreFlushesAPartialLineAndStopsTheReader(t *testing.T) {
	prev := processOutputDrain
	processOutputDrain = 300 * time.Millisecond
	t.Cleanup(func() { processOutputDrain = prev })

	real := filepath.Join(t.TempDir(), "stderr")
	f, err := os.Create(real)
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = f, f
	t.Cleanup(func() { os.Stdout, os.Stderr = origOut, origErr; _ = f.Close() })

	restore, err := redirectProcessOutput(func(s string) string { return strings.ReplaceAll(s, procoutSecret, "[redacted]") })
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPluginChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "DC_PROCOUT_HOLD="+procoutSecret)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		restore()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	// Time for the child to start and write its half line. Nothing reaches the real
	// stderr before restore: a partial line is held until it ends or is flushed.
	time.Sleep(time.Second)
	if fi, err := os.Stat(real); err != nil || fi.Size() != 0 {
		t.Fatalf("stderr before restore: %v, %v", fi, err)
	}
	start := time.Now()
	stopped := restore()
	if took := time.Since(start); took > 3*processOutputDrain {
		t.Errorf("restore took %s", took)
	}
	if !stopped {
		t.Error("the reader was left blocked on the pipe a child holds")
	}
	got, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "plugin: partial [redacted]" {
		t.Errorf("stderr is %q, want the partial line, redacted", got)
	}
}
