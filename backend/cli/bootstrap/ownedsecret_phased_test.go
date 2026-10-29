// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// phasedArea is one credentials Secret that dcctl writes outside the release, driven the way
// helmInstall drives it, so the same ordering tests run over the Sparkplug broker Secret and the
// LwM2M identities Secret.
type phasedArea struct {
	name string
	// state is a run given n credentials (n == 0: a withdrawal), with a distinguishing tag.
	state func(n int, tag string) *State
	// sync is the first phase.
	sync func(typed kubernetes.Interface, st *State) (string, postReleaseStep, error)
	// secret reads the Secret.
	secret func(t *testing.T, typed kubernetes.Interface) (map[string][]byte, bool)
	// key is the Secret key of credential n (1-based).
	key         func(n int) string
	foreignName string
}

func phasedAreas() []phasedArea {
	return []phasedArea{
		{
			name: "sparkplug",
			state: func(n int, tag string) *State {
				var src []SparkplugSource
				for i := 0; i < n; i++ {
					src = append(src, SparkplugSource{Tenant: "plant", Broker: SparkplugBroker{URL: "tcp://b:1883", Password: "pw-" + tag}})
				}
				st := sparkplugState(src)
				st.SparkplugSourcesGiven = true
				st.Evolving = true
				return st
			},
			sync: func(typed kubernetes.Interface, st *State) (string, postReleaseStep, error) {
				return syncSparkplugBrokerSecret(context.Background(), typed, st)
			},
			secret:      brokerSecret,
			key:         sparkplugPasswordEnv,
			foreignName: "dci-dctest-sparkplug-broker",
		},
		{
			name: "lwm2m",
			state: func(n int, tag string) *State {
				var ids []Lwm2mIdentity
				for i := 0; i < n; i++ {
					ids = append(ids, Lwm2mIdentity{Identity: "id", PSK: distinctPSK(tag), Tenant: "acme", ExternalID: "e", DeviceTypeToken: "s"})
				}
				st := lwm2mState(ids)
				st.Lwm2mIdentitiesGiven = true
				st.Evolving = true
				return st
			},
			sync: func(typed kubernetes.Interface, st *State) (string, postReleaseStep, error) {
				return syncLwm2mIdentitiesSecret(context.Background(), typed, st, nil, true)
			},
			secret:      lwm2mSecret,
			key:         lwm2mPSKEnv,
			foreignName: "dci-dctest-lwm2m-identities",
		},
	}
}

// run is one whole run against the cluster: phase one, then the release (releaseErr), then phase
// two as helmInstall runs it. It returns the stamp the release was built with, what finishing the
// run returned, and the warnings it printed.
func (a phasedArea) run(t *testing.T, typed kubernetes.Interface, st *State, releaseErr error) (string, error, []string) {
	t.Helper()
	stamp, post, err := a.sync(typed, st)
	if err != nil {
		t.Fatal(err)
	}
	var warns []string
	err = finishOwnedSecrets(context.Background(), releaseErr, []postReleaseStep{post}, func(m string) { warns = append(warns, m) })
	return stamp, err, warns
}

func TestARotationToFewerCredentialsKeepsTheOldKeysUntilTheReleaseSucceeds(t *testing.T) {
	t.Parallel()
	for _, a := range phasedAreas() {
		t.Run(a.name, func(t *testing.T) {
			typed := versionedCluster()
			if _, err, _ := a.run(t, typed, a.state(2, "old"), nil); err != nil {
				t.Fatal(err)
			}

			releaseFailed := errors.New("helm: timed out waiting for the condition")
			_, err, _ := a.run(t, typed, a.state(1, "new"), releaseFailed)
			if err != releaseFailed {
				t.Fatalf("the failed release was not returned: %v", err)
			}
			data, _ := a.secret(t, typed)
			if len(data[a.key(2)]) == 0 || len(data[a.key(1)]) == 0 {
				t.Fatalf("a key the OLD pods still reference is gone after a failed release: %v", secretKeys(data))
			}

			// The re-run, release succeeding this time, converges.
			if _, err, warns := a.run(t, typed, a.state(1, "new"), nil); err != nil || len(warns) != 0 {
				t.Fatalf("%v %v", err, warns)
			}
			data, _ = a.secret(t, typed)
			if _, still := data[a.key(2)]; still || len(data[a.key(1)]) == 0 {
				t.Fatalf("an unreferenced key survived a successful release: %v", secretKeys(data))
			}
		})
	}
}

func TestAWithdrawalKeepsTheSecretUntilTheReleaseSucceeds(t *testing.T) {
	t.Parallel()
	for _, a := range phasedAreas() {
		t.Run(a.name, func(t *testing.T) {
			typed := versionedCluster()
			if _, err, _ := a.run(t, typed, a.state(2, "old"), nil); err != nil {
				t.Fatal(err)
			}
			releaseFailed := errors.New("helm: upgrade failed")
			stamp, err, _ := a.run(t, typed, a.state(0, ""), releaseFailed)
			if err != releaseFailed || stamp != "" {
				t.Fatalf("%v %q", err, stamp)
			}
			if _, ok := a.secret(t, typed); !ok {
				t.Fatal("the Secret was deleted although the release failed, so the old pods' next restart cannot resolve it")
			}
			if _, err, _ := a.run(t, typed, a.state(0, ""), nil); err != nil {
				t.Fatal(err)
			}
			if _, ok := a.secret(t, typed); ok {
				t.Fatal("the withdrawn Secret survived a successful release")
			}
		})
	}
}

func TestAForeignSecretIsNeverShrunkOrDeleted(t *testing.T) {
	t.Parallel()
	for _, a := range phasedAreas() {
		t.Run(a.name, func(t *testing.T) {
			foreign := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: a.foreignName, Namespace: "dci-dctest"},
				Data:       map[string][]byte{"theirs": []byte("theirs"), a.key(2): []byte("theirs-too")},
			}
			typed := versionedCluster(foreign)
			// Phase one refuses both a rotation and a withdrawal, before any release.
			for _, n := range []int{1, 0} {
				if _, _, err := a.sync(typed, a.state(n, "x")); err == nil {
					t.Fatalf("a foreign Secret was accepted by a run of %d credentials", n)
				}
			}
			// ...and so does phase two, should the Secret have turned foreign since.
			owner := instanceOwner("dctest", "uid-1")
			for name, step := range map[string]postReleaseStep{
				"prune":    prunePostStep(typed, owner, "dci-dctest", a.foreignName, map[string]string{a.key(1): "v"}, "7", "rerun"),
				"withdraw": withdrawPostStep(typed, owner, "dci-dctest", a.foreignName, "rerun"),
			} {
				var warns []string
				if err := finishOwnedSecrets(context.Background(), nil, []postReleaseStep{step}, func(m string) { warns = append(warns, m) }); err != nil || len(warns) != 1 {
					t.Fatalf("%s: %v %v", name, err, warns)
				}
			}
			data, ok := a.secret(t, typed)
			if !ok || len(data) != 2 || string(data["theirs"]) != "theirs" {
				t.Fatalf("the foreign Secret was touched: %v", secretKeys(data))
			}
		})
	}
}

// blockWrites makes the fake API server refuse every Secret update and delete while *on is set.
func blockWrites(typed *fake.Clientset, on *bool) {
	for _, verb := range []string{"update", "delete"} {
		typed.PrependReactor(verb, "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			if *on {
				return true, nil, errors.New("etcd unavailable")
			}
			return false, nil, nil
		})
	}
}

func TestAFailedPostStepWarnsWithTheCleanupAndTheNextRunConverges(t *testing.T) {
	t.Parallel()
	for _, a := range phasedAreas() {
		for _, withdraw := range []bool{false, true} {
			name, next := a.name+"/shrink", a.state(1, "new")
			if withdraw {
				name, next = a.name+"/withdraw", a.state(0, "")
			}
			t.Run(name, func(t *testing.T) {
				typed := versionedCluster()
				if _, err, _ := a.run(t, typed, a.state(2, "old"), nil); err != nil {
					t.Fatal(err)
				}
				blocked := false
				blockWrites(typed, &blocked)

				// Phase one, then the release succeeds, then the cluster refuses the second phase.
				_, post, err := a.sync(typed, next)
				if err != nil {
					t.Fatal(err)
				}
				blocked = true
				var warns []string
				if err := finishOwnedSecrets(context.Background(), nil, []postReleaseStep{post}, func(m string) { warns = append(warns, m) }); err != nil {
					t.Fatalf("a failed post step failed a run whose release had succeeded: %v", err)
				}
				if len(warns) != 1 || !strings.Contains(warns[0], "Finish it with: ") || !strings.Contains(warns[0], "dci-dctest/") {
					t.Fatalf("no actionable warning: %v", warns)
				}

				// The server recovers; the same command, run again, finishes the job.
				blocked = false
				if _, err, warns := a.run(t, typed, next, nil); err != nil || len(warns) != 0 {
					t.Fatalf("%v %v", err, warns)
				}
				data, ok := a.secret(t, typed)
				if withdraw && ok {
					t.Fatalf("the re-run did not delete the Secret: %v", secretKeys(data))
				}
				if !withdraw && (!ok || len(data[a.key(2)]) != 0) {
					t.Fatalf("the re-run did not remove the stale key: %v", secretKeys(data))
				}
			})
		}
	}
}

// Removing an unreferenced key must not look like a change of what the pods read: the stamp the
// release carried is the stamp every later run computes, until a value actually changes.
func TestPruningDoesNotChangeThePodsStampButARotationDoes(t *testing.T) {
	t.Parallel()
	for _, a := range phasedAreas() {
		t.Run(a.name, func(t *testing.T) {
			typed := versionedCluster()
			if _, err, _ := a.run(t, typed, a.state(2, "old"), nil); err != nil {
				t.Fatal(err)
			}
			released, err, _ := a.run(t, typed, a.state(1, "new"), nil) // shrinks AFTER the release
			if err != nil {
				t.Fatal(err)
			}
			if data, _ := a.secret(t, typed); len(data[a.key(2)]) != 0 {
				t.Fatalf("the key was not pruned: %v", secretKeys(data))
			}
			// A later run given nothing reads the Secret for its stamp.
			keep := a.state(1, "new")
			keep.SparkplugSourcesGiven, keep.Lwm2mIdentitiesGiven = false, false
			read, _, err := a.sync(typed, keep)
			if err != nil {
				t.Fatal(err)
			}
			if read != released {
				t.Fatal("pruning a key changed the stamp, so the next run would roll the pods for nothing")
			}
			// The same credentials given again change nothing.
			again, _, err := a.sync(typed, a.state(1, "new"))
			if err != nil || again != released {
				t.Fatalf("re-running the same command moved the stamp: %v", err)
			}
			// A real rotation still rolls them.
			rotated, _, err := a.sync(typed, a.state(1, "rotated"))
			if err != nil || rotated == released {
				t.Fatalf("a rotated value left the stamp where it was: %v", err)
			}
		})
	}
}

func TestAFailedReleaseRunsNoPostStep(t *testing.T) {
	ran := false
	step := postReleaseStep{run: func(context.Context) error { ran = true; return nil }}
	boom := errors.New("boom")
	if err := finishOwnedSecrets(context.Background(), boom, []postReleaseStep{step, {}}, func(string) {}); err != boom || ran {
		t.Fatalf("%v %v", err, ran)
	}
}

func secretKeys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
