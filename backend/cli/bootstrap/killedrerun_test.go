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

	"github.com/devicechain-io/dc-microservice/secrets/escrow"
)

// A bootstrap killed in the infrastructure step leaves an escrow artifact on disk and
// the instance's credential Secret in the cluster, but NO copy of the root key in the
// cluster (the key first reaches it in the Helm step's config document). The re-run must
// deploy the artifact's key, write no second artifact, and say so.
func TestARerunAfterAKillAdoptsTheKeyItAlreadyEscrowed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prod.escrow")
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, "prod", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	withExistingInstance(t, "", nil) // no config document: the kill came before the Helm step
	stubInstanceCredentials(t, true, nil)
	st := &State{
		Instance: "prod", BuildImages: true, Values: map[string]string{},
		Escrow: EscrowPlan{Path: path, Passphrase: "pw"},
	}

	out := captureStdout(t, func() {
		if err := stepRenderConfig(t.Context(), st); err != nil {
			t.Fatalf("a re-run over its own escrow artifact was refused: %v", err)
		}
	})
	if got := st.Values["secretsRootKey"]; got != testRootKey {
		t.Fatal("the re-run deploys a key other than the one its escrow artifact holds")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("the artifact was rewritten or removed (read err %v)", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("a second file appeared beside the artifact: %d entries", len(entries))
	}
	if !strings.Contains(out, "already holds this instance's key") || !strings.Contains(out, "reusing it") {
		t.Errorf("the run did not say it was reusing the artifact:\n%s", out)
	}
	if strings.Contains(out, testRootKey) {
		t.Error("the output carries key material")
	}
}

// Each thing the adoption requires is withheld in turn; every one must fall back to the
// refusal rather than mint-and-collide or adopt.
func TestAnArtifactThatIsNotProvablyThisAttemptsStillRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		instance   string // instance the artifact was written for
		passphrase string // passphrase this run has
		kept       bool
		keptErr    error
		garbage    bool
	}{
		"no credential Secret (a destroyed instance's artifact)": {"prod", "pw", false, nil, false},
		"the Secret cannot be read":                              {"prod", "pw", false, errors.New("boom"), false},
		"a passphrase that does not open it":                     {"prod", "wrong", true, nil, false},
		"an artifact for another instance":                       {"staging", "pw", true, nil, false},
		"an unreadable artifact":                                 {"prod", "pw", true, nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "prod.escrow")
			if tc.garbage {
				if err := os.WriteFile(path, []byte("not an artifact"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, tc.instance, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			withExistingInstance(t, "", nil)
			stubInstanceCredentials(t, tc.kept, tc.keptErr)
			st := &State{
				Instance: "prod", BuildImages: true, Values: map[string]string{},
				Escrow: EscrowPlan{Path: path, Passphrase: tc.passphrase},
			}

			err := stepRenderConfig(t.Context(), st)
			if err == nil {
				t.Fatal("an artifact this run cannot prove is its own was accepted")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("the refusal does not name the artifact: %v", err)
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Error("the artifact was changed by a refused run")
			}
		})
	}
}

// The restoring path is unchanged: the restored key wins, and nothing is looked up.
func TestRestoringIsNotRoutedThroughTheAdoption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prod.escrow")
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, "prod", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	withExistingInstance(t, "", nil)
	orig := readInstanceCredentialSecret
	t.Cleanup(func() { readInstanceCredentialSecret = orig })
	readInstanceCredentialSecret = func(context.Context, string, string) (bool, error) {
		t.Error("the adoption consulted the cluster on a restore")
		return true, nil
	}
	st := &State{
		Instance: "prod", BuildImages: true, Values: map[string]string{},
		Escrow: EscrowPlan{RestoredRootKey: testRootKey, RestoredFrom: path},
	}
	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatalf("stepRenderConfig: %v", err)
	}
	if st.Values["secretsRootKey"] != testRootKey {
		t.Error("a restore did not deploy the restored key")
	}
}

// With a config document in the cluster the existing reuse path owns the decision, and
// the adoption is never consulted: a mismatching artifact is still an orphan refusal.
func TestAMismatchedArtifactOverALiveInstanceStillRefuses(t *testing.T) {
	const live = "3q2+796tvu/erb7v3q2+796tvu/erb7v3q0="
	path := filepath.Join(t.TempDir(), "prod.escrow")
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, "prod", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	withExistingInstance(t, live, nil)
	stubInstanceCredentials(t, true, nil)
	st := &State{
		Instance: "prod", BuildImages: true, Values: map[string]string{},
		Escrow: EscrowPlan{Path: path, Passphrase: "pw"},
	}
	err := stepRenderConfig(t.Context(), st)
	if err == nil || !strings.Contains(err.Error(), "does NOT hold the root key") {
		t.Fatalf("want the orphan refusal, got %v", err)
	}
	if st.Values["secretsRootKey"] == testRootKey {
		t.Error("the artifact's key replaced the live instance's")
	}
}

// The adoption reports the artifact's own key id alongside the key it returns.
func TestTheAdoptionReportsTheArtifactsKeyId(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prod.escrow")
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, "prod", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	stubInstanceCredentials(t, true, nil)
	key, id := keyFromHalfBuiltEscrow(t.Context(), &State{
		Instance: "prod", Escrow: EscrowPlan{Path: path, Passphrase: "pw"},
	})
	if key != testRootKey {
		t.Fatal("did not return the escrowed key")
	}
	raw, _ := os.ReadFile(path)
	art, err := escrow.Decode(raw)
	if err != nil || id != art.Fingerprint {
		t.Errorf("key id %q is not the artifact's fingerprint", id)
	}
}

// ---- the stale-lock message ----

func stubProcess(t *testing.T, host string, state procState) {
	t.Helper()
	oh, op := localHostname, probeProcess
	t.Cleanup(func() { localHostname, probeProcess = oh, op })
	localHostname = func() (string, error) { return host, nil }
	probeProcess = func(int) procState { return state }
}

func staleHeld(holder string) *ClaimHeldError {
	return &ClaimHeldError{
		Instance: "prod", Holder: holder, KubeContext: "kind-x",
		Renewed: time.Now().Add(-time.Hour), LooksStale: true,
	}
}

func TestAStaleRefusalNamesADeadLocalProcess(t *testing.T) {
	stubProcess(t, "box", procNotRunning)
	const holder = "me@box/23528/0a1b2c3d4e5f6071"
	msg := staleHeld(holder).Error()
	for _, want := range []string{
		"process 23528 on this machine, which is no longer running",
		holder,
		"dcctl instances reclaim --kube-context kind-x",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
}

func TestAStaleRefusalSaysNothingExtraWhenItCannotKnow(t *testing.T) {
	for name, tc := range map[string]struct {
		host   string
		state  procState
		holder string
	}{
		"process alive":   {"box", procRunning, "me@box/23528/aa"},
		"cannot tell":     {"box", procUnknown, "me@box/23528/aa"},
		"another host":    {"elsewhere", procNotRunning, "me@box/23528/aa"},
		"unparseable pid": {"box", procNotRunning, "me@box/notapid/aa"},
		"no host":         {"box", procNotRunning, "me/23528/aa"},
		"not an identity": {"box", procNotRunning, "somebody"},
		"zero pid":        {"box", procNotRunning, "me@box/0/aa"},
	} {
		t.Run(name, func(t *testing.T) {
			stubProcess(t, tc.host, tc.state)
			msg := staleHeld(tc.holder).Error()
			if strings.Contains(msg, "no longer running") {
				t.Errorf("claimed a dead process without evidence:\n%s", msg)
			}
		})
	}
}

func TestAFreshRefusalNeverDescribesADeadProcess(t *testing.T) {
	stubProcess(t, "box", procNotRunning)
	e := staleHeld("me@box/23528/aa")
	e.LooksStale = false
	if strings.Contains(e.Error(), "no longer running") {
		t.Error("a recently renewed lease was described as a dead process")
	}
}

// The real probe: this very process is running; a pid that cannot exist is not.
func TestTheOSProbeKnowsItsOwnProcess(t *testing.T) {
	if got := osProbeProcess(os.Getpid()); got != procRunning {
		t.Errorf("own pid probed as %v, want running", got)
	}
	if got := osProbeProcess(0x7ffffff0); got == procRunning {
		t.Errorf("an impossible pid probed as running")
	}
}

func TestAHolderWithPunctuationInItsUserStillParses(t *testing.T) {
	stubProcess(t, "box", procNotRunning)
	if pid, ok := holderPidOnThisMachine("DOM\\a.b@c@box/77/aa"); !ok || pid != 77 {
		t.Errorf("got %d %v", pid, ok)
	}
}
