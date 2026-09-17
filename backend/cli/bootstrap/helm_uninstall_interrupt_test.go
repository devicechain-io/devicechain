// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/release"
)

// signallingWriter records what is written and says when anything was.
type signallingWriter struct {
	mu      sync.Mutex
	b       strings.Builder
	written chan struct{}
}

func newSignallingWriter() *signallingWriter {
	return &signallingWriter{written: make(chan struct{}, 64)}
}

func (w *signallingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.b.Write(p)
	select {
	case w.written <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (w *signallingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// 🔴 AN INTERRUPT IS ANSWERED AT ONCE AND THE UNINSTALL IS STILL WAITED FOR. Helm's
// uninstall takes no context, so the only honest responses to Ctrl-C are to say what is
// happening and keep waiting — and the result returned has to be Helm's, because whether
// the release is gone is what the caller acts on. Returning ctx.Err() instead would make
// a release that WAS removed read as a failed uninstall.
func TestAnInterruptedUninstallIsAcknowledgedOnceAndStillAwaited(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := newSignallingWriter()
	finish := make(chan struct{})
	helmErr := errors.New("helm's own verdict")
	helmRes := &release.UninstallReleaseResponse{Info: "helm's own response"}

	type answer struct {
		v   *release.UninstallReleaseResponse
		err error
	}
	got := make(chan answer, 1)
	started := make(chan struct{})
	go func() {
		v, err := awaitUninstall(ctx, out, "kind-prod", func() (*release.UninstallReleaseResponse, error) {
			close(started)
			<-finish
			return helmRes, helmErr
		})
		got <- answer{v, err}
	}()

	// Interrupted once Helm is under way, not before: an uninstall cancelled before it
	// began is never started.
	<-started
	cancel()
	select {
	case <-out.written:
	case <-time.After(5 * time.Second):
		t.Fatal("an interrupt during the uninstall was not acknowledged while it was still running")
	}
	select {
	case a := <-got:
		t.Fatalf("awaitUninstall returned (%v, %v) while the uninstall was still running; an interrupt "+
			"must not abandon a Helm uninstall that cannot be stopped", a.v, a.err)
	case <-time.After(100 * time.Millisecond):
	}

	close(finish)
	var a answer
	select {
	case a = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("awaitUninstall did not return once the uninstall finished")
	}
	if a.v != helmRes || !errors.Is(a.err, helmErr) {
		t.Fatalf("awaitUninstall returned (%v, %v), want the uninstall's own (%v, %v)", a.v, a.err, helmRes, helmErr)
	}

	said := out.String()
	notice := uninstallInterruptNotice("kind-prod")
	if n := strings.Count(said, notice); n != 1 {
		t.Fatalf("the interrupt notice was printed %d times, want once:\n%s", n, said)
	}
	// On a fresh line: it lands while a doing() line is still open.
	if !strings.HasPrefix(said, "\n") {
		t.Fatalf("the notice does not start on a fresh line, so it is glued to the open progress line:\n%q", said)
	}
	for _, want := range []string{"dcctl instances reclaim --kube-context kind-prod", "dcctl destroy", "10m0s"} {
		if !strings.Contains(said, want) {
			t.Errorf("the notice does not say %q:\n%s", want, said)
		}
	}
}

// The negative control: an uninstall nobody interrupts prints nothing extra.
func TestAnUninterruptedUninstallSaysNothing(t *testing.T) {
	out := newSignallingWriter()
	helmRes := &release.UninstallReleaseResponse{}
	v, err := awaitUninstall(context.Background(), out, "kind-prod", func() (*release.UninstallReleaseResponse, error) {
		return helmRes, nil
	})
	if v != helmRes || err != nil {
		t.Fatalf("got (%v, %v), want (%v, nil)", v, err, helmRes)
	}
	if s := out.String(); s != "" {
		t.Fatalf("an uninterrupted uninstall printed %q", s)
	}
}

// An uninstall interrupted before it began is not begun: nothing was asked of Helm, so
// there is nothing to wait for and no "interrupt received" to print over it.
func TestAnUninstallInterruptedBeforeItBeganIsNotStarted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := newSignallingWriter()
	ran := false
	_, err := awaitUninstall(ctx, out, "kind-prod", func() (*release.UninstallReleaseResponse, error) {
		ran = true
		return nil, nil
	})
	if ran {
		t.Fatal("an uninstall was started on a context already cancelled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want the cancellation", err)
	}
	if s := out.String(); s != "" {
		t.Fatalf("an uninstall that never started printed %q", s)
	}
}

// 🔴 BOTH HELM UNINSTALLS A DESTROY RUNS GO THROUGH IT. Each is a correct call to un.Run
// whether or not it is wrapped, and both sit behind a live cluster — so an uninstall that
// went back to calling un.Run directly would break no behavioural test while an interrupt
// there went silently unanswered again.
func TestEveryDestroyUninstallAcknowledgesAnInterrupt(t *testing.T) {
	for _, fn := range []string{"uninstallRelease", "uninstallProviderRelease"} {
		if got := callsWithin(t, fn, "awaitUninstall"); len(got) == 0 {
			t.Errorf("%s no longer runs its Helm uninstall through awaitUninstall, so Ctrl-C during "+
				"its wait is neither acknowledged nor explained", fn)
		}
	}
}
