// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
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
	calls := stubExec(t, "New password (shown once): abc\n", "", nil)
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
	want := []string{"/ko-app/user-management", "break-glass", "mfa-reset", "--email", "root@example.com"}
	if strings.Join(c.command, " ") != strings.Join(want, " ") {
		t.Errorf("command = %v, want %v", c.command, want)
	}
	if c.stdin != "" {
		t.Errorf("stdin = %q with no password given", c.stdin)
	}
	if !strings.Contains(out.String(), "New password") {
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
