// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 🔴 AN INTERRUPTED COMMAND THAT NEVER COMES BACK IS GIVEN UP ON, AND SAYS SO. This is
// the case dcctl had no answer for: terraform-exec reads both pipes to EOF before it
// calls Wait, so a process that outlived tofu holding the inherited pipe left dcctl
// blocked with no timer anywhere that could fire.
func TestAnInterruptedCommandThatNeverReturnsIsAbandoned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	blocked := make(chan struct{})
	// Released at the end so the parked goroutine can finish rather than outlive the
	// test binary; in dcctl it is deliberately left parked, because there is nothing
	// to release it with and the process is on its way out.
	t.Cleanup(func() { close(blocked) })

	err := runUntilAbandoned(ctx, 20*time.Millisecond, func() error {
		<-blocked
		return nil
	})
	if !errors.Is(err, errTofuAbandoned) {
		t.Fatalf("an interrupted command that never returned did not produce the abandon error: %v", err)
	}

	// 🔴 THE SENTENCE IS THE POINT, NOT THE SENTINEL. "It failed" and "it was left
	// mid-flight" call for different next moves, and only the second is true here:
	// dcctl stopped LOOKING, it did not stop the work. An operator who reads this as a
	// failure and assumes nothing happened is the way this turns into a surprise.
	for _, want := range []string{"PARTIALLY APPLIED", "Re-run", "idempotent", "20ms"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the abandon message does not mention %q, so it does not tell the operator "+
				"what state their infrastructure is in or what to do:\n%s", want, err)
		}
	}
}

// The other half of the interrupted case, and the one a naive watchdog gets wrong: an
// interrupt normally ends with tofu stopping gracefully and RETURNING, and that result
// must reach the caller unaltered. Nothing here sleeps — the command is made to return
// on the cancellation itself, which is exactly the instant the two racers are closest.
func TestAnInterruptedCommandThatFinishesReturnsItsOwnResult(t *testing.T) {
	stopped := errors.New("tofu stopped gracefully and wrote its state")

	// Both budgets, because they fail differently. An hour is the production shape: the
	// timer is armed and never fires. Zero is the boundary: the timer has ALREADY fired
	// by the time the result is looked for, so a result in hand has to beat an expired
	// budget rather than lose a coin toss to it.
	for _, budget := range []time.Duration{time.Hour, 0} {
		ctx, cancel := context.WithCancel(context.Background())
		err := runUntilAbandoned(ctx, budget, func() error {
			cancel()
			<-ctx.Done()
			return stopped
		})
		if !errors.Is(err, stopped) {
			t.Errorf("budget %s: a cancelled command that returned its own result was reported "+
				"as abandoned instead: %v", budget, err)
		}
		cancel()
	}
}

// 🔴 A LIVE RUN IS NOT ON A CLOCK. The whole reason the timer is armed only after the
// context is cancelled is that a legitimate apply routinely runs for tens of minutes,
// and a watchdog armed at entry is a kill switch on it.
func TestALiveCommandIsNeverAbandoned(t *testing.T) {
	// The control comes first. "Nothing fired" is also what a deleted watchdog, a
	// never-armed timer and a budget that silently means "infinite" all look like, so
	// this proves a zero budget on a CANCELLED context fires at once — which is what
	// makes the same zero budget not firing below into evidence rather than an absence.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	held := make(chan struct{})
	t.Cleanup(func() { close(held) })
	if err := runUntilAbandoned(cancelled, 0, func() error { <-held; return nil }); !errors.Is(err, errTofuAbandoned) {
		t.Fatalf("a zero budget on a cancelled context did not fire, so the check below would "+
			"pass over a watchdog that had been removed entirely: %v", err)
	}

	release := make(chan struct{})
	finished := errors.New("the apply's own result")
	out := make(chan error, 1)
	go func() {
		out <- runUntilAbandoned(context.Background(), 0, func() error {
			<-release
			return finished
		})
	}()

	// An assertion of absence, so it is bounded by a wait rather than by a handshake.
	// The wait can only make this test too LENIENT, never flaky-red: if the timer were
	// armed at entry it would be a zero-length one, fired before this select is even
	// reached, as the control above just demonstrated.
	select {
	case err := <-out:
		t.Fatalf("a command nobody interrupted was abandoned on a zero budget: %v", err)
	case <-time.After(250 * time.Millisecond):
	}

	close(release)
	if err := <-out; !errors.Is(err, finished) {
		t.Fatalf("the live command's own result did not reach the caller: %v", err)
	}
}

// 🔴 THE DEFECT ITSELF, REPRODUCED. The three tests above exercise runUntilAbandoned
// against a function that is simply made not to return; this one drives a real
// terraform-exec run against a fake tofu that does what a provider plugin does — starts
// something that inherits its stdout and outlives it. terraform-exec then sits in
// ReadBytes waiting for an EOF that will not come, with tofu already gone, and no
// WaitDelay or signal anywhere in the stack ends it. It is asserted here that the hang
// is real BEFORE the interrupt, because a fixture that stopped reproducing it would
// leave a green test proving nothing.
func TestAnInterruptedApplyWhoseOutputPipeIsHeldIsAbandoned(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "spawned")
	// `sleep 3 &` inherits the stdout pipe tofu was given and outlives the `exit 0`
	// below — grandchild, orphan, still holding the write end.
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"  version) echo '{\"terraform_version\":\"1.8.0\",\"platform\":\"linux_amd64\",\"provider_selections\":{}}' ;;\n" +
		"  apply)   sleep 3 & echo PROGRESS-apply; : > " + ready + "; exit 0 ;;\n" +
		"  *)       echo \"unexpected subcommand $1\" >&2; exit 1 ;;\n" +
		"esac\n"
	bin := filepath.Join(dir, "tofu")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var applyErr error
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		// os.Stdout is NOT redirected here: capturing it means restoring it from this
		// goroutine while a parked terraform-exec goroutine still holds the writer, and
		// the helper that does it reports its own failures with t.Fatalf, which is not
		// valid off the test goroutine. One PROGRESS-apply line in the log is cheaper.
		tf, err := newTofuExec(t.TempDir(), bin)
		if err != nil {
			applyErr = err
			return
		}
		tf.abandonBudget = 50 * time.Millisecond
		applyErr = tf.Apply(ctx)
	}()

	// Wait for the fake to have spawned the holder and exited.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake tofu never ran its apply, so nothing was reproduced")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The negative control: tofu has exited and the apply must STILL not have returned.
	// If it has, the pipe was not held and the rest of this test is measuring nothing.
	select {
	case <-returned:
		t.Fatalf("the apply returned although its output pipe was held open; this fixture no "+
			"longer reproduces the hang, so it proves nothing about the fix (err: %v)", applyErr)
	case <-time.After(250 * time.Millisecond):
	}

	cancel()
	select {
	case <-returned:
	case <-time.After(15 * time.Second):
		t.Fatal("the interrupted apply never returned: dcctl is still blocked on a pipe a dead " +
			"tofu's orphan is holding, which is the defect")
	}
	if !errors.Is(applyErr, errTofuAbandoned) {
		t.Fatalf("the interrupted apply did not report itself abandoned: %v", applyErr)
	}
}
