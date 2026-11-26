// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installjson

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// No Write can finish until cleanup: stop must work even if the sink never does.
type stalledProcessWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	got     bytes.Buffer // inspected only after p.done closes
}

func (w *stalledProcessWriter) resume() { w.once.Do(func() { close(w.release) }) }

func (w *stalledProcessWriter) Write(b []byte) (int, error) {
	select {
	case w.entered <- struct{}{}:
	default:
	}
	<-w.release
	return w.got.Write(b)
}

func TestProcessDrainKeepsEveryReadByte(t *testing.T) {
	for _, chunks := range [][]string{
		{"first secret\nsecond secret\ntail secret"},
		{"first sec", "ret\nsecond sec", "ret\ntail sec", "ret"},
	} {
		w := &stalledProcessWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
		var readers []io.Reader
		for _, s := range chunks {
			readers = append(readers, strings.NewReader(s))
		}
		p := newPipeReader(w, func(s string) string {
			return strings.ReplaceAll(s, "secret", "[redacted]")
		})
		go p.run(io.MultiReader(readers...))
		select {
		case <-w.entered:
		case <-time.After(time.Second):
			w.resume()
			t.Fatal("the test never reached the slow sink")
		}
		select {
		case <-p.done:
			t.Error("drain completed before the destination accepted its bytes")
		default:
		}
		w.resume() // a slow sink recovers within the bound; nothing may drop
		select {
		case <-p.done:
		case <-time.After(time.Second):
			t.Fatal("the recovered sink did not drain")
		}
		if got, want := w.got.String(), "first [redacted]\nsecond [redacted]\ntail [redacted]"; got != want {
			t.Errorf("drained %q, want every line and the final partial line: %q", got, want)
		}
		if result := p.result(); !result.stopped || result.dropped {
			t.Errorf("a fully drained reader reported %+v", result)
		}
	}
}

func TestProcessReaderStopDoesNotWaitForStderr(t *testing.T) {
	w := &stalledProcessWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	t.Cleanup(w.resume)
	p := newPipeReader(w, func(s string) string { return s })
	go p.run(strings.NewReader("first\nsecond\npartial"))
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("the test never reached the stalled sink")
	}
	stopped := make(chan struct{})
	go func() { p.stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop waited for a stderr write that never returns")
	}
	select {
	case <-p.readDone:
	case <-time.After(time.Second):
		t.Fatal("the stalled sink stranded the reader")
	}
	if result := p.result(); result.dropped {
		t.Error("output was dropped before the drain bound expired")
	}
	p.discard()
	if result := p.result(); !result.stopped || !result.dropped {
		t.Errorf("abandoning a stalled write did not record loss: %+v", result)
	}
	w.resume()
	select {
	case <-p.done:
	case <-time.After(time.Second):
		t.Fatal("the writer did not exit after its stalled write returned")
	}
	if got := w.got.String(); got != "first\n" {
		t.Errorf("queued output was written after the bound expired: %q", got)
	}
}

func TestRestoreIsBoundedWithStalledStderr(t *testing.T) {
	for _, ending := range []string{"\n", ""} {
		t.Run("ending="+ending, func(t *testing.T) { testRestoreWithStalledStderr(t, ending) })
	}
}

func testRestoreWithStalledStderr(t *testing.T, ending string) {
	t.Helper()
	prev := processOutputDrain
	processOutputDrain = 50 * time.Millisecond
	t.Cleanup(func() { processOutputDrain = prev })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stdout, os.Stderr = origOut, origErr; _ = r.Close(); _ = w.Close() })
	restore, err := redirectProcessOutput(func(s string) string { return s })
	if err != nil {
		t.Fatal(err)
	}
	// Larger than the destination pipe's capacity. Reading just one byte proves
	// the destination Write began, then its consumer stops draining forever.
	started := make(chan error, 1)
	go func() { var b [1]byte; _, err := r.Read(b[:]); started <- err }()
	if _, err := io.WriteString(os.Stderr, strings.Repeat("x", 1<<20)+ending); err != nil {
		t.Fatal(err)
	}
	result := make(chan processOutputResult, 1)
	go func() { result <- restore() }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("the test never reached the stalled pipe")
	}
	select {
	case drained := <-result:
		if !drained.stopped || !drained.dropped {
			t.Errorf("restore must stop the reader and record dropped output: %+v", drained)
		}
	case <-time.After(time.Second):
		// Release the blocked OS write so even the broken implementation can
		// finish restoring the process globals before the next test.
		_ = r.Close()
		<-result
		t.Fatal("restore exceeded both drain bounds on stalled stderr")
	}
	if os.Stdout != origOut || os.Stderr != w {
		t.Error("restore did not give back the process streams")
	}
}
