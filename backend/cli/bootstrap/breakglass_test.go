// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

func umPod(name string, ready bool, terminating bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: InstanceNamespace("acme"),
			Labels: map[string]string{areaLabel: userManagementArea},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}
	if terminating {
		now := metav1.NewTime(time.Now())
		p.DeletionTimestamp = &now
	}
	return p
}

type execCall struct {
	ns, pod, container string
	command            []string
	stdin              string
}

func stubExec(t *testing.T, stdout, stderr string, err error) *[]execCall {
	t.Helper()
	var calls []execCall
	orig := execInPod
	t.Cleanup(func() { execInPod = orig })
	execInPod = func(_ context.Context, _ *rest.Config, _ kubernetes.Interface,
		ns, pod, container string, command []string, stdin io.Reader) (string, string, error) {
		c := execCall{ns: ns, pod: pod, container: container, command: command}
		if stdin != nil {
			b, _ := io.ReadAll(stdin)
			c.stdin = string(b)
		}
		calls = append(calls, c)
		return stdout, stderr, err
	}
	return &calls
}

// The exec goes to a READY, not-terminating user-management pod of the instance namespace, runs the
// service binary's own subcommand, and carries the address as an argument and a password (when
// given) on stdin and never in the argument list.
func TestBreakGlassExecsTheServiceBinaryInAReadyPod(t *testing.T) {
	typed := fake.NewSimpleClientset(
		umPod("um-a", false, false), umPod("um-b", true, true), umPod("um-c", true, false))
	calls := stubExec(t, "Second factor removed.\n", "", nil)
	var out, errOut bytes.Buffer

	if err := breakGlassMfaReset(context.Background(), &rest.Config{}, typed, "acme", "root@example.com",
		nil, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("%d execs, want 1", len(*calls))
	}
	c := (*calls)[0]
	if c.pod != "um-c" || c.ns != InstanceNamespace("acme") || c.container != "user-management" {
		t.Errorf("exec went to %s/%s container %s", c.ns, c.pod, c.container)
	}
	want := []string{"/ko-app/dc-user-management", "break-glass", "mfa-reset", "--email", "root@example.com", "--password-stdin"}
	if strings.Join(c.command, " ") != strings.Join(want, " ") {
		t.Errorf("command = %v, want %v", c.command, want)
	}
	// dcctl generated the password, sent it on stdin, and printed it once.
	gen := strings.TrimSuffix(c.stdin, "\n")
	if len(gen) != breakGlassPasswordLength || strings.Contains(strings.Join(c.command, " "), gen) {
		t.Errorf("stdin = %q; want one generated line, not in the argument list", c.stdin)
	}
	if strings.Count(out.String(), gen) != 1 || !strings.Contains(out.String(), "New password") {
		t.Errorf("the generated password must be printed exactly once: %q", out.String())
	}
	if !strings.Contains(out.String(), "Second factor removed") {
		t.Errorf("the pod's output was not relayed: %q", out.String())
	}

	// A password: one line on stdin, the flag after it, and the password nowhere in the command.
	*calls = nil
	if err := breakGlassMfaReset(context.Background(), &rest.Config{}, typed, "acme", "root@example.com",
		strings.NewReader("s3cret-pw\nignored\n"), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	c = (*calls)[0]
	if c.stdin != "s3cret-pw\n" {
		t.Errorf("stdin = %q", c.stdin)
	}
	if c.command[len(c.command)-1] != "--password-stdin" {
		t.Errorf("command = %v, want --password-stdin last", c.command)
	}
	if strings.Contains(out.String(), "s3cret-pw") {
		t.Error("a password the operator supplied is echoed back")
	}
	for _, a := range c.command {
		if strings.Contains(a, "s3cret-pw") {
			t.Errorf("the password is in the argument list: %v", c.command)
		}
	}
}

// No ready pod is a refusal that names the namespace, and nothing is executed.
func TestBreakGlassWithNoReadyPodRefuses(t *testing.T) {
	typed := fake.NewSimpleClientset(umPod("um-a", false, false), umPod("um-b", true, true))
	calls := stubExec(t, "", "", nil)
	err := breakGlassMfaReset(context.Background(), &rest.Config{}, typed, "acme", "root@example.com", nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), InstanceNamespace("acme")) {
		t.Fatalf("got %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("%d execs with no ready pod", len(*calls))
	}
}

// The pod's own refusal (its stderr) is shown, the transport error is returned, and nothing from
// stdout is relayed as if it had succeeded.
func TestBreakGlassRelaysTheSubcommandsRefusal(t *testing.T) {
	typed := fake.NewSimpleClientset(umPod("um-a", true, false))
	stubExec(t, "", "break-glass: break-glass is for identities that hold a system role\n", errors.New("command terminated with exit code 1"))
	var out, errOut bytes.Buffer
	err := breakGlassMfaReset(context.Background(), &rest.Config{}, typed, "acme", "pat@example.com", nil, &out, &errOut)
	if err == nil {
		t.Fatal("a failed exec was reported as success")
	}
	if !strings.Contains(errOut.String(), "system role") {
		t.Errorf("stderr = %q", errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q after a failure", out.String())
	}
}

func TestBreakGlassRefusesAnEmptyPasswordLine(t *testing.T) {
	typed := fake.NewSimpleClientset(umPod("um-a", true, false))
	calls := stubExec(t, "", "", nil)
	err := breakGlassMfaReset(context.Background(), &rest.Config{}, typed, "acme", "root@example.com",
		strings.NewReader("\n"), io.Discard, io.Discard)
	if err == nil || len(*calls) != 0 {
		t.Fatalf("err=%v execs=%d; an empty password must stop before the exec", err, len(*calls))
	}
}

// 🔴 THE BINARY'S NAME IS DERIVED, NOT TYPED. ko names the binary after the last element of the main
// package's import path; user-management's main package is the module root, so it is the last
// element of the module path in its go.mod (dc-user-management), not the functional area. This
// reads the module line and applies that rule, so the constant cannot drift from the module (the
// image-smoke CI job then proves the path in the built image).
func TestTheBinaryPathIsTheLastElementOfTheModulePath(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "services", "user-management", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	var module string
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			module = strings.TrimSpace(rest)
			break
		}
	}
	if module == "" {
		t.Fatal("no module line in user-management/go.mod")
	}
	want := "/ko-app/" + module[strings.LastIndex(module, "/")+1:]
	if userManagementBinary != want {
		t.Errorf("userManagementBinary = %q, but ko names the binary %q (module %s)", userManagementBinary, want, module)
	}
}

func superuserSecretFixture(ns, name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Labels:          map[string]string{"app.kubernetes.io/managed-by": "dcctl"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "owner", UID: "u-1"}},
		},
		Data: map[string][]byte{"password": []byte("old"), "other": []byte("keep")},
	}
}

// Resetting the SEED superuser updates the Secret dcctl and the chart read, and only its password
// key: labels, owner and the other keys are untouched. Any other identity leaves it alone.
func TestBreakGlassOnTheSeedSuperuserUpdatesOnlyItsPasswordKey(t *testing.T) {
	ns := InstanceNamespace("acme")
	typed := fake.NewSimpleClientset(umPod("um-a", true, false), superuserSecretFixture(ns, superuserSecretName("acme")))
	calls := stubExec(t, "ok\n", "", nil)
	var out, errOut bytes.Buffer

	if err := breakGlassMfaReset(context.Background(), &rest.Config{}, typed, "acme", auth.DefaultSuperuserEmail,
		nil, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	sent := strings.TrimSuffix((*calls)[0].stdin, "\n")
	got, err := typed.CoreV1().Secrets(ns).Get(context.Background(), superuserSecretName("acme"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data["password"]) != sent {
		t.Errorf("Secret password = %q, want the password sent to the pod", got.Data["password"])
	}
	if string(got.Data["other"]) != "keep" || got.Labels["app.kubernetes.io/managed-by"] != "dcctl" || len(got.OwnerReferences) != 1 {
		t.Errorf("the patch touched more than the password key: %+v", got.ObjectMeta)
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected warning: %q", errOut.String())
	}

	// Another operator identity: the Secret stays as it was.
	other := fake.NewSimpleClientset(umPod("um-a", true, false), superuserSecretFixture(ns, superuserSecretName("acme")))
	if err := breakGlassMfaReset(context.Background(), &rest.Config{}, other, "acme", "ops@example.com",
		nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	s2, _ := other.CoreV1().Secrets(ns).Get(context.Background(), superuserSecretName("acme"), metav1.GetOptions{})
	if string(s2.Data["password"]) != "old" {
		t.Errorf("a non-seed identity changed the superuser Secret: %q", s2.Data["password"])
	}
}

// If the Secret cannot be updated the reset has still happened: the operator is told loudly, with
// the key to set by hand, and the command does not report failure for a rotation that succeeded.
func TestBreakGlassWarnsLoudlyWhenTheSecretCannotBeUpdated(t *testing.T) {
	ns := InstanceNamespace("acme")
	typed := fake.NewSimpleClientset(umPod("um-a", true, false), superuserSecretFixture(ns, superuserSecretName("acme")))
	typed.PrependReactor("patch", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	stubExec(t, "ok\n", "", nil)
	var out, errOut bytes.Buffer
	if err := breakGlassMfaReset(context.Background(), &rest.Config{}, typed, "acme", auth.DefaultSuperuserEmail,
		nil, &out, &errOut); err != nil {
		t.Fatalf("the rotation succeeded, so the command must not fail: %v", err)
	}
	if !strings.Contains(errOut.String(), "WARNING") || !strings.Contains(errOut.String(), "OLD password") {
		t.Errorf("no loud warning: %q", errOut.String())
	}
	if !strings.Contains(out.String(), "New password") {
		t.Error("the new password must still be shown")
	}
}

// An instance with no superuser Secret (built before dcctl generated one) has nothing to update.
func TestBreakGlassWithNoSuperuserSecretIsNotAnError(t *testing.T) {
	typed := fake.NewSimpleClientset(umPod("um-a", true, false))
	stubExec(t, "ok\n", "", nil)
	var errOut bytes.Buffer
	if err := breakGlassMfaReset(context.Background(), &rest.Config{}, typed, "acme", auth.DefaultSuperuserEmail,
		nil, io.Discard, &errOut); err != nil || errOut.Len() != 0 {
		t.Fatalf("err=%v warning=%q", err, errOut.String())
	}
}
