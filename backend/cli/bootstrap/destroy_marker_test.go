// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/kube"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/release"
)

func markerPath(t *testing.T, home, instance string) string {
	t.Helper()
	return filepath.Join(home, ".devicechain", "instances", instance, destroyMarkerFile)
}

func markerExists(t *testing.T, home, instance string) bool {
	t.Helper()
	_, err := os.Stat(markerPath(t, home, instance))
	return err == nil
}

// unstattableMarker puts an instance directory into the "could not tell" state: the
// marker inside it can be neither found nor ruled out. Three callers reach for it —
// DestroyInProgress, RefuseUnfinishedDestroy and ListInstances all have to keep that
// answer distinct from "absent" — and the two traps below belong to the fixture rather
// than to any one of them, which is why they are written once.
//
// 🔴 0o000, NOT 0o100. With execute-only the directory is still TRAVERSABLE, so a stat of
// a known name inside it succeeds and reports ErrNotExist — the reach control below skips
// on that, which is a case that asserts nothing wearing a pass.
//
// 🔴 AND THE REACH CONTROL ITSELF. Running as root, or on a filesystem that ignores
// modes, the stat SUCCEEDS and the caller's case would assert nothing at all.
func unstattableMarker(t *testing.T, home, instance string) {
	t.Helper()
	dir := filepath.Join(home, ".devicechain", "instances", instance)
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, stateDirMode) })
	if _, err := os.Stat(filepath.Join(dir, destroyMarkerFile)); errors.Is(err, os.ErrNotExist) {
		t.Skip("the tightened directory is still statable here, so this case cannot detect the fold")
	}
}

// 🔴 THE ESCROW-COLLISION CONTROL, and for this file it is worse than it is for the
// record. `destroy` spares every file whose name looks like escrow, so a marker matching
// that pattern would survive the destroy that wrote it — and every later bootstrap and
// upgrade of that name would then be refused over a teardown that HAD finished, with the
// refusal naming a `dcctl destroy` that cannot clear it. That is the closed dead end this
// whole change exists to open, rebuilt by a filename.
func TestTheDestroyMarkerIsNotSparedAsEscrow(t *testing.T) {
	if looksLikeEscrow(destroyMarkerFile) {
		t.Fatalf("%q matches looksLikeEscrow, so destroy would spare it and every later "+
			"bootstrap of this name would be refused over a teardown that finished", destroyMarkerFile)
	}
	// And prove the guard it must not match is live, rather than a function that returns
	// false for everything.
	if !looksLikeEscrow("devicechain-rootkey.escrow") {
		t.Fatal("looksLikeEscrow no longer recognises a real escrow name — this control proves nothing")
	}
}

// 🔴 THREE OUTCOMES, NOT TWO. "Could not tell" is the one that must never collapse into
// "absent": every caller of this treats absent as permission to proceed.
func TestDestroyInProgressSeparatesPresentAbsentAndCouldNotTell(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		fakeHome(t)
		marked, err := DestroyInProgress("never-destroyed")
		if err != nil || marked {
			t.Fatalf("got (%v, %v), want (false, nil) for an instance nobody has torn down", marked, err)
		}
	})

	t.Run("present", func(t *testing.T) {
		fakeHome(t)
		if err := writeDestroyMarker("acme"); err != nil {
			t.Fatal(err)
		}
		marked, err := DestroyInProgress("acme")
		if err != nil || !marked {
			t.Fatalf("got (%v, %v), want (true, nil)", marked, err)
		}
	})

	// A marker an operator half-edited is still a teardown that started. Presence is a
	// stat, so the contents cannot turn it back into an absence.
	t.Run("present but unparseable", func(t *testing.T) {
		home := fakeHome(t)
		dir := filepath.Join(home, ".devicechain", "instances", "acme")
		if err := os.MkdirAll(dir, stateDirMode); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, destroyMarkerFile), []byte("{ this is not json"), stateFileMode); err != nil {
			t.Fatal(err)
		}
		marked, err := DestroyInProgress("acme")
		if err != nil || !marked {
			t.Fatalf("got (%v, %v) for a corrupt marker, want (true, nil) — a teardown that "+
				"started is one whether or not its marker parses", marked, err)
		}
	})

	t.Run("could not tell", func(t *testing.T) {
		unstattableMarker(t, fakeHome(t), "acme")

		marked, err := DestroyInProgress("acme")
		if err == nil {
			t.Fatal("a marker that could not be statted was reported as a definite answer, " +
				"which is the failure-reads-as-healthy fold this exists to stop")
		}
		if marked {
			t.Fatal("could-not-tell was reported as present")
		}
	})
}

// The refusal itself: what `dcctl bootstrap` and `dcctl upgrade` do with the marker.
func TestRefuseUnfinishedDestroyRefusesAMarkedInstanceAndFailsClosed(t *testing.T) {
	t.Run("an instance with no marker is not refused", func(t *testing.T) {
		fakeHome(t)
		if err := RefuseUnfinishedDestroy("acme"); err != nil {
			t.Fatalf("an instance nobody is tearing down was refused: %v", err)
		}
	})

	t.Run("a marked instance is refused, typed, and told how to finish", func(t *testing.T) {
		home := fakeHome(t)
		if err := writeDestroyMarker("acme"); err != nil {
			t.Fatal(err)
		}
		err := RefuseUnfinishedDestroy("acme")
		var unfinished *ErrDestroyUnfinished
		if !errors.As(err, &unfinished) {
			t.Fatalf("got %v, want a typed *ErrDestroyUnfinished — callers match on the type", err)
		}
		if !strings.Contains(err.Error(), "dcctl destroy acme") {
			t.Errorf("the refusal does not name the command that finishes the teardown: %v", err)
		}
		// The path IS the escape hatch: an operator certain nothing remains removes the
		// file, and a refusal that does not say where it is leaves them with no move.
		if !strings.Contains(err.Error(), markerPath(t, home, "acme")) {
			t.Errorf("the refusal does not name the marker it is refusing over: %v", err)
		}
	})

	// 🔴 AND "COULD NOT TELL" REFUSES. Proceeding on an unanswerable check is how a
	// bootstrap lands on top of a half-removed instance.
	t.Run("a marker that could not be checked is refused too", func(t *testing.T) {
		unstattableMarker(t, fakeHome(t), "acme")

		err := RefuseUnfinishedDestroy("acme")
		if err == nil {
			t.Fatal("a check that could not be answered was read as permission to proceed")
		}
		var unfinished *ErrDestroyUnfinished
		if errors.As(err, &unfinished) {
			t.Fatal("could-not-tell was reported as a definite half-destroyed instance; the " +
				"command layer unwinds the local record on that type and would act on a guess")
		}
	})
}

// recordingKubeClient notes when Helm's uninstall starts turning the release into objects
// to delete — the first DELETION in a destroy, and the thing the marker has to precede.
type recordingKubeClient struct {
	*kubefake.PrintingKubeClient
	record func()
}

func (c recordingKubeClient) Build(r io.Reader, validate bool) (kube.ResourceList, error) {
	c.record()
	return c.PrintingKubeClient.Build(r, validate)
}

// 🔴 THE MARKER IS WRITTEN BEFORE THE FIRST DELETION, AND BEFORE THE CLUSTER IS EVEN
// REACHED. The rig points dcctl at a context that does not resolve, so beginDestroy's
// ClaimClients fails and it returns before it can write a phase anywhere — which is
// precisely the run whose only possible evidence is local. A marker written "next to the
// phase write" would be skipped here, in the one case it exists for.
func TestTheMarkerIsWrittenBeforeTheFirstDeletionAndWithoutReachingTheCluster(t *testing.T) {
	r := newTeardownRig(t, []*release.Release{deviceChainRelease(helmReleaseNameFor("acme"), "acme")},
		[]string{"acme"}, "acme")
	cfg, err := helmActionConfigFor("kind-c")
	if err != nil {
		t.Fatal(err)
	}
	cfg.KubeClient = recordingKubeClient{
		PrintingKubeClient: &kubefake.PrintingKubeClient{Out: io.Discard},
		record:             func() { r.calls = append(r.calls, "chart uninstall") },
	}

	out, err := r.destroy(t, false)
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	if !slices.Contains(r.calls, "chart uninstall") {
		t.Fatal("the chart uninstall never ran, so this test cannot say what the marker preceded")
	}
	// The lock could not be taken, which is the situation being reproduced. If the rig
	// ever starts reaching a cluster this stops being the case it claims to be.
	if !strings.Contains(out, "could not reach the cluster to take the lock") {
		t.Fatalf("the rig reached the cluster, so this is no longer the unreachable-cluster "+
			"case the marker exists for:\n%s", out)
	}
	if !inOrder(r.calls, []string{"mark destroying acme", "chart uninstall"}) {
		t.Errorf("steps ran as:\n  %s\nwant the marker written before the first deletion",
			strings.Join(r.calls, "\n  "))
	}
}

// 🔴 THE PROMPT COMES FIRST, SO A DECLINED DESTROY LEAVES NOTHING BEHIND. Both arms are
// in one test on purpose: the "declined" arm alone passes in a build that writes no
// marker at all, which is the shape of a test that cannot fail.
func TestOnlyAConfirmedDestroyMarksTheInstance(t *testing.T) {
	for _, tc := range []struct {
		name       string
		answer     string
		wantMarker bool
	}{
		{"confirmed", "y\n", true},
		{"declined", "n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTeardownRig(t, []*release.Release{deviceChainRelease(helmReleaseNameFor("acme"), "acme")},
				[]string{"acme"}, "acme")
			answerPrompt(t, tc.answer)

			var err error
			out := captureOutput(t, func() {
				err = Destroy(t.Context(), &fakeProvider{name: "local", present: map[string]bool{"c": true}},
					DestroyOptions{Options: Options{Instance: "acme"}})
			})
			if tc.wantMarker && err != nil {
				t.Fatalf("the confirmed destroy failed: %v\n%s", err, out)
			}
			if !tc.wantMarker && !strings.Contains(out, "Aborted") {
				t.Fatalf("the prompt was not declined, so this arm proves nothing:\n%s", out)
			}
			// A confirmed destroy runs to the end and REMOVES the marker again, so what
			// says it was written is the recorded call; a declined one must have neither.
			if got := slices.Contains(r.calls, "mark destroying acme"); got != tc.wantMarker {
				t.Errorf("marker written = %v, want %v (calls: %v)", got, tc.wantMarker, r.calls)
			}
			if markerExists(t, r.home, "acme") {
				t.Error("a marker was left on disk after the destroy returned")
			}
		})
	}
}

// 🔴 A MARKER THAT CANNOT BE WRITTEN IS LOUD AND IS NOT A STOP, for the reason the lock
// and the phase are not: an operator who has decided to tear an instance down must not be
// blocked because a bookkeeping file could not be written. What they must not get is
// silence — this is the run whose evidence is now missing.
func TestAFailedMarkerWriteWarnsAndDoesNotStopTheDestroy(t *testing.T) {
	r := newTeardownRig(t, []*release.Release{deviceChainRelease(helmReleaseNameFor("acme"), "acme")},
		[]string{"acme"}, "acme")
	writeDestroyMarker = func(string) error { return errors.New("read-only file system") }

	out, err := r.destroy(t, false)
	if err != nil {
		t.Fatalf("a destroy stopped because its marker could not be written: %v\n%s", err, out)
	}
	if !strings.Contains(out, "read-only file system") {
		t.Errorf("the failed marker write said nothing:\n%s", out)
	}
	if !r.stateRemoved() {
		t.Error("the destroy did not finish, so this says nothing about the warning being non-fatal")
	}
}

// 🔴 REMOVAL IS NOT NEW CODE AND THAT IS EXACTLY WHY IT NEEDS A TEST. The marker is
// removed because removeStatePreservingEscrow's default branch removes ordinary files —
// a property of a function written for something else, which nothing would notice losing.
// The marker is planted by hand rather than by the destroy so that this measures the
// REMOVAL and not the write.
func TestAFinishedDestroyRemovesTheMarker(t *testing.T) {
	r := newTeardownRig(t, []*release.Release{deviceChainRelease(helmReleaseNameFor("acme"), "acme")},
		[]string{"acme"}, "acme")
	if err := writeDestroyMarker("acme"); err != nil {
		t.Fatal(err)
	}
	if !markerExists(t, r.home, "acme") {
		t.Fatal("the fixture never wrote the marker this test is about")
	}

	out, err := r.destroy(t, false)
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	if markerExists(t, r.home, "acme") {
		t.Fatal("a finished destroy left its own marker behind, so every later bootstrap of " +
			"this name is refused over a teardown that completed")
	}
	// And the refusal it drives agrees, which is the property an operator actually meets.
	if err := RefuseUnfinishedDestroy("acme"); err != nil {
		t.Fatalf("the name is still refused after a destroy that finished: %v", err)
	}
}

// answerPrompt feeds one line to the confirmation prompt, which reads os.Stdin directly.
func answerPrompt(t *testing.T, answer string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(answer); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig; _ = r.Close() })
}
