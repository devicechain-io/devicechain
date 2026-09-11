// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	dck8s "github.com/devicechain-io/dc-k8s/config"
)

// testClaimNS stands in for the operator namespace the lock really lives in. The
// name is not load-bearing anywhere in claim.go — the Lease NAME is the key — so
// a fixed string here is honest.
const testClaimNS = "dc-system"

// 🔴 THE FAKE CLIENTSET ASSIGNS NO resourceVersion AND ENFORCES NO PRECONDITION,
// and every test below is written knowing it. Create and Update leave the field
// exactly as the caller wrote it, a stale Update succeeds rather than conflicting,
// and Delete ignores metav1.Preconditions entirely.
//
// That is not a defect to work around quietly, it is the thing to say out loud: a
// test that let the fake supply the resourceVersion semantics would be measuring
// the fake. So where the semantics matter, the tests set resourceVersion by hand
// (confirmAbandoned) or force the API's answer with a reactor (breakVerb), and
// where a production guard is the ONLY thing standing between a Delete and the
// object (Release), the fake's permissiveness is what makes that test meaningful:
// nothing but the code under test can stop the deletion.
//
// 🔑 WHAT IS THEREFORE NOT COVERED, SAID PLAINLY RATHER THAN PAPERED OVER: the
// compare-and-swap preconditions are not exercised AS preconditions. Release's
// UID/resourceVersion Preconditions and Reclaim's resourceVersion-carrying Update
// are refused by a real API server and accepted by this one, so the tests below
// assert what dcctl does WITH the API server's answer (a forced Conflict) and what
// its own guards do without one. A reactor that manufactured the precondition
// check would be testing the reactor.
//
// heldLease builds a Lease as a holding run leaves it in the cluster.
func heldLease(ns, holder, instance string, renewed time.Time, rv string) *coordinationv1.Lease {
	secs := int32(claimLeaseDuration / time.Second)
	at := metav1.NewMicroTime(renewed)
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:            claimLeaseName,
			Namespace:       ns,
			ResourceVersion: rv,
			Annotations:     map[string]string{annotationClaimInstance: instance},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &secs,
			AcquireTime:          &at,
			RenewTime:            &at,
		},
	}
}

// getLease reads the Lease the way a second operator would.
func getLease(t *testing.T, cs *fake.Clientset, ns string) (*coordinationv1.Lease, error) {
	t.Helper()
	return cs.CoordinationV1().Leases(ns).Get(context.Background(), claimLeaseName, metav1.GetOptions{})
}

// stealLease rewrites the holder identity in the cluster, which is what a reclaim
// from another machine looks like from inside this process.
func stealLease(t *testing.T, cs *fake.Clientset, ns, newHolder string) {
	t.Helper()
	cur, err := getLease(t, cs, ns)
	if err != nil {
		t.Fatalf("reading the lock to steal it: %v", err)
	}
	cur.Spec.HolderIdentity = &newHolder
	if _, err := cs.CoordinationV1().Leases(ns).Update(context.Background(), cur, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("stealing the lock: %v", err)
	}
}

// breakVerb arms a reactor that makes one verb on leases fail the way a partition
// or an overloaded API server does — a server-side error, NOT a NotFound, because
// the two lead the code under test somewhere different on purpose.
//
// It returns the switch rather than failing from the start, so a test can take a
// real claim first and break the API afterwards.
func breakVerb(cs *fake.Clientset, verb string) *atomic.Bool {
	broken := &atomic.Bool{}
	cs.PrependReactor(verb, "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
		if !broken.Load() {
			return false, nil, nil
		}
		return true, nil, apierrors.NewInternalError(errors.New("the API server is not answering"))
	})
	return broken
}

// testClaim builds a Claim that is NOT running its renewal goroutine, so a test
// can drive renewOnce itself and know that nothing else moved the Lease. Claims
// that must be released (and therefore need the stop/done channels) come from
// AcquireClaim instead.
func testClaim(cs *fake.Clientset, ns, holder string) *Claim {
	return &Claim{ns: ns, holder: holder, client: cs, lastHeld: time.Now()}
}

// releaseOnCleanup stops the renewal goroutine a real claim starts. t.Context() is
// cancelled before cleanups run, so this deliberately uses its own context —
// otherwise every cleanup would exercise the "context already dead" path instead
// of the release it is there to perform.
func releaseOnCleanup(t *testing.T, c *Claim) {
	t.Helper()
	t.Cleanup(func() { c.Release(context.Background()) })
}

// 🔴 THE SECOND RUN IS REFUSED, NOT QUEUED, and refusing is the whole feature. A
// lock that waits would turn two operators typing the same command into two
// appliers separated only by time, which is the thing this mechanism exists to
// prevent — the second one would start the moment the first finished, against a
// cluster whose state it decided on minutes earlier.
func TestASecondRunIsRefusedRatherThanQueued(t *testing.T) {
	cs := fake.NewClientset()

	first, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod")
	if err != nil {
		t.Fatalf("the first run could not take a free lock: %v", err)
	}
	releaseOnCleanup(t, first)

	second, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod")
	if err == nil {
		t.Fatalf("a second run took the lock as well; both would now be applying to one cluster")
	}
	if second != nil {
		t.Errorf("the refused run was handed a claim anyway: %+v", second)
	}

	// 🔴 THE TYPE, NOT THE TEXT. The command layer branches on this error to decide
	// what to print and what exit code to use; a string match here would pass just
	// as happily if the refusal degraded into a plain errors.New, which no caller
	// can act on.
	var held *ClaimHeldError
	if !errors.As(err, &held) {
		t.Fatalf("the refusal is not a *ClaimHeldError, so no caller can act on it: %T: %v", err, err)
	}
	if held.Holder != first.Holder() {
		t.Errorf("the refusal names holder %q; the lock is held by %q. An operator told the wrong "+
			"holder goes and checks the wrong machine", held.Holder, first.Holder())
	}
	if held.Instance != "prod" {
		t.Errorf("the refusal names instance %q, not the one recorded on the Lease", held.Instance)
	}
	if held.Renewed.IsZero() {
		t.Error("the refusal carries no renew time, so it cannot say how old the claim is")
	}

	// And the failed attempt must not have touched the lock. A refusal that
	// overwrote the holder on its way out would be the takeover it just declined.
	cur, err := getLease(t, cs, testClaimNS)
	if err != nil {
		t.Fatalf("the lock disappeared during the refused attempt: %v", err)
	}
	if ptrString(cur.Spec.HolderIdentity) != first.Holder() {
		t.Errorf("the lock now names %q; the refused run rewrote it",
			ptrString(cur.Spec.HolderIdentity))
	}
}

// A cluster somebody else administers may simply not let this account create the
// Lease, and that is not the same failure as "somebody is already running". The
// API server's own sentence names the verb, the resource, the namespace and the
// user; a wrapper that reads like an outage sends the operator to look at the
// cluster instead of at their own RBAC.
func TestAForbiddenCreateSaysWhatAccessIsMissing(t *testing.T) {
	cs := fake.NewClientset()
	cs.PrependReactor("create", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"},
			claimLeaseName, errors.New("this account cannot create leases in this namespace"))
	})

	c, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod")
	if err == nil {
		t.Fatalf("a refused create produced a claim: %+v", c)
	}
	// The cause has to survive the wrapping, or a caller can only string-match it.
	if !apierrors.IsForbidden(err) {
		t.Errorf("the API server's refusal was flattened into something nothing can classify: %v", err)
	}
	// 🔴 IT MUST NOT BE MISTAKEN FOR THE LOCK BEING HELD. The command layer branches
	// on *ClaimHeldError to tell an operator to wait for a colleague; waiting will
	// never resolve a missing role binding.
	var held *ClaimHeldError
	if errors.As(err, &held) {
		t.Error("a permissions failure was reported as another operator holding the lock")
	}
	if !strings.Contains(err.Error(), testClaimNS) {
		t.Errorf("the refusal does not say which namespace the access is needed in: %v", err)
	}
	if !strings.Contains(err.Error(), "leases.coordination.k8s.io") {
		t.Errorf("the refusal does not name the resource the account cannot touch: %v", err)
	}
}

// 🔴 THE REFUSAL REPORTS EVIDENCE AND NEVER A VERDICT. renewTime was written by
// another machine's clock and is read by this one, so "it looks stale" is exactly
// as much as this code is entitled to say. The failure mode being pinned is a
// message that reads as a diagnosis — an operator told the holder is dead has been
// given permission to force past a live run, and the platform will have told them
// so on the strength of a clock difference.
func TestTheRefusalReportsStalenessWithoutAssertingTheHolderIsDead(t *testing.T) {
	const other = "operator@another-laptop/4242/0a1b2c3d"
	ancient := time.Now().Add(-2 * time.Hour)
	cs := fake.NewClientset(heldLease(testClaimNS, other, "prod", ancient, "100"))

	_, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod")
	var held *ClaimHeldError
	if !errors.As(err, &held) {
		t.Fatalf("a claimed cluster did not refuse with a *ClaimHeldError: %T: %v", err, err)
	}
	if !held.LooksStale {
		t.Fatal("a Lease two hours past its duration did not read as stale, so the two message " +
			"arms below are not the ones this test thinks it is exercising")
	}

	stale := held.Error()
	ordinary := (&ClaimHeldError{
		Instance: held.Instance, Holder: held.Holder, Renewed: time.Now(),
	}).Error()

	// The two arms must actually differ. One message for both cases means either
	// the fresh refusal is sending people to the steal path, or the stale one is
	// withholding the way out.
	if stale == ordinary {
		t.Fatalf("the stale and ordinary refusals are the same sentence:\n%s", stale)
	}
	if !strings.Contains(stale, "reclaim") {
		t.Errorf("the stale refusal does not name the command that checks properly before it "+
			"steals, so the only route left is a manual delete:\n%s", stale)
	}
	if strings.Contains(ordinary, "reclaim") {
		t.Errorf("the ordinary refusal points at the steal path for a lock that was renewed "+
			"seconds ago:\n%s", ordinary)
	}
	// The hedge is load-bearing prose: the message has to name the clock, because
	// the clock is the reason the evidence is not a verdict.
	if !strings.Contains(stale, "clock") {
		t.Errorf("the stale refusal does not say the judgement depends on a clock:\n%s", stale)
	}
	for _, verdict := range []string{"is dead", "has died", "has crashed", "is no longer running", "has exited"} {
		if strings.Contains(stale, verdict) {
			t.Errorf("the stale refusal asserts %q, which this machine cannot know:\n%s", verdict, stale)
		}
	}

	// And the cheap read must still be capable of saying "not stale", or the arm
	// above is reached by everything and the distinction is decorative.
	fresh := heldLease(testClaimNS, other, "prod", time.Now(), "100")
	if looksStale(fresh, time.Now()) {
		t.Error("a Lease renewed just now reads as stale, so every refusal takes the stale arm")
	}
}

// 🔴 THE MOST IMPORTANT TEST IN THIS FILE. confirmAbandoned is the only thing in
// the module that decides a holder is gone, and what it is allowed to decide on is
// an unchanged resourceVersion over a window THIS machine timed. A resourceVersion
// is assigned by the API server and bumped by any write, so it is the one signal
// in play that both parties agree on.
//
// The mutation this guards against is small and entirely plausible: comparing
// renewTime against time.Now(). It reads as the obvious implementation, it passes
// any test built from clocks that agree, and it declares a LIVE holder dead as
// soon as the two machines disagree by a minute — handing a second applier a
// cluster somebody is mid-apply on.
//
// The sleep is injected so the window costs nothing to test. That seam is what
// makes the rule testable at all; see the note on Reclaim, which does not have it.
func TestConfirmAbandonedDecidesOnAnUnchangedResourceVersionNotATimestamp(t *testing.T) {
	const other = "operator@another-laptop/4242/0a1b2c3d"

	// window builds a sleep that runs during, and records what it was asked to wait.
	window := func(during func()) (func(context.Context, time.Duration) error, *int, *time.Duration) {
		calls := 0
		waited := time.Duration(0)
		return func(_ context.Context, d time.Duration) error {
			calls++
			waited = d
			if during != nil {
				during()
			}
			return nil
		}, &calls, &waited
	}

	t.Run("an unchanged resourceVersion means abandoned", func(t *testing.T) {
		cs := fake.NewClientset(heldLease(testClaimNS, other, "prod", time.Now().Add(-3*time.Hour), "100"))
		sleep, calls, waited := window(nil)

		abandoned, l, err := confirmAbandoned(t.Context(), cs, testClaimNS, sleep)
		if err != nil {
			t.Fatalf("confirming an untouched lock failed: %v", err)
		}
		if !abandoned {
			t.Error("a Lease nothing wrote to for a full window was not judged abandoned, so an " +
				"operator whose colleague's laptop really is gone has no way back into their own cluster")
		}
		if l == nil {
			t.Fatal("the confirming read returned no Lease, so the caller cannot say who it is taking it from")
		}
		// The window has to be the lease duration measured HERE. A shorter one would
		// call a holder gone between two of its own renewals.
		if *calls != 1 {
			t.Errorf("the window was timed %d times, want exactly once", *calls)
		}
		if *waited != claimLeaseDuration {
			t.Errorf("the window was %s, not the lease duration %s", *waited, claimLeaseDuration)
		}
	})

	// 🔴 THE SKEW-IMMUNITY CASE. renewTime is three hours old — a timestamp
	// comparison answers "abandoned" with total confidence — but a write landed
	// during the window, so the holder is demonstrably alive. If anyone
	// reintroduces a clock comparison, this is the test that dies.
	t.Run("a resourceVersion that moved means alive, however old renewTime looks", func(t *testing.T) {
		ancient := time.Now().Add(-3 * time.Hour)
		cs := fake.NewClientset(heldLease(testClaimNS, other, "prod", ancient, "100"))

		// The holder renews from a machine whose clock is three hours behind ours:
		// the API server bumps the resourceVersion, and renewTime STAYS ancient by
		// our reckoning. That is what skew looks like from in here.
		sleep, _, _ := window(func() {
			cur, err := getLease(t, cs, testClaimNS)
			if err != nil {
				t.Fatal(err)
			}
			cur.ResourceVersion = "101"
			if _, err := cs.CoordinationV1().Leases(testClaimNS).Update(
				context.Background(), cur, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		})

		abandoned, l, err := confirmAbandoned(t.Context(), cs, testClaimNS, sleep)
		if err != nil {
			t.Fatalf("confirming failed: %v", err)
		}
		if abandoned {
			t.Fatal("a lock that was WRITTEN TO during the window was judged abandoned: the " +
				"decision is being made on renewTime, and a live holder is about to be stolen from")
		}
		// And the proof that this case is the one it claims to be: the cheap,
		// skew-dependent read disagrees with the verdict. If looksStale were false
		// here, a timestamp implementation would pass this test by accident.
		if l == nil || !looksStale(l, time.Now()) {
			t.Fatal("this fixture's renewTime does not read as stale, so a timestamp comparison " +
				"would get the right answer here and the test proves nothing")
		}
	})

	// The other direction of the same skew: a machine whose clock is AHEAD leaves a
	// renewTime in the future. A timestamp rule reads that as "renewed recently" and
	// refuses forever, stranding a cluster whose holder is genuinely gone.
	t.Run("a renewTime in the future is still judged by the resourceVersion", func(t *testing.T) {
		future := time.Now().Add(2 * time.Hour)
		cs := fake.NewClientset(heldLease(testClaimNS, other, "prod", future, "100"))
		sleep, _, _ := window(nil)

		abandoned, l, err := confirmAbandoned(t.Context(), cs, testClaimNS, sleep)
		if err != nil {
			t.Fatalf("confirming failed: %v", err)
		}
		if !abandoned {
			t.Error("a lock nothing touched was not judged abandoned because its renewTime is in " +
				"the future; a holder with a fast clock would hold this cluster forever")
		}
		if l == nil || looksStale(l, time.Now()) {
			t.Fatal("this fixture reads as stale, so it is not the future-clock case it claims to be")
		}
	})

	t.Run("a lock released during the window is free, not stealable", func(t *testing.T) {
		cs := fake.NewClientset(heldLease(testClaimNS, other, "prod", time.Now().Add(-3*time.Hour), "100"))
		sleep, _, _ := window(func() {
			if err := cs.CoordinationV1().Leases(testClaimNS).Delete(
				context.Background(), claimLeaseName, metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
		})

		abandoned, l, err := confirmAbandoned(t.Context(), cs, testClaimNS, sleep)
		if err != nil {
			t.Fatalf("confirming failed: %v", err)
		}
		if abandoned {
			t.Error("a Lease that vanished mid-window was reported as abandoned, which would have " +
				"the caller steal a lock that is not there")
		}
		// 🔴 nil IS THE ANSWER, not an oversight: Reclaim distinguishes "nothing to
		// reclaim" from "held by someone" purely by this being nil, and returning the
		// pre-delete Lease here would send an operator to the refusal for a holder
		// that has already exited cleanly.
		if l != nil {
			t.Errorf("a released lock came back as still held by %q",
				ptrString(l.Spec.HolderIdentity))
		}
	})

	t.Run("an unclaimed cluster is answered without waiting", func(t *testing.T) {
		cs := fake.NewClientset()
		sleep, calls, _ := window(nil)

		abandoned, l, err := confirmAbandoned(t.Context(), cs, testClaimNS, sleep)
		if err != nil || abandoned || l != nil {
			t.Fatalf("a free lock answered abandoned=%v lease=%v err=%v", abandoned, l != nil, err)
		}
		// A minute of waiting to discover there is no lock at all is a minute an
		// operator spends watching a command that had its answer on the first read.
		if *calls != 0 {
			t.Errorf("the window was timed %d times for a lock that does not exist", *calls)
		}
	})

	t.Run("a caller who gives up is not answered abandoned", func(t *testing.T) {
		cs := fake.NewClientset(heldLease(testClaimNS, other, "prod", time.Now().Add(-3*time.Hour), "100"))
		gaveUp := errors.New("the operator pressed ctrl-c")
		sleep := func(context.Context, time.Duration) error { return gaveUp }

		abandoned, _, err := confirmAbandoned(t.Context(), cs, testClaimNS, sleep)
		if abandoned {
			t.Error("an interrupted window was reported as a completed one")
		}
		if !errors.Is(err, gaveUp) {
			t.Errorf("the interruption was swallowed: %v", err)
		}
	})
}

// 🔴 THE RENEWAL READ IS THE FENCE. Renewing is the part that looks like the job;
// checking who the Lease belongs to before renewing is the part that matters. A
// reclaim writes a new holder identity, and this comparison is the only way the
// original process finds out — without it, the reclaimed run carries on applying
// and the reclaim has produced exactly the two-applier situation it was performed
// to end.
func TestARenewalThatFindsADifferentHolderIsLost(t *testing.T) {
	const ours = "operator@this-laptop/1111/aabbccdd"
	const theirs = "colleague@another-laptop/2222/eeff0011"

	t.Run("a stolen lock is reported lost", func(t *testing.T) {
		cs := fake.NewClientset(heldLease(testClaimNS, theirs, "prod", time.Now(), "100"))
		c := testClaim(cs, testClaimNS, ours)

		err := c.renewOnce()
		if err == nil {
			t.Fatal("a run whose lock was reclaimed renewed happily and would keep applying")
		}
		// errors.Is, not a string match: Pipeline.Run wraps this, the command layer
		// unwraps it, and a sentinel that stopped being a sentinel would still print
		// a plausible message while nothing could branch on it.
		if !errors.Is(err, ErrClaimLost) {
			t.Fatalf("the loss is not ErrClaimLost, so nothing upstream can recognise it: %v", err)
		}
		if !strings.Contains(err.Error(), theirs) {
			t.Errorf("the loss does not name who holds the lock now: %v", err)
		}
	})

	t.Run("a deleted lock is lost and is never recreated", func(t *testing.T) {
		cs := fake.NewClientset()
		c := testClaim(cs, testClaimNS, ours)

		err := c.renewOnce()
		if !errors.Is(err, ErrClaimLost) {
			t.Fatalf("a deleted lock did not fence the run: %v", err)
		}
		// 🔴 A holder that recreates its own Lease on NotFound becomes a SECOND
		// holder the instant somebody else has already taken it.
		if _, err := getLease(t, cs, testClaimNS); !apierrors.IsNotFound(err) {
			t.Error("the renewal recreated the lock it had just been told was gone")
		}
	})

	// The counterweight, and without it the two arms above are satisfied by a
	// renewOnce that always reports loss: an ordinary renewal must succeed, must
	// push renewTime forward, and must leave the holder alone.
	t.Run("an ordinary renewal carries on", func(t *testing.T) {
		before := time.Now().Add(-30 * time.Second)
		cs := fake.NewClientset(heldLease(testClaimNS, ours, "prod", before, "100"))
		c := testClaim(cs, testClaimNS, ours)
		c.lastHeld = before

		if err := c.renewOnce(); err != nil {
			t.Fatalf("a healthy run fenced itself: %v", err)
		}
		cur, err := getLease(t, cs, testClaimNS)
		if err != nil {
			t.Fatal(err)
		}
		if cur.Spec.RenewTime == nil || !cur.Spec.RenewTime.After(before) {
			t.Error("the renewal did not move renewTime forward, so the lock expires under a run " +
				"that is still working")
		}
		if ptrString(cur.Spec.HolderIdentity) != ours {
			t.Errorf("the renewal rewrote the holder to %q", ptrString(cur.Spec.HolderIdentity))
		}
		if !c.lastHeld.After(before) {
			t.Error("a successful renewal did not refresh lastHeld, so this run will fence itself " +
				"a lease duration from now while holding the lock perfectly well")
		}
	})
}

// 🔴 THE SYMMETRY IS THE POINT. The reclaimer's test is "nothing changed for a
// lease duration on MY clock"; this is the same window on the holder's own. A
// holder partitioned from the API server that kept applying would be the one
// interleaving where both sides follow the rules and both are wrong — the
// reclaimer honestly concludes the holder is gone, and the holder honestly
// believes it still holds the lock.
//
// The other half is just as necessary: a couple of failed calls must NOT fence a
// healthy run, which is what the 6:1 renew-to-duration ratio buys.
func TestARunThatCannotRenewForAFullLeaseDurationDeclaresItselfLost(t *testing.T) {
	const ours = "operator@this-laptop/1111/aabbccdd"

	for _, tc := range []struct {
		verb string
		what string
	}{
		{"get", "the API server stops answering reads"},
		{"update", "the API server stops accepting writes"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			cs := fake.NewClientset(heldLease(testClaimNS, ours, "prod", time.Now(), "100"))
			broken := breakVerb(cs, tc.verb)
			c := testClaim(cs, testClaimNS, ours)
			broken.Store(true)

			// Transient: the run is still within its lease duration, so it holds.
			if err := c.renewOnce(); err != nil {
				t.Fatalf("one failed call fenced a run that renewed seconds ago: %v", err)
			}
			if c.Lost() {
				t.Fatal("a run that missed a single renewal declared itself lost; on a busy API " +
					"server that aborts bootstraps for nothing")
			}

			// Sustained: past a full lease duration with nothing renewed, a
			// reclaimer would already be entitled to take this lock, so the holder
			// must stop acting as one. lastHeld is unexported and this is an
			// in-package test, which is how the clock is moved without waiting a
			// real minute.
			c.mu.Lock()
			c.lastHeld = time.Now().Add(-claimLeaseDuration - time.Second)
			c.mu.Unlock()

			err := c.renewOnce()
			if !errors.Is(err, ErrClaimLost) {
				t.Fatalf("a run that has not renewed for a full lease duration is still applying: %v", err)
			}
			if !strings.Contains(err.Error(), "not renewed") {
				t.Errorf("the message does not say why the run fenced itself: %v", err)
			}

			// And the recovery direction: once the API answers again, the same claim
			// renews and carries on. A run fenced permanently by a blip would be a
			// different bug wearing this one's clothes.
			broken.Store(false)
			if err := c.renewOnce(); err != nil {
				t.Errorf("the run could not recover once the API came back: %v", err)
			}
		})
	}
}

// 🔴 A FENCED PROCESS MUST NOT DELETE THE WINNER'S LOCK. This is the sharpest edge
// in the module: the losing run is on its way out anyway, so the cost of getting it
// wrong is invisible here and paid by a THIRD run, which walks into a cluster
// somebody is mid-apply on and finds the lock free.
//
// The fake clientset ignores the UID/resourceVersion preconditions Release attaches,
// which is what makes this test worth writing: the holder comparison in Release is
// the only thing that can stop the deletion.
func TestReleaseNeverDeletesALeaseItNoLongerHolds(t *testing.T) {
	const theirs = "colleague@another-laptop/2222/eeff0011"

	t.Run("a reclaimed lock is left where it is", func(t *testing.T) {
		cs := fake.NewClientset()
		c, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod")
		if err != nil {
			t.Fatal(err)
		}
		stealLease(t, cs, testClaimNS, theirs)

		c.Release(context.Background())

		cur, err := getLease(t, cs, testClaimNS)
		if err != nil {
			t.Fatalf("the reclaimed lock was deleted by the run that lost it: %v", err)
		}
		if ptrString(cur.Spec.HolderIdentity) != theirs {
			t.Errorf("the lock now names %q, not the operator who reclaimed it",
				ptrString(cur.Spec.HolderIdentity))
		}
	})

	// The counterweight, and it is not decorative: a CLI that has exited holds
	// nothing, so leaving the Lease behind sends the next run — usually the same
	// person retrying — down the reclaim path, a full lease duration of waiting and
	// a typed confirmation to answer a question this process already knew.
	t.Run("a lock this run still holds is deleted", func(t *testing.T) {
		cs := fake.NewClientset()
		c, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod")
		if err != nil {
			t.Fatal(err)
		}

		c.Release(context.Background())

		if _, err := getLease(t, cs, testClaimNS); !apierrors.IsNotFound(err) {
			t.Errorf("the lock outlived the run that held it (err=%v)", err)
		}
	})

	// Release is called from a defer in the command layer, where a second call is a
	// refactor away; it must not panic on the closed channel.
	t.Run("releasing twice is safe", func(t *testing.T) {
		cs := fake.NewClientset()
		c, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod")
		if err != nil {
			t.Fatal(err)
		}
		c.Release(context.Background())
		c.Release(context.Background())
	})
}

// 🔴 THE FENCE ASKS THE API SERVER. The renewal goroutine sets its flag on a ten
// second ticker, and a step can finish and the next begin well inside that window —
// so a cached answer can be a full interval out of date at exactly the moment it is
// consulted, which is the moment the next step is about to write to the cluster.
//
// Every subtest here finishes in milliseconds, far short of claimRenewInterval, so
// the renewal goroutine provably has not ticked: anything CheckHeld knows, it
// learned by asking.
func TestCheckHeldAsksTheApiServerRatherThanReadingAFlag(t *testing.T) {
	const theirs = "colleague@another-laptop/2222/eeff0011"

	t.Run("a live holder change is detected without waiting for a renewal", func(t *testing.T) {
		cs := fake.NewClientset()
		c, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod")
		if err != nil {
			t.Fatal(err)
		}
		releaseOnCleanup(t, c)
		if c.Lost() {
			t.Fatal("a freshly acquired claim already reads as lost")
		}

		stealLease(t, cs, testClaimNS, theirs)

		err = c.CheckHeld(t.Context())
		if !errors.Is(err, ErrClaimLost) {
			t.Fatalf("the fence did not see a reclaim that had already landed: %v", err)
		}
		if !strings.Contains(err.Error(), theirs) {
			t.Errorf("the fence does not name the new holder: %v", err)
		}
		// Once seen, the loss sticks: a fenced run must stay silent even if the
		// next read happens to succeed.
		if !c.Lost() {
			t.Error("the loss was not recorded, so a later step could decide it still holds the lock")
		}
	})

	t.Run("a deleted lock is a loss", func(t *testing.T) {
		cs := fake.NewClientset()
		c, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod")
		if err != nil {
			t.Fatal(err)
		}
		releaseOnCleanup(t, c)
		if err := cs.CoordinationV1().Leases(testClaimNS).Delete(
			t.Context(), claimLeaseName, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := c.CheckHeld(t.Context()); !errors.Is(err, ErrClaimLost) {
			t.Fatalf("a run whose lock had been deleted was allowed to continue: %v", err)
		}
	})

	// 🔴 THE OTHER DIRECTION, and it is the one that decides whether this fence is
	// usable at all. One failed GET must not abort a bootstrap: the fence falls back
	// to what the renewal loop knows, which cannot mask a genuine loss because a run
	// that has not renewed for a full lease duration has already fenced itself.
	t.Run("a transient API error does not report loss while the claim is fresh", func(t *testing.T) {
		cs := fake.NewClientset()
		c, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod")
		if err != nil {
			t.Fatal(err)
		}
		releaseOnCleanup(t, c)

		broken := breakVerb(cs, "get")
		broken.Store(true)
		if err := c.CheckHeld(t.Context()); err != nil {
			t.Fatalf("one unanswered read aborted a run that holds its lock: %v", err)
		}

		// ...but a fresh claim is what makes that safe, so the stale case must still
		// report the loss the renewal loop recorded.
		c.mu.Lock()
		c.lastHeld = time.Now().Add(-claimLeaseDuration - time.Second)
		c.mu.Unlock()
		c.setLost(fmt.Errorf("%w: this run has not renewed the cluster lock", ErrClaimLost))
		if err := c.CheckHeld(t.Context()); !errors.Is(err, ErrClaimLost) {
			t.Fatalf("a fenced run passed the boundary check because the API was unreachable: %v", err)
		}
	})
}

// reclaimWindow builds the injected wait for the tests below: it runs during,
// which is where a test says what the rest of the world did while this machine
// was timing its window.
func reclaimWindow(during func()) (func(context.Context, time.Duration) error, *int) {
	calls := 0
	return func(context.Context, time.Duration) error {
		calls++
		if during != nil {
			during()
		}
		return nil
	}, &calls
}

// There is nothing to reclaim on a cluster nobody has claimed, and the answer
// arrives on the first read — the confirming Get happens before the window, so an
// operator does not spend a minute watching a command that already knew.
func TestReclaimRefusesWhenThereIsNothingToReclaim(t *testing.T) {
	cs := fake.NewClientset()
	sleep, calls := reclaimWindow(nil)

	c, err := reclaim(t.Context(), cs, testClaimNS, sleep)
	if err == nil {
		t.Fatalf("reclaiming an unclaimed cluster produced a claim: %+v", c)
	}
	if c != nil {
		t.Errorf("a refused reclaim handed back a claim anyway: %+v", c)
	}
	if !strings.Contains(err.Error(), "not claimed") {
		t.Errorf("the refusal does not say the lock is simply free: %v", err)
	}
	if *calls != 0 {
		t.Errorf("the window was timed %d times for a lock that does not exist", *calls)
	}
	// And it must not have created one on the way past. A "reclaim" that acquires a
	// free lock would be a bootstrap taking place under a command nobody expects to
	// deploy anything.
	if _, err := getLease(t, cs, testClaimNS); !apierrors.IsNotFound(err) {
		t.Errorf("the refused reclaim left a lock behind (err=%v)", err)
	}
}

// 🔴 A LIVE HOLDER IS REFUSED, and this is the refusal the whole ceremony exists
// to produce. Something wrote to the Lease inside the window, so the holder is
// demonstrably there — and an operator who reclaimed anyway would be the second
// applier on a cluster somebody is mid-apply on.
func TestReclaimRefusesALiveHolder(t *testing.T) {
	const theirs = "colleague@another-laptop/2222/eeff0011"
	live := heldLease(testClaimNS, theirs, "prod", time.Now().Add(-3*time.Hour), "100")
	var transitions int32 = 3
	live.Spec.LeaseTransitions = &transitions
	cs := fake.NewClientset(live)

	// The holder renews mid-window from a machine whose clock is behind ours:
	// the resourceVersion moves, renewTime stays ancient by our reckoning.
	sleep, calls := reclaimWindow(func() {
		cur, err := getLease(t, cs, testClaimNS)
		if err != nil {
			t.Fatal(err)
		}
		cur.ResourceVersion = "101"
		if _, err := cs.CoordinationV1().Leases(testClaimNS).Update(
			context.Background(), cur, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	})

	c, err := reclaim(t.Context(), cs, testClaimNS, sleep)
	if err == nil {
		t.Fatalf("a live holder's lock was stolen: %+v", c)
	}
	if c != nil {
		t.Errorf("the refused reclaim handed back a claim: %+v", c)
	}
	if *calls != 1 {
		t.Errorf("the window was timed %d times, want exactly once", *calls)
	}
	// The TYPE again: `dcctl instances reclaim` prints the holder, the instance and
	// the age from this, and a degraded plain error would leave it with nothing to
	// print but "refused".
	var held *ClaimHeldError
	if !errors.As(err, &held) {
		t.Fatalf("the refusal is not a *ClaimHeldError: %T: %v", err, err)
	}
	if held.Holder != theirs {
		t.Errorf("the refusal names holder %q, not the one holding the lock", held.Holder)
	}

	// 🔴 AND NOTHING WAS WRITTEN. A refusal that had already stamped its own holder
	// or bumped the transition count would have performed the steal it just declined.
	cur, err := getLease(t, cs, testClaimNS)
	if err != nil {
		t.Fatalf("the lock disappeared during a refused reclaim: %v", err)
	}
	if ptrString(cur.Spec.HolderIdentity) != theirs {
		t.Errorf("the lock now names %q; the refused reclaim wrote its holder",
			ptrString(cur.Spec.HolderIdentity))
	}
	if cur.Spec.LeaseTransitions == nil || *cur.Spec.LeaseTransitions != transitions {
		t.Errorf("leaseTransitions moved during a refused reclaim: %v", cur.Spec.LeaseTransitions)
	}
	if cur.ResourceVersion != "101" {
		t.Errorf("the lock was written to during a refused reclaim (resourceVersion %q)",
			cur.ResourceVersion)
	}
}

// An abandoned lock IS taken, and this is the half that makes the refusals above
// tolerable: without it, an operator whose colleague's laptop really is gone has
// no way back into their own cluster short of deleting the Lease by hand.
func TestReclaimTakesAnAbandonedLock(t *testing.T) {
	const theirs = "colleague@another-laptop/2222/eeff0011"

	for _, tc := range []struct {
		name  string
		start *int32
		want  int32
	}{
		{"a lock that has never changed hands", nil, 1},
		{"a lock that has changed hands before", ptrInt32(3), 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := heldLease(testClaimNS, theirs, "prod", time.Now().Add(-3*time.Hour), "100")
			l.Spec.LeaseTransitions = tc.start
			cs := fake.NewClientset(l)
			sleep, calls := reclaimWindow(nil)

			c, err := reclaim(t.Context(), cs, testClaimNS, sleep)
			if err != nil {
				t.Fatalf("an untouched lock could not be reclaimed: %v", err)
			}
			if c == nil {
				t.Fatal("the reclaim reported success and returned no claim")
			}
			releaseOnCleanup(t, c)
			if *calls != 1 {
				t.Errorf("the window was timed %d times", *calls)
			}

			cur, err := getLease(t, cs, testClaimNS)
			if err != nil {
				t.Fatal(err)
			}
			// 🔴 A FRESH IDENTITY, not the old one re-stamped. The previous holder's
			// renewal loop discovers the loss by finding an identity that is not its
			// own, so a reclaim that reused the string would leave a reclaimed process
			// applying with total confidence.
			if got := ptrString(cur.Spec.HolderIdentity); got != c.Holder() {
				t.Errorf("the lock names %q and the new claim thinks it is %q", got, c.Holder())
			}
			if c.Holder() == theirs {
				t.Error("the reclaim kept the previous holder's identity, so that process would " +
					"never notice it had been reclaimed")
			}
			if cur.Spec.LeaseTransitions == nil || *cur.Spec.LeaseTransitions != tc.want {
				t.Errorf("leaseTransitions is %v, want %d: the count is how a reader tells a "+
					"handover from a renewal", cur.Spec.LeaseTransitions, tc.want)
			}

			// The instance annotation is carried across, and it is not decoration: it
			// is what the NEXT operator's refusal names. Asserted through that refusal
			// rather than off the object, because that is where it is consumed.
			if c.instance != "prod" {
				t.Errorf("the new claim is working on instance %q", c.instance)
			}
			_, err = AcquireClaim(t.Context(), cs, testClaimNS, "prod")
			var held *ClaimHeldError
			if !errors.As(err, &held) {
				t.Fatalf("a third run was not refused by the reclaimed lock: %v", err)
			}
			if held.Instance != "prod" {
				t.Errorf("the next refusal names instance %q; the annotation was dropped in the "+
					"handover", held.Instance)
			}
			if held.Holder != c.Holder() {
				t.Errorf("the next refusal names holder %q, not the operator who reclaimed it",
					held.Holder)
			}
		})
	}
}

// 🔴 A CONFLICT MEANS THE PREVIOUS HOLDER IS ALIVE, and this is the narrowest
// window in the module: the holder wakes and renews between the confirming read
// and the steal. The Update carries the resourceVersion from that read for exactly
// this reason — the API server refuses it, and what would otherwise be a silent
// overwrite of a live claim becomes a refusal.
//
// Nothing else in the suite would notice if the compare-and-swap were dropped:
// every other path passes with a blind write.
func TestReclaimTreatsAConflictAsThePreviousHolderBeingAlive(t *testing.T) {
	const theirs = "colleague@another-laptop/2222/eeff0011"

	t.Run("a conflict on the steal is reported as a live holder", func(t *testing.T) {
		cs := fake.NewClientset(heldLease(testClaimNS, theirs, "prod", time.Now().Add(-3*time.Hour), "100"))
		// The fake enforces no resourceVersion precondition of its own, so the
		// API server's answer is forced here. What is being asserted is what dcctl
		// DOES with a Conflict, which is the part that lives in this repo.
		cs.PrependReactor("update", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"},
				claimLeaseName, errors.New("the object has been modified"))
		})
		sleep, _ := reclaimWindow(nil)

		c, err := reclaim(t.Context(), cs, testClaimNS, sleep)
		if err == nil {
			t.Fatalf("a steal the API server refused was reported as a success: %+v", c)
		}
		if c != nil {
			t.Errorf("a refused steal handed back a claim, which would now be applying against a "+
				"lock it does not hold: %+v", c)
		}
		if !strings.Contains(err.Error(), "previous holder is alive") {
			t.Errorf("a conflict was not reported as the holder being alive, so an operator reads "+
				"an API error and tries again harder: %v", err)
		}

		// And the lock is left exactly as it was.
		cur, err := getLease(t, cs, testClaimNS)
		if err != nil {
			t.Fatal(err)
		}
		if ptrString(cur.Spec.HolderIdentity) != theirs {
			t.Errorf("the lock now names %q after a refused steal", ptrString(cur.Spec.HolderIdentity))
		}
	})

	// 🔴 THE COUNTERWEIGHT, and it is what stops the branch above from swallowing
	// every failure. An unreachable API server is not evidence that anybody is
	// alive; saying so would send an operator to wait for a process that is gone.
	t.Run("another failure is not evidence of a live holder", func(t *testing.T) {
		cs := fake.NewClientset(heldLease(testClaimNS, theirs, "prod", time.Now().Add(-3*time.Hour), "100"))
		broken := breakVerb(cs, "update")
		broken.Store(true)
		sleep, _ := reclaimWindow(nil)

		c, err := reclaim(t.Context(), cs, testClaimNS, sleep)
		if err == nil {
			t.Fatalf("a failed steal was reported as a success: %+v", c)
		}
		if strings.Contains(err.Error(), "previous holder is alive") {
			t.Errorf("an unreachable API server was reported as a live holder: %v", err)
		}
		if !strings.Contains(err.Error(), "reclaiming the cluster lock") {
			t.Errorf("the failure does not say what was being attempted: %v", err)
		}
	})
}

// The exported entry point is what the command layer calls, and it is the only
// thing that pairs Reclaim with the real wait. A reclaim that cannot finish its
// window leaves the holder exactly where it was.
func TestAnInterruptedReclaimLeavesTheHolderAlone(t *testing.T) {
	const theirs = "colleague@another-laptop/2222/eeff0011"
	cs := fake.NewClientset(heldLease(testClaimNS, theirs, "prod", time.Now().Add(-3*time.Hour), "100"))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if c, err := Reclaim(ctx, cs, testClaimNS); err == nil {
		t.Fatalf("a reclaim that never completed its window took the lock anyway: %+v", c)
	}
	cur, err := getLease(t, cs, testClaimNS)
	if err != nil {
		t.Fatalf("the lock was removed by an interrupted reclaim: %v", err)
	}
	if ptrString(cur.Spec.HolderIdentity) != theirs {
		t.Errorf("the lock now names %q; an unfinished reclaim wrote its holder",
			ptrString(cur.Spec.HolderIdentity))
	}
}

func ptrInt32(v int32) *int32 { return &v }

// 🔴 THE FENCE STOPS THE RUN BEFORE THE NEXT STEP, NOT AFTER IT. A check placed
// after a step is not a fence, it is a report: the write the fence exists to
// prevent has already happened by the time it fires.
func TestThePipelineFenceStopsBeforeTheNextStep(t *testing.T) {
	const theirs = "colleague@another-laptop/2222/eeff0011"

	// recordingPipeline returns three steps that do nothing but say they ran, and
	// the slice they append to. during runs inside the first step.
	recordingPipeline := func(during func()) (Pipeline, *[]string) {
		var ran []string
		step := func(name string, extra func()) Step {
			return Step{Name: name, Run: func(context.Context, *State) error {
				ran = append(ran, name)
				if extra != nil {
					extra()
				}
				return nil
			}}
		}
		return Pipeline{Steps: []Step{
			step("first", during), step("second", nil), step("third", nil),
		}}, &ran
	}

	t.Run("a claim already lost stops the pipeline before its first step", func(t *testing.T) {
		cs := fake.NewClientset()
		c, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod")
		if err != nil {
			t.Fatal(err)
		}
		releaseOnCleanup(t, c)
		stealLease(t, cs, testClaimNS, theirs)

		p, ran := recordingPipeline(nil)
		err = p.Run(t.Context(), &State{Claim: c})
		if err == nil {
			t.Fatal("a fenced run executed the whole pipeline")
		}
		if !errors.Is(err, ErrClaimLost) {
			t.Fatalf("the pipeline stopped for a reason nothing upstream can recognise: %v", err)
		}
		if len(*ran) != 0 {
			t.Errorf("steps %v ran after the claim was lost; the check is happening after the "+
				"work rather than before it", *ran)
		}
		if !strings.Contains(err.Error(), `stopping before "first"`) {
			t.Errorf("the error does not name the step that did not run: %v", err)
		}
	})

	t.Run("a claim lost mid-run stops at the next boundary", func(t *testing.T) {
		cs := fake.NewClientset()
		c, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod")
		if err != nil {
			t.Fatal(err)
		}
		releaseOnCleanup(t, c)

		// The reclaim lands while the first step is running, which is the real
		// shape of this: nothing can stop a step already in flight, so the bound
		// on the exposure is the remainder of the current step.
		p, ran := recordingPipeline(func() { stealLease(t, cs, testClaimNS, theirs) })
		err = p.Run(t.Context(), &State{Claim: c})
		if err == nil {
			t.Fatal("the run continued through a reclaim that landed mid-pipeline")
		}
		if strings.Join(*ran, ",") != "first" {
			t.Errorf("steps %v ran; only the one already in flight should have", *ran)
		}
		// 🔴 THE STEP NAMED IS THE ONE THAT DID NOT RUN. A fence moved after the
		// step would report "first" here — the step that already finished — and the
		// counts above would not notice.
		if !strings.Contains(err.Error(), `stopping before "second"`) {
			t.Errorf("the error names the wrong boundary: %v", err)
		}
	})

	// 🔴 THE COUNTERWEIGHT, without which the two arms above are satisfied by a
	// pipeline that refuses to run anything at all.
	t.Run("a healthy claim runs every step", func(t *testing.T) {
		cs := fake.NewClientset()
		c, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod")
		if err != nil {
			t.Fatal(err)
		}
		releaseOnCleanup(t, c)

		p, ran := recordingPipeline(nil)
		if err := p.Run(t.Context(), &State{Claim: c}); err != nil {
			t.Fatalf("a run holding its lock was fenced: %v", err)
		}
		if strings.Join(*ran, ",") != "first,second,third" {
			t.Errorf("the pipeline ran %v", *ran)
		}
	})

	// And a pipeline with no claim at all — every test in this package, and every
	// dry run — must still execute.
	t.Run("no claim means no fence", func(t *testing.T) {
		p, ran := recordingPipeline(nil)
		if err := p.Run(t.Context(), &State{}); err != nil {
			t.Fatalf("a pipeline with no claim was refused: %v", err)
		}
		if len(*ran) != 3 {
			t.Errorf("the pipeline ran %v", *ran)
		}
	})
}

// The lock lives in the operator's namespace, and that namespace is a kustomize
// setting rather than a constant. A copy of it here would be a second place to
// remember: a rename would leave dcctl taking its lock somewhere nothing else
// looks, which is a lock that protects nothing while appearing to work.
func TestOperatorNamespaceComesFromTheRenderedOverlay(t *testing.T) {
	t.Run("it is read out of the manifests", func(t *testing.T) {
		// The Namespace is deliberately NOT the first document: the function has to
		// search the overlay, not read its head.
		manifests := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: dc-operator-controller-manager
  namespace: dc-operator-system
---
apiVersion: v1
kind: Namespace
metadata:
  name: dc-operator-system
`)
		got, err := operatorNamespace(manifests)
		if err != nil {
			t.Fatalf("the overlay's namespace could not be read: %v", err)
		}
		if got != "dc-operator-system" {
			t.Errorf("read namespace %q", got)
		}
	})

	t.Run("an overlay with no Namespace fails loudly", func(t *testing.T) {
		manifests := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: dc-operator-controller-manager
  namespace: dc-operator-system
`)
		got, err := operatorNamespace(manifests)
		if err == nil {
			t.Fatalf("a manifest set declaring no Namespace answered %q; the lock would be taken "+
				"in a namespace nothing else uses", got)
		}
		if !strings.Contains(err.Error(), "lock") {
			t.Errorf("the refusal does not say what it costs: %v", err)
		}
	})

	// And against the overlay dcctl actually renders, because the refusal above is
	// only worth having if the real thing passes.
	t.Run("the real operator overlay declares one", func(t *testing.T) {
		manifests, err := dck8s.RenderOperator("")
		if err != nil {
			t.Fatalf("rendering the operator overlay: %v", err)
		}
		ns, err := operatorNamespace(manifests)
		if err != nil {
			t.Fatalf("the shipped operator overlay declares no namespace to take the lock in: %v", err)
		}
		if ns == "" {
			t.Error("the shipped operator overlay declares an empty namespace")
		}
	})
}
