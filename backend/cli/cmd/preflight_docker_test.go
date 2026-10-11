// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// fakeHost presents a host that has every tool EXCEPT docker, and records what the
// doctor asked it to run. It restores the real lookups when the test ends.
func fakeHost(t *testing.T, have func(name string) bool) (ran *[][]string) {
	t.Helper()
	origLook, origRun := lookPath, runCmd
	t.Cleanup(func() { lookPath, runCmd = origLook, origRun })
	var calls [][]string
	lookPath = func(name string) (string, error) {
		if have(name) {
			return "/fake/" + name, nil
		}
		return "", exec.ErrNotFound
	}
	runCmd = func(name string, args ...string) string {
		calls = append(calls, append([]string{name}, args...))
		return "" // no daemon, no cluster
	}
	return &calls
}

func noDocker(name string) bool { return name != "docker" }

func ranDocker(calls [][]string) bool {
	for _, c := range calls {
		if c[0] == "docker" {
			return true
		}
	}
	return false
}

// A remote-image install into an existing cluster needs no Docker daemon. The
// doctor used to fail it on "docker not found" and "docker daemon not reachable".
func TestAnInstallIntoAnExistingClusterDoesNotNeedDocker(t *testing.T) {
	calls := fakeHost(t, noDocker)
	opts := doctorOpts{KubeContext: "prod"}
	d := &doctor{}
	d.checkTools("local", opts)
	d.checkDocker(opts)
	if d.fails != 0 {
		t.Errorf("a remote install on a host with no docker failed %d preflight check(s); it never uses one", d.fails)
	}
	if ranDocker(*calls) {
		t.Errorf("the doctor ran docker (%v) for an install that never touches a daemon", *calls)
	}
}

// The counterweight: skipping the check is only safe while the paths that DO use
// a daemon still fail fast without one.
func TestARunThatUsesDockerStillFailsFastWithoutIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts doctorOpts
	}{
		{"local kind cluster", doctorOpts{}},
		{"local build into an existing cluster", doctorOpts{KubeContext: "prod", Build: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeHost(t, noDocker)
			d := &doctor{}
			d.checkTools("local", tc.opts)
			if d.fails == 0 {
				t.Fatal("a missing docker binary was not a failure for a run that needs a daemon")
			}
			d = &doctor{}
			fakeHost(t, func(string) bool { return true }) // binary present, daemon silent
			d.checkDocker(tc.opts)
			if d.fails == 0 {
				t.Error("an unreachable docker daemon was not a failure for a run that needs one")
			}
		})
	}
}

// The version floor is asked of the cluster the run targets. With no context given
// the query is unchanged, so a local kind install still reads the current one.
func TestTheKubernetesVersionIsAskedOfTheSelectedContext(t *testing.T) {
	if got, want := kubectlVersionArgs(""), []string{"version", "-o", "json"}; !reflect.DeepEqual(got, want) {
		t.Errorf("no context: %v, want %v", got, want)
	}
	calls := fakeHost(t, func(string) bool { return true })
	d := &doctor{}
	d.checkKubeServerVersion("staging")
	var kubectl []string
	for _, c := range *calls {
		if c[0] == "kubectl" {
			kubectl = c
		}
	}
	if kubectl == nil || !strings.Contains(strings.Join(kubectl, " "), "--context staging") {
		t.Errorf("kubectl was asked %v; it must carry --context staging or it answers for the CURRENT context", kubectl)
	}
}
