// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func destroyTestHome(t *testing.T) string {
	t.Helper()
	home := fakeHome(t)
	t.Setenv("KUBECONFIG", filepath.Join(home, "kubeconfig"))
	return home
}

func plantDestroyFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDestroyRefusesAPathShapedInstance(t *testing.T) {
	for _, name := range []string{".", "..", "../..", "a/../b", "a/", `a\..\b`, "", "a\x00b"} {
		for _, dry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/dry=%v", name, dry), func(t *testing.T) {
				home := destroyTestHome(t)
				files := []string{"instances/a/terraform.tfstate", "instances/a/instance.json", "instances/b/terraform.tfstate", "instances/b/instance.json", "clusters/x/state", "sims/y/state"}
				for _, f := range files {
					plantDestroyFile(t, filepath.Join(home, ".devicechain", f), []byte("{}"))
				}
				p := &fakeProvider{name: "local"}
				var err error
				out := captureOutput(t, func() {
					err = Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: name, DryRun: dry}})
				})
				if err == nil || !strings.Contains(err.Error(), "is not a plain name") {
					t.Fatalf("expected name refusal, got %v", err)
				}
				if strings.Contains(out, "already gone") || strings.Contains(out, "removed") {
					t.Fatalf("false success: %s", out)
				}
				if p.existsCalls != 0 {
					t.Fatal("consulted provider before name validation")
				}
				for _, f := range files {
					if b, err := os.ReadFile(filepath.Join(home, ".devicechain", f)); err != nil || string(b) != "{}" {
						t.Errorf("file %s changed: %q, %v", f, b, err)
					}
				}
			})
		}
	}
}

func TestRemoveInstanceStateRefusesParent(t *testing.T) {
	home := destroyTestHome(t)
	path := filepath.Join(home, ".devicechain", "instances", "a", "terraform.tfstate")
	plantDestroyFile(t, path, []byte("keep"))
	if err := removeInstanceState(DestroyOptions{Options: Options{Instance: ".."}}); err == nil {
		t.Fatal("accepted parent")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "keep" {
		t.Fatalf("state changed: %q, %v", b, err)
	}
}

func TestLegacyNamesStayDestroyable(t *testing.T) {
	for _, name := range []string{"My_Instance", "UPPER"} {
		t.Run(name, func(t *testing.T) {
			home := destroyTestHome(t)
			dir := filepath.Join(home, ".devicechain", "instances", name)
			rec, err := json.Marshal(InstanceRecord{Instance: name, Cluster: "old", KubeContext: "kind-old"})
			if err != nil {
				t.Fatal(err)
			}
			plantDestroyFile(t, filepath.Join(dir, "instance.json"), rec)
			plantDestroyFile(t, filepath.Join(dir, "terraform.tfstate"), []byte("{}"))
			captureOutput(t, func() {
				if err := Destroy(context.Background(), &fakeProvider{name: "local"}, DestroyOptions{Options: Options{Instance: name}}); err != nil {
					t.Error(err)
				}
			})
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("legacy state survived: %v", err)
			}
		})
	}
}

func TestAGuessedBindingIsNeverDeclaredGone(t *testing.T) {
	for _, kubeContext := range []string{"", "explicit-context"} {
		t.Run("context="+kubeContext, func(t *testing.T) {
			home := destroyTestHome(t)
			path := filepath.Join(home, ".devicechain", "instances", "harig", "terraform.tfstate")
			plantDestroyFile(t, path, []byte(`{"version":4,"resources":[]}`))
			p := &fakeProvider{name: "local"}
			var err error
			out := captureOutput(t, func() {
				err = Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "harig", KubeContext: kubeContext, AssumeYes: true}})
			})
			if err == nil {
				t.Fatal("unreachable cluster was accepted")
			}
			if kubeContext == "" && (!strings.Contains(err.Error(), "--kube-context") || !strings.Contains(err.Error(), "by hand")) {
				t.Fatalf("missing recovery hint: %v", err)
			}
			if kubeContext != "" && strings.Contains(err.Error(), "dcctl has no record") {
				t.Fatalf("guess hint on explicit binding: %v", err)
			}
			if strings.Contains(out, "already gone") {
				t.Fatalf("false gone claim: %s", out)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("state lost: %v", err)
			}
			if p.existsCalls != 0 {
				t.Fatal("unrecorded binding consulted provider listing")
			}
		})
	}
}

func TestARecordedClusterThatStillAnswersIsNotGone(t *testing.T) {
	for _, tc := range []struct{ recorded, answering string }{{"U", "U"}, {"U", "V"}, {"", "U"}} {
		t.Run(tc.recorded+"/"+tc.answering, func(t *testing.T) {
			home := destroyTestHome(t)
			writeRecord(t, InstanceRecord{Instance: "inst", Cluster: "c", KubeContext: "kind-c", ClusterUID: tc.recorded})
			path := filepath.Join(home, ".devicechain", "instances", "inst", "terraform.tfstate")
			plantDestroyFile(t, path, []byte(`{"version":4,"resources":[]}`))
			clusterDir := plantClusterState(t, home, "U")
			orig := kubeContexts
			t.Cleanup(func() { kubeContexts = orig })
			kubeContexts = func() ([]string, string, error) { return []string{"kind-c"}, "", nil }
			calls := stubIdentifyCluster(t, tc.answering, nil)
			var err error
			out := captureOutput(t, func() {
				err = Destroy(context.Background(), &fakeProvider{name: "local"}, DestroyOptions{Options: Options{Instance: "inst", AssumeYes: true}})
			})
			if *calls != 1 {
				t.Errorf("identity calls: %d", *calls)
			}
			if tc.recorded == "U" && tc.answering == "V" && (err == nil || !strings.Contains(err.Error(), "different cluster")) {
				t.Fatalf("expected identity refusal, got %v", err)
			}
			if strings.Contains(out, "already gone") {
				t.Fatalf("false gone claim: %s", out)
			}
			for _, f := range []string{path, filepath.Join(clusterDir, "infra", "cluster", "terraform.tfstate")} {
				if _, err := os.Stat(f); err != nil {
					t.Errorf("state lost: %v", err)
				}
			}
		})
	}
}

func TestRecordedContextThatNoLongerAnswersAllowsCleanup(t *testing.T) {
	home := destroyTestHome(t)
	writeRecord(t, InstanceRecord{Instance: "inst", Cluster: "c", KubeContext: "kind-c", ClusterUID: "U"})
	origContexts, origIdentify := kubeContexts, identifyCluster
	t.Cleanup(func() { kubeContexts, identifyCluster = origContexts, origIdentify })
	kubeContexts = func() ([]string, string, error) { return []string{"kind-c"}, "", nil }
	identifyCluster = func(ctx context.Context, name string) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Error("identity probe lacks short timeout")
		}
		return "", fmt.Errorf("reading cluster identity: %w", &url.Error{
			Op:  "Get",
			URL: "https://127.0.0.1:6443/api/v1/namespaces/kube-system",
			Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
		})
	}
	out := captureOutput(t, func() {
		if err := Destroy(context.Background(), &fakeProvider{name: "local"}, DestroyOptions{Options: Options{Instance: "inst"}}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "already gone") {
		t.Fatalf("missing gone message: %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".devicechain", "instances", "inst")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state survived: %v", err)
	}
}

func TestRecordedContextWithUnreadableIdentityKeepsState(t *testing.T) {
	for name, cause := range map[string]error{
		"forbidden":    apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "kube-system", errors.New("access denied")),
		"unauthorized": apierrors.NewUnauthorized("credentials expired"),
		"TLS":          &url.Error{Op: "Get", URL: "https://cluster", Err: errors.New("x509: certificate signed by unknown authority")},
	} {
		t.Run(name, func(t *testing.T) {
			home := destroyTestHome(t)
			writeRecord(t, InstanceRecord{Instance: "inst", Cluster: "c", KubeContext: "kind-c", ClusterUID: "U"})
			path := filepath.Join(home, ".devicechain", "instances", "inst", "terraform.tfstate")
			plantDestroyFile(t, path, []byte("keep"))
			clusterDir := plantClusterState(t, home, "U")
			orig := kubeContexts
			t.Cleanup(func() { kubeContexts = orig })
			kubeContexts = func() ([]string, string, error) { return []string{"kind-c"}, "", nil }
			stubIdentifyCluster(t, "", fmt.Errorf("reading cluster identity: %w", cause))
			var err error
			out := captureOutput(t, func() {
				err = Destroy(context.Background(), &fakeProvider{name: "local"}, DestroyOptions{Options: Options{Instance: "inst"}})
			})
			if !errors.Is(err, cause) || !strings.Contains(err.Error(), `recorded context "kind-c" answered but its identity could not be read`) ||
				!strings.Contains(err.Error(), "local state was kept") {
				t.Fatalf("expected identity refusal, got %v", err)
			}
			if strings.Contains(out, "already gone") {
				t.Fatalf("false gone claim: %s", out)
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
				t.Fatalf("state changed: %q, %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(clusterDir, "infra", "cluster", "terraform.tfstate")); err != nil {
				t.Fatalf("cluster state lost: %v", err)
			}
		})
	}
}

func TestClusterUnreachable(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"nil":              {nil, false},
		"generic":          {errors.New("dial failed"), false},
		"canceled":         {context.Canceled, false},
		"probe deadline":   {context.DeadlineExceeded, true},
		"connection reset": {syscall.ECONNRESET, true},
		"refused":          {syscall.ECONNREFUSED, true},
		"host unreachable": {syscall.EHOSTUNREACH, true},
		"net unreachable":  {syscall.ENETUNREACH, true},
		"DNS":              {&net.DNSError{Err: "no such host", Name: "cluster"}, true},
		"URL timeout":      {&url.Error{Op: "Get", URL: "https://cluster", Err: os.ErrDeadlineExceeded}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := clusterUnreachable(tc.err); got != tc.want {
				t.Fatalf("clusterUnreachable(%v) = %v, want %v", tc.err, got, tc.want)
			}
			if tc.err != nil && clusterUnreachable(fmt.Errorf("identity: %w", tc.err)) != tc.want {
				t.Fatalf("wrapped error changed result: %v", tc.err)
			}
		})
	}
}

func TestClusterProbeHonorsParentCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%v", deadline), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			origContexts, origIdentify := kubeContexts, identifyCluster
			t.Cleanup(func() { kubeContexts, identifyCluster = origContexts, origIdentify })
			kubeContexts = func() ([]string, string, error) { return []string{"kind-c"}, "", nil }
			identifyCluster = func(context.Context, string) (string, error) {
				cancel()
				if deadline {
					return "", fmt.Errorf("probe: %w", context.DeadlineExceeded)
				}
				return "", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
			}
			gone, err := corroborateClusterGone(ctx, ClusterBinding{KubeContext: "kind-c"})
			if gone || !errors.Is(err, context.Canceled) {
				t.Fatalf("parent cancellation read as cluster loss: gone=%v, err=%v", gone, err)
			}
		})
	}
}

func TestClusterProbeDeadlineAllowsCleanup(t *testing.T) {
	orig := kubeContexts
	t.Cleanup(func() { kubeContexts = orig })
	kubeContexts = func() ([]string, string, error) { return []string{"kind-c"}, "", nil }
	stubIdentifyCluster(t, "", fmt.Errorf("probe: %w", context.DeadlineExceeded))
	if gone, err := corroborateClusterGone(context.Background(), ClusterBinding{KubeContext: "kind-c"}); !gone || err != nil {
		t.Fatalf("probe timeout was not evidence of loss: gone=%v, err=%v", gone, err)
	}
}

func TestDestroyNameFailsClosedWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if err := ValidateDestroyableInstanceName("valid"); err == nil {
		t.Fatal("accepted unresolved home")
	}
}

func TestDestroyKeepsStateWhenKubeconfigCannotBeRead(t *testing.T) {
	home := destroyTestHome(t)
	writeRecord(t, InstanceRecord{Instance: "inst", Cluster: "c", KubeContext: "kind-c"})
	path := filepath.Join(home, ".devicechain", "instances", "inst", "instance.json")
	plantDestroyFile(t, filepath.Join(home, "kubeconfig"), []byte("contexts: ["))
	var err error
	out := captureOutput(t, func() {
		err = Destroy(context.Background(), &fakeProvider{name: "local"}, DestroyOptions{Options: Options{Instance: "inst"}})
	})
	if err == nil {
		t.Error("unreadable kubeconfig was treated as proof of loss")
	}
	if strings.Contains(out, "already gone") {
		t.Errorf("false gone claim: %s", out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state lost: %v", err)
	}
}
