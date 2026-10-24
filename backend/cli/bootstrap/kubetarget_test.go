// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	"k8s.io/client-go/tools/clientcmd"
)

// kubeFixture writes a kubeconfig whose contexts each reach a server named after the
// cluster, with a CA that differs per cluster. A value "server#seed" derives the CA from
// seed instead, so two files can name the same server with different CAs.
// current-context is current (may be "").
func kubeFixture(t *testing.T, dir, name, current string, contexts map[string]string) string {
	t.Helper()
	names := make([]string, 0, len(contexts))
	for ctx := range contexts {
		names = append(names, ctx)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: Config\nclusters:\n")
	for _, ctx := range names {
		server, seed, _ := strings.Cut(contexts[ctx], "#")
		if seed == "" {
			seed = server
		}
		ca := base64.StdEncoding.EncodeToString([]byte("ca-of-" + seed))
		fmt.Fprintf(&b, "- name: c-%s\n  cluster:\n    server: https://%s:6443\n    certificate-authority-data: %s\n", ctx, server, ca)
	}
	b.WriteString("users:\n")
	for _, ctx := range names {
		fmt.Fprintf(&b, "- name: u-%s\n  user:\n    token: tok-%s\n", ctx, ctx)
	}
	b.WriteString("contexts:\n")
	for _, ctx := range names {
		fmt.Fprintf(&b, "- name: %s\n  context:\n    cluster: c-%s\n    user: u-%s\n", ctx, ctx, ctx)
	}
	fmt.Fprintf(&b, "current-context: %s\n", current)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// useHomeKubeconfig makes path the file client-go falls back to when KUBECONFIG is unset
// (a variable fixed at init, which kubeisolation already replaces for the run).
func useHomeKubeconfig(t *testing.T, path string) {
	t.Helper()
	old := clientcmd.RecommendedHomeFile
	clientcmd.RecommendedHomeFile = path
	t.Cleanup(func() { clientcmd.RecommendedHomeFile = old })
}

// testKube gives a test that only needs SOME resolvable cluster one.
func testKube(t *testing.T) {
	t.Helper()
	p := kubeFixture(t, t.TempDir(), "config", "test", map[string]string{"test": "test.example"})
	t.Setenv("KUBECONFIG", p)
}

// pinRecordingTofu is a tofu that logs every invocation (one line each) beside itself.
func pinRecordingTofu(t *testing.T) (bin, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX fake tofu")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
echo "$*" >> "$(dirname "$0")/calls.log"
case "$1" in
  version) echo '{"terraform_version":"1.8.0","platform":"linux_amd64","provider_selections":{}}' ;;
  show)    echo '{"format_version":"1.0"}' ;;
  output)  echo '{}' ;;
  *)       : ;;
esac
`
	bin = filepath.Join(dir, "tofu")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, filepath.Join(dir, "calls.log")
}

// mustFreeze freezes the target for kubeContext as a runner built now would.
func mustFreeze(t *testing.T, kubeContext string) kubeTarget {
	t.Helper()
	tg, err := freezeKubeTarget(kubeContext)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

// varsOf returns the -var assignments of the last call to subcommand.
func varsOf(t *testing.T, log, sub string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		f := strings.Fields(lines[i])
		if len(f) == 0 || f[0] != sub {
			continue
		}
		out := map[string]string{}
		for j := 0; j+1 < len(f); j++ {
			if f[j] == "-var" {
				k, v, _ := strings.Cut(f[j+1], "=")
				out[k] = v
			}
		}
		return out
	}
	t.Fatalf("tofu was never run with %q:\n%s", sub, b)
	return nil
}

// ambientTrap puts a stale file under the home kubeconfig that holds a context of the
// SAME NAME as the one the test's own KUBECONFIG defines, on a different server. It
// returns the KUBECONFIG file, which is the one tofu must be handed.
func ambientTrap(t *testing.T) (want string) {
	t.Helper()
	dir := t.TempDir()
	useHomeKubeconfig(t, kubeFixture(t, dir, "home", "shared", map[string]string{"shared": "old-cluster"}))
	want = kubeFixture(t, dir, "lane", "shared", map[string]string{"shared": "new-cluster"})
	t.Setenv("KUBECONFIG", want)
	return want
}

// The reported bug, through the real command paths that build a tofu invocation: with a
// lane-private KUBECONFIG naming the new cluster, and a stale home file holding a
// context of the same name, every provider-configuring command must be handed the
// KUBECONFIG file and that context.
func TestTofuIsHandedTheFileTheClientReads(t *testing.T) {
	want := ambientTrap(t)
	// The ambient variable the old workaround (and kubeisolation) sets must not outrank the pin.
	t.Setenv("TF_VAR_kubeconfig_path", filepath.Join(t.TempDir(), "stale"))
	ctx := context.Background()
	st := &State{Instance: "acme", KubeContext: "shared", Values: map[string]string{}}

	check := func(t *testing.T, log, sub string) {
		t.Helper()
		got := varsOf(t, log, sub)
		if got["kubeconfig_path"] != want {
			t.Errorf("%s: kubeconfig_path = %q, want the KUBECONFIG file %q", sub, got["kubeconfig_path"], want)
		}
		if got["kubeconfig_context"] != "shared" {
			t.Errorf("%s: kubeconfig_context = %q, want shared", sub, got["kubeconfig_context"])
		}
	}

	t.Run("instance apply", func(t *testing.T) {
		bin, log := pinRecordingTofu(t)
		tf, err := newTofuExec(t.TempDir(), bin, st.KubeContext)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyInstanceInfra(ctx, st, tf, infraVars(st)); err != nil {
			t.Fatal(err)
		}
		check(t, log, "apply")
	})
	t.Run("instance apply on the current context", func(t *testing.T) {
		bin, log := pinRecordingTofu(t)
		tf, _ := newTofuExec(t.TempDir(), bin, "")
		if err := applyInstanceInfra(ctx, &State{Instance: "acme", Values: map[string]string{}}, tf, nil); err != nil {
			t.Fatal(err)
		}
		check(t, log, "apply")
	})
	t.Run("destroy", func(t *testing.T) {
		bin, log := pinRecordingTofu(t)
		tf, _ := newTofuExec(t.TempDir(), bin, "shared")
		uninstall := func(context.Context, string, string, string) (bool, error) { return false, nil }
		if err := destroyOpenedInstanceRoot(ctx, tf, "shared", "acme", uninstall); err != nil {
			t.Fatal(err)
		}
		check(t, log, "destroy")
	})
	t.Run("namespace import", func(t *testing.T) {
		bin, log := pinRecordingTofu(t)
		tf, _ := newTofuExec(t.TempDir(), bin, "shared")
		if err := adoptInfraNamespace(ctx, tf, "dc-system", []string{"kubeconfig_context=shared"}); err != nil {
			t.Fatal(err)
		}
		check(t, log, "import")
	})
	t.Run("plan", func(t *testing.T) {
		bin, log := pinRecordingTofu(t)
		tf, _ := newTofuExec(t.TempDir(), bin, "shared")
		if _, err := tf.Plan(ctx, tfexec.Var("kubeconfig_context=elsewhere")); err != nil {
			t.Fatal(err)
		}
		check(t, log, "plan")
	})
}

// The same context name in two files of a multi-file KUBECONFIG: client-go gives the
// first definition of a name precedence, and the pin must agree with it.
func TestAContextNameCollisionResolvesAsTheClientDoes(t *testing.T) {
	dir := t.TempDir()
	first := kubeFixture(t, dir, "first", "dup", map[string]string{"dup": "first-cluster"})
	second := kubeFixture(t, dir, "second", "dup", map[string]string{"dup": "second-cluster", "other": "other-cluster"})
	t.Setenv("KUBECONFIG", first+string(os.PathListSeparator)+second)

	pin, err := resolveKubePin(mustFreeze(t, "dup"), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.release()
	cfg, err := RestConfig("dup")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pin.identity, cfg.Host) || cfg.Host != "https://first-cluster:6443" {
		t.Fatalf("the pin reaches %q but the client reaches %q (want first-cluster, the first definition)", pin.identity, cfg.Host)
	}
}

// KUBECONFIG listing several files: the context may live in ANY of them, and it must be
// found rather than the first file being taken.
func TestAMultiFileKUBECONFIGIsMergedNotTruncated(t *testing.T) {
	dir := t.TempDir()
	a := kubeFixture(t, dir, "a", "in-a", map[string]string{"in-a": "cluster-a"})
	b := kubeFixture(t, dir, "b", "in-a", map[string]string{"in-b": "cluster-b"})
	t.Setenv("KUBECONFIG", a+string(os.PathListSeparator)+b)

	bin, log := pinRecordingTofu(t)
	work := t.TempDir()
	root := filepath.Join(work, "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tf, _ := newTofuExec(root, bin, "in-b")
	if err := tf.Destroy(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := varsOf(t, log, "destroy")
	if got["kubeconfig_context"] != "in-b" {
		t.Fatalf("context = %q, want in-b", got["kubeconfig_context"])
	}
	if got["kubeconfig_path"] == a || got["kubeconfig_path"] == b {
		t.Fatalf("a multi-file KUBECONFIG was handed to tofu as one of its files (%s): that silently drops the others", got["kubeconfig_path"])
	}
	if _, err := os.Stat(got["kubeconfig_path"]); !os.IsNotExist(err) {
		t.Errorf("the merged kubeconfig (credentials) outlived the command that needed it: %v", err)
	}

	// While it exists it must reach the SAME cluster the client reaches for that context.
	pin, err := resolveKubePin(mustFreeze(t, "in-b"), work)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.release()
	if !pin.merged || !strings.HasPrefix(pin.identity, "https://cluster-b:6443") {
		t.Fatalf("merged pin = %+v, want a merged file reaching cluster-b", pin)
	}
	raw, _ := os.ReadFile(pin.Path)
	if strings.Contains(string(raw), "cluster-a") {
		t.Errorf("the merged file carries a cluster the context does not use:\n%s", raw)
	}
}

// The identity check: if the cluster behind the frozen context is no longer the one the
// client was built against, nothing runs. Two ways it can move: the SERVER changes, or
// only the CERTIFICATE AUTHORITY does (a cluster rebuilt behind the same address).
func TestTofuIsRefusedWhenItsClusterIsNotTheClients(t *testing.T) {
	for name, moved := range map[string]string{
		"another server":              "somewhere-else",
		"the same server, another CA": "new-cluster#rebuilt",
	} {
		t.Run(name, func(t *testing.T) {
			lane := ambientTrap(t)
			bin, log := pinRecordingTofu(t)
			tf, err := newTofuExec(t.TempDir(), bin, "shared")
			if err != nil {
				t.Fatal(err)
			}
			// The file is rewritten after the runner (and, in a real run, the clients) were built.
			kubeFixture(t, filepath.Dir(lane), filepath.Base(lane), "shared", map[string]string{"shared": moved})

			ctx := context.Background()
			for name, run := range map[string]func() error{
				"destroy": func() error { return tf.Destroy(ctx) },
				"apply":   func() error { return tf.Apply(ctx) },
				"plan":    func() error { _, err := tf.Plan(ctx); return err },
				"import":  func() error { return tf.Import(ctx, "a.b", "x") },
			} {
				err := run()
				if err == nil || !strings.Contains(err.Error(), "refusing to run OpenTofu") {
					t.Errorf("%s = %v, want a refusal", name, err)
				}
			}
			if b, _ := os.ReadFile(log); strings.Contains(string(b), "destroy") || strings.Contains(string(b), "apply") ||
				strings.Contains(string(b), "plan") || strings.Contains(string(b), "import") {
				t.Fatalf("tofu ran a provider-configuring command despite the mismatch:\n%s", b)
			}
		})
	}
}

// With no --kube-context the current context is resolved ONCE, when the runner is built.
// Another process running `kubectl config use-context` afterwards must not retarget it.
func TestTheCurrentContextIsFrozenWhenTheRunnerIsBuilt(t *testing.T) {
	dir := t.TempDir()
	path := kubeFixture(t, dir, "config", "first", map[string]string{"first": "first-cluster", "second": "second-cluster"})
	t.Setenv("KUBECONFIG", path)
	bin, log := pinRecordingTofu(t)
	tf, err := newTofuExec(t.TempDir(), bin, "")
	if err != nil {
		t.Fatal(err)
	}
	kubeFixture(t, dir, "config", "second", map[string]string{"first": "first-cluster", "second": "second-cluster"})
	if err := tf.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := varsOf(t, log, "apply")["kubeconfig_context"]; got != "first" {
		t.Fatalf("kubeconfig_context = %q after use-context, want the frozen first", got)
	}
}

// A saved plan can only be applied by the runner that made it (the one that recorded the
// cluster it was made for), and not after the cluster behind the frozen context moved.
func TestASavedPlanIsOnlyAppliedToTheClusterItWasMadeFor(t *testing.T) {
	ambientTrap(t)
	bin, log := pinRecordingTofu(t)
	tf, _ := newTofuExec(t.TempDir(), bin, "shared")
	ctx := context.Background()

	if err := tf.Apply(ctx, tfexec.DirOrPlan("p.bin")); err == nil {
		t.Fatal("applied a plan this runner never made")
	}
	if _, err := tf.Plan(ctx, tfexec.Out("p.bin")); err != nil {
		t.Fatal(err)
	}
	if err := tf.Apply(ctx, tfexec.DirOrPlan("p.bin")); err != nil {
		t.Fatalf("applying its own plan: %v", err)
	}
	// -var is refused beside a saved plan, so none may be added to it.
	if v := varsOf(t, log, "apply"); len(v) != 0 {
		t.Errorf("apply of a saved plan was given -var %v", v)
	}
	// The cluster behind the name is rebuilt: the same name, another server.
	t.Setenv("KUBECONFIG", kubeFixture(t, t.TempDir(), "moved", "shared", map[string]string{"shared": "third-cluster"}))
	if err := tf.Apply(ctx, tfexec.DirOrPlan("p.bin")); err == nil || !strings.Contains(err.Error(), "refusing to run OpenTofu") {
		t.Fatalf("apply after the cluster changed = %v, want a refusal", err)
	}
}

// The provider's own environment defaults would override part of the pinned cluster. The
// ones that only default an attribute the roots always set cannot, and are not refused.
func TestAProviderOverrideInTheEnvironmentIsRefused(t *testing.T) {
	ambientTrap(t)
	target := mustFreeze(t, "shared")
	for _, name := range []string{"KUBE_HOST", "KUBE_CTX_CLUSTER", "KUBE_CTX_AUTH_INFO", "KUBE_TOKEN", "KUBE_INSECURE"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "x")
			if _, err := resolveKubePin(target, t.TempDir()); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("resolveKubePin = %v, want a refusal naming %s", err, name)
			}
		})
	}
	for _, name := range []string{"KUBE_CONFIG_PATH", "KUBE_CONFIG_PATHS", "KUBE_CTX"} {
		t.Run(name+" is not refused", func(t *testing.T) {
			t.Setenv(name, "x")
			pin, err := resolveKubePin(target, t.TempDir())
			if err != nil {
				t.Fatalf("refused %s, which cannot override the pinned file and context: %v", name, err)
			}
			pin.release()
		})
	}
}

// A Plan that is never followed by an Apply (a rehearsal, a refusal) must not leave the
// merged kubeconfig, which holds credentials, on disk.
func TestAPlanLeavesNoMergedKubeconfigBehind(t *testing.T) {
	dir := t.TempDir()
	a := kubeFixture(t, dir, "a", "in-a", map[string]string{"in-a": "cluster-a"})
	b := kubeFixture(t, dir, "b", "in-a", map[string]string{"in-b": "cluster-b"})
	t.Setenv("KUBECONFIG", a+string(os.PathListSeparator)+b)
	bin, log := pinRecordingTofu(t)
	work := t.TempDir()
	root := filepath.Join(work, "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tf, err := newTofuExec(root, bin, "in-b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tf.Plan(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := varsOf(t, log, "plan")["kubeconfig_path"]
	if filepath.Dir(path) != work {
		t.Fatalf("the plan was not given a merged kubeconfig beside the root (%s), so this proves nothing", path)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the merged kubeconfig outlived a Plan nobody applied: %v", err)
	}
}

// The runner is built at three sites, and each must be handed the caller's kube-context.
// The identity check cannot see a wrong one -- the wrong context is still a real cluster
// the client can reach -- so these drive each site with a KUBECONFIG whose current-context
// is a DIFFERENT cluster and assert what tofu is told.
func TestEachRunnerSiteIsHandedTheCallersKubeContext(t *testing.T) {
	dir := t.TempDir()
	lane := kubeFixture(t, dir, "lane", "other", map[string]string{"shared": "new-cluster", "other": "other-cluster"})
	t.Setenv("KUBECONFIG", lane)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	bin, log := pinRecordingTofu(t)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	check := func(t *testing.T, sub string) {
		t.Helper()
		got := varsOf(t, log, sub)
		if got["kubeconfig_context"] != "shared" || got["kubeconfig_path"] != lane {
			t.Errorf("%s was told context %q file %q, want shared %q (current-context is other)",
				sub, got["kubeconfig_context"], got["kubeconfig_path"], lane)
		}
	}

	t.Run("openInstanceRoot", func(t *testing.T) {
		st := &State{Instance: "acme", KubeContext: "shared", Values: map[string]string{}}
		inst, err := openInstanceRoot(ctx, st)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := inst.tf.Plan(ctx); err != nil {
			t.Fatal(err)
		}
		check(t, "plan")
	})
	t.Run("applyClusterPrereqs", func(t *testing.T) {
		st := &State{Instance: "acme", KubeContext: "shared", Values: map[string]string{}}
		// It fails after the apply, reading the object store off a cluster that is not there;
		// what it told tofu is what is asserted.
		_, _ = applyClusterPrereqs(ctx, st, "uid-1", nil, "dc-system")
		check(t, "apply")
		check(t, "import")
	})
	t.Run("destroyInstanceRoot", func(t *testing.T) {
		old := destroyUninstaller
		destroyUninstaller = func(context.Context, string, string, string) (bool, error) { return false, nil }
		t.Cleanup(func() { destroyUninstaller = old })
		if err := destroyInstanceRoot(ctx, "shared", "acme"); err != nil {
			t.Fatal(err)
		}
		check(t, "destroy")
	})
}

func TestAnUnresolvableContextIsRefused(t *testing.T) {
	ambientTrap(t)
	if _, err := freezeKubeTarget("nope"); err == nil {
		t.Fatal("a context that is in no kubeconfig resolved")
	}
	t.Setenv("KUBECONFIG", kubeFixture(t, t.TempDir(), "nocur", "", map[string]string{"x": "x"}))
	if _, err := freezeKubeTarget(""); err == nil {
		t.Fatal("no current-context and no --kube-context resolved to something")
	}
}
