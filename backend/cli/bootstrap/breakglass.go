// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// The break-glass reset of an operator identity's second factor.
//
// 🔴 dcctl DOES NOT RESET ANYTHING ITSELF. It finds a ready user-management pod and runs the
// service's own binary in it (`user-management break-glass mfa-reset`), so the reset is the store
// code the service runs and not a second copy in this CLI that could drift, and there is no SQL here.
// The authority is the cluster credential that may exec into the pod, which is the point: this is
// for the operator who cannot sign in, so nothing it does may depend on signing in.

const (
	// userManagementArea is the functional area (and the container name) the exec targets.
	userManagementArea = "user-management"
	// userManagementBinary is where the service image puts the binary (ko's convention).
	userManagementBinary = "/ko-app/user-management"
	areaLabel            = "devicechain.io/functional-area"
)

// BreakGlassMfaReset removes the second factor from an operator identity of the instance, replaces
// its password and ends its sessions, by running the service's own subcommand in a ready
// user-management pod. With a non-nil passwordStdin its first line is the new password;
// otherwise the pod generates one and prints it, once, to out.
func BreakGlassMfaReset(ctx context.Context, kubeContext, instance, email string,
	passwordStdin io.Reader, out, errOut io.Writer) error {
	if err := ValidateInstanceName(instance); err != nil {
		return err
	}
	restCfg, err := RestConfig(kubeContext)
	if err != nil {
		return fmt.Errorf("building kube config: %w", err)
	}
	_, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster: %w", err)
	}
	return breakGlassMfaReset(ctx, restCfg, typed, instance, email, passwordStdin, out, errOut)
}

func breakGlassMfaReset(ctx context.Context, restCfg *rest.Config, typed kubernetes.Interface,
	instance, email string, passwordStdin io.Reader, out, errOut io.Writer) error {
	if strings.TrimSpace(email) == "" {
		return errors.New("an email address is required")
	}
	ns := InstanceNamespace(instance)
	pod, err := readyUserManagementPod(ctx, typed, ns)
	if err != nil {
		return err
	}
	command := []string{userManagementBinary, "break-glass", "mfa-reset", "--email", email}
	var stdin io.Reader
	if passwordStdin != nil {
		// One line, never an argument: an argument is in the process list of the node.
		line, rerr := bufio.NewReader(passwordStdin).ReadString('\n')
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return fmt.Errorf("reading the new password: %w", rerr)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return errors.New("the new password read from stdin was empty; break-glass always replaces the password")
		}
		command = append(command, "--password-stdin")
		stdin = strings.NewReader(line + "\n")
	}
	stdout, stderr, err := execInPod(ctx, restCfg, typed, ns, pod, userManagementArea, command, stdin)
	if stderr != "" {
		fmt.Fprint(errOut, stderr)
	}
	if err != nil {
		// The subcommand's own refusal is on stderr above; this is the transport's word for it.
		return fmt.Errorf("break-glass in pod %s/%s did not succeed: %w", ns, pod, err)
	}
	fmt.Fprint(out, stdout)
	return nil
}

// readyUserManagementPod picks a running, ready, not-terminating user-management pod of the
// namespace, or says why there is none.
func readyUserManagementPod(ctx context.Context, typed kubernetes.Interface, ns string) (string, error) {
	pods, err := typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: areaLabel + "=" + userManagementArea})
	if err != nil {
		return "", fmt.Errorf("listing the user-management pods in namespace %s: %w", ns, err)
	}
	var ready []string
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				ready = append(ready, p.Name)
			}
		}
	}
	if len(ready) == 0 {
		return "", fmt.Errorf("no ready user-management pod in namespace %s (%d found); "+
			"is the instance running, and is this the right --kube-context?", ns, len(pods.Items))
	}
	sort.Strings(ready)
	return ready[0], nil
}
