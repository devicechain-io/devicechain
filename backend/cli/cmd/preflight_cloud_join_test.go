// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"k8s.io/client-go/rest"
)

// A probe is joined, the way the screen's commands are (commandWaitDelay): within does
// not answer while the probe it gave up on is still running, so nothing a run started
// outlives the run.
func TestAProbeIsJoinedBeforeItsAnswer(t *testing.T) {
	var ended atomic.Bool
	_, err := within(context.Background(), 20*time.Millisecond, func(ctx context.Context) (int, error) {
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond) // winding down after its cancel
		ended.Store(true)
		return 1, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a probe past its deadline answered %v", err)
	}
	if !ended.Load() {
		t.Fatal("within answered while the probe it gave up on was still running")
	}
}

// The join is bounded: a probe that ignores its cancel is given up on after the join
// delay, never waited for without end.
func TestAProbeJoinIsBounded(t *testing.T) {
	old := probeJoinDelay
	t.Cleanup(func() { probeJoinDelay = old })
	probeJoinDelay = 100 * time.Millisecond
	release := make(chan struct{})
	defer close(release)
	start := time.Now()
	_, err := within(context.Background(), 20*time.Millisecond, func(context.Context) (int, error) {
		<-release // ignores its context
		return 1, nil
	})
	if took := time.Since(start); took > time.Second || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a probe that ignores its cancel: %v after %v", err, took)
	}
	if old != commandWaitDelay {
		t.Errorf("the join delay is %v, want the screen commands' %v", old, commandWaitDelay)
	}
}

// An Exit (the screen's context ending) cancels the request in flight, rather than
// leaving it to run out its own timeout after the run has gone.
func TestAnExitCancelsTheVersionRequest(t *testing.T) {
	var cancelled atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			cancelled.Store(true)
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()
	p, err := cloudProbesFrom(context.Background(), &rest.Config{Host: srv.URL, BearerToken: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	d := &doctor{observe: func(installflow.PreflightObservation) {}}
	start := time.Now()
	d.checkAPIAnswers(ctx, p, "127.0.0.1", "prod")
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("the probe took %v after an Exit", took)
	}
	deadline := time.Now().Add(time.Second)
	for !cancelled.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !cancelled.Load() {
		t.Fatal("the version request was still running after the Exit")
	}
}
