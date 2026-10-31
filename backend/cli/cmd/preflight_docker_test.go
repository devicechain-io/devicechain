// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
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
	if got, want := kubectlVersionArgs("staging"), []string{"version", "-o", "json", "--context", "staging"}; !reflect.DeepEqual(got, want) {
		t.Errorf("with context: %v, want %v", got, want)
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

// writeKubeconfig points the contexts check at a kubeconfig holding exactly names.
func writeKubeconfig(t *testing.T, names ...string) {
	t.Helper()
	body := `apiVersion: v1
kind: Config
`
	if len(names) > 0 {
		body += `contexts:
`
	}
	for _, n := range names {
		body += `- name: ` + n + `
  context: {cluster: c, user: u}
`
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
}

// seams records whether the host checks ran, and with what.
type seams struct {
	systemRan, portsRan bool
	hostsKind           bool
}

func fakeChecks(t *testing.T) *seams {
	t.Helper()
	origSys, origPorts := systemCheck, portsCheck
	t.Cleanup(func() { systemCheck, portsCheck = origSys, origPorts })
	r := &seams{}
	systemCheck = func(_ *doctor, _ string, hostsKind bool) { r.systemRan, r.hostsKind = true, hostsKind }
	portsCheck = func(*doctor) { r.portsRan = true }
	return r
}

func kubectlContext(calls [][]string) string {
	for _, c := range calls {
		if c[0] == "kubectl" {
			for i, a := range c {
				if a == "--context" && i+1 < len(c) {
					return c[i+1]
				}
			}
			return ""
		}
	}
	return "<kubectl not run>"
}

// The one mapping from flags to checks. With no --kube-context the target is the kind
// cluster the run will create or reuse, NOT whatever the kubeconfig currently selects.
func TestDoctorOptsForResolvesTheTargetContext(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		kubeContext, cluster string
		build                bool
		target               string
		docker, kind         bool
	}{
		{"default kind cluster", "", "", false, "kind-devicechain", true, true},
		{"named kind cluster", "", "lab", false, "kind-lab", true, true},
		{"existing cluster", "prod", "lab", false, "prod", false, false},
		{"existing cluster, build", "prod", "lab", true, "prod", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := doctorOptsFor(tc.kubeContext, tc.cluster, tc.build)
			if o.Target != tc.target || o.needsDocker() != tc.docker || o.hostsKind() != tc.kind {
				t.Errorf("got target=%q docker=%v kind=%v, want %q %v %v", o.Target, o.needsDocker(), o.hostsKind(), tc.target, tc.docker, tc.kind)
			}
		})
	}
}

// runDoctor against a host with no kind, no docker and a kubeconfig that holds prod.
func TestRunDoctorOnAnExistingClusterSkipsEveryLocalClusterCheck(t *testing.T) {
	calls := fakeHost(t, func(n string) bool { return n != "docker" && n != "kind" })
	sc := fakeChecks(t)
	writeKubeconfig(t, "prod")
	d := runDoctor("local", doctorOptsFor("prod", "lab", false))
	if d.fails != 0 {
		t.Errorf("%d failure(s) for an install into an existing cluster; kind and docker are not needed", d.fails)
	}
	if sc.systemRan || sc.portsRan {
		t.Errorf("host checks ran (system=%v ports=%v) for a remote cluster", sc.systemRan, sc.portsRan)
	}
	if got := kubectlContext(*calls); got != "prod" {
		t.Errorf("version asked of context %q, want prod", got)
	}
}

// --build on an existing cluster needs a daemon and its data-root disk, but hosts no kind.
func TestRunDoctorBuildingOnAnExistingClusterChecksDockerNotKind(t *testing.T) {
	calls := fakeHost(t, func(n string) bool { return n != "kind" })
	sc := fakeChecks(t)
	writeKubeconfig(t, "prod")
	runDoctor("local", doctorOptsFor("prod", "", true))
	if !sc.systemRan || sc.hostsKind {
		t.Errorf("system check ran=%v hostsKind=%v; want ran without the kind-node checks", sc.systemRan, sc.hostsKind)
	}
	if sc.portsRan {
		t.Error("local port checks ran for a cluster dcctl does not host")
	}
	if !ranDocker(*calls) {
		t.Error("the docker engine was not checked for a --build run")
	}
}

// With no --kube-context the version floor is asked of the kind cluster, not the
// kubeconfig's current context.
func TestRunDoctorOnALocalClusterChecksKindAndAsksTheKindContext(t *testing.T) {
	calls := fakeHost(t, func(string) bool { return true })
	sc := fakeChecks(t)
	writeKubeconfig(t, "other")
	runDoctor("local", doctorOptsFor("", "lab", false))
	if !sc.systemRan || !sc.hostsKind || !sc.portsRan {
		t.Errorf("local run: system=%v hostsKind=%v ports=%v, want all true", sc.systemRan, sc.hostsKind, sc.portsRan)
	}
	if got := kubectlContext(*calls); got != "kind-lab" {
		t.Errorf("version asked of context %q, want kind-lab", got)
	}
}

// A --kube-context the kubeconfig does not hold is a failure, including when it holds none.
func TestAMissingKubeContextFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		names []string
	}{
		{"other contexts present", []string{"staging"}},
		{"no contexts at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeHost(t, func(string) bool { return true })
			writeKubeconfig(t, tc.names...)
			d := &doctor{}
			d.checkKubeContexts("prod")
			if d.fails == 0 {
				t.Error("a --kube-context absent from the kubeconfig did not fail the preflight")
			}
		})
	}
	writeKubeconfig(t, "prod")
	d := &doctor{}
	d.checkKubeContexts("prod")
	if d.fails != 0 {
		t.Errorf("a present context failed (%d)", d.fails)
	}
}

// A kind cluster that does not exist yet is not a missing --kube-context: the
// kubeconfig having no kind-<cluster> must not add a failure to a local run.
func TestALocalRunDoesNotRequireTheKindContextToExistYet(t *testing.T) {
	fakeHost(t, func(string) bool { return true })
	fakeChecks(t)
	writeKubeconfig(t, "other")
	without := runDoctor("local", doctorOptsFor("", "lab", false)).fails
	writeKubeconfig(t, "other", "kind-lab")
	with := runDoctor("local", doctorOptsFor("", "lab", false)).fails
	if without != with {
		t.Errorf("failures %d without kind-lab in the kubeconfig, %d with: a cluster not yet created was treated as a missing context", without, with)
	}
}

// A taken host port stops the kind create on Docker's bind error seconds into an
// install, so the preflight must fail on it when the cluster does not exist yet, and
// must not when the cluster that holds the ports is the one being reused.
func TestATakenHostPortFailsOnlyWhenTheClusterIsNotYetThere(t *testing.T) {
	origPort, origRun := portInUse, runCmd
	t.Cleanup(func() { portInUse, runCmd = origPort, origRun })
	portInUse = func(p int) bool { return p == 80 || p == 1883 }
	existing := ""
	runCmd = func(name string, args ...string) string {
		switch {
		case name == "kind":
			return existing
		case name == "docker" && len(args) > 1 && args[1] == "--filter":
			return "other-control-plane"
		}
		return ""
	}

	d := &doctor{cluster: "lab"}
	d.checkPorts()
	if d.fails != 2 {
		t.Errorf("failures = %d with ports 80 and 1883 taken and no cluster lab, want 2", d.fails)
	}

	existing = "other\nlab\n"
	d = &doctor{cluster: "lab"}
	d.checkPorts()
	if d.fails != 0 {
		t.Errorf("failures = %d, want 0: the ports are held by the existing cluster lab itself", d.fails)
	}
}
