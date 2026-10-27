// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// The break-glass reset of an operator identity's second factor.
//
// 🔴 dcctl DOES NOT RESET ANYTHING ITSELF. It finds a ready user-management pod and runs the
// service's own binary in it (`dc-user-management break-glass mfa-reset`), so the reset is the store
// code the service runs and not a second copy in this CLI that could drift, and there is no SQL here.
// The authority is the cluster credential that may exec into the pod (pods/exec in the instance
// namespace), which is the point: this is for the operator who cannot sign in, so nothing it does
// may depend on signing in.

const (
	// userManagementArea is the functional area (and the container name) the exec targets.
	userManagementArea = "user-management"
	// userManagementBinary is where the service image puts the binary. ko names it after the last
	// element of the main package's import path, which here is the module path
	// github.com/devicechain-io/dc-user-management, so it is dc-user-management and NOT the area name.
	// Two things hold this to the truth: a unit test derives it from the module's go.mod, and the
	// image-smoke CI job runs this exact path in the built image.
	userManagementBinary = "/ko-app/dc-user-management"
	areaLabel            = "devicechain.io/functional-area"
)

// BreakGlassMfaReset removes the second factor from an operator identity of the instance, replaces
// its password and ends its sessions, by running the service's own subcommand in a ready
// user-management pod.
//
// dcctl chooses the new password itself (crypto/rand, or the first line of passwordStdin when it is
// non-nil) and sends it to the pod on stdin. That makes it the one party that can also write the
// password down: it is printed once, and when the identity is the instance's seed superuser the
// Secret that dcctl and the chart read that password from is updated to match.
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

// The generated password's shape matches the service's own generator: no characters people misread
// from a terminal, none a shell treats specially.
const (
	breakGlassPasswordLength = 24
	breakGlassAlphabet       = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
)

func generateBreakGlassPassword() (string, error) {
	out := make([]byte, breakGlassPasswordLength)
	max := big.NewInt(int64(len(breakGlassAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generating a password: %w", err)
		}
		out[i] = breakGlassAlphabet[n.Int64()]
	}
	return string(out), nil
}

func breakGlassMfaReset(ctx context.Context, restCfg *rest.Config, typed kubernetes.Interface,
	instance, email string, passwordStdin io.Reader, out, errOut io.Writer) error {
	if strings.TrimSpace(email) == "" {
		return errors.New("an email address is required")
	}
	var password string
	generated := passwordStdin == nil
	if generated {
		var err error
		if password, err = generateBreakGlassPassword(); err != nil {
			return err
		}
	} else {
		line, rerr := bufio.NewReader(passwordStdin).ReadString('\n')
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return fmt.Errorf("reading the new password: %w", rerr)
		}
		password = strings.TrimRight(line, "\r\n")
		if password == "" {
			return errors.New("the new password read from stdin was empty; break-glass always replaces the password")
		}
	}
	ns := InstanceNamespace(instance)
	pod, err := readyUserManagementPod(ctx, typed, ns)
	if err != nil {
		return err
	}
	// One line on stdin, never an argument: an argument is in the process list of the node.
	command := []string{userManagementBinary, "break-glass", "mfa-reset", "--email", email, "--password-stdin"}
	stdout, stderr, err := execInPod(ctx, restCfg, typed, ns, pod, userManagementArea, command, strings.NewReader(password+"\n"))
	if stderr != "" {
		fmt.Fprint(errOut, stderr)
	}
	if err != nil {
		// The subcommand's own refusal is on stderr above; this is the transport's word for it.
		return fmt.Errorf("break-glass in pod %s/%s did not succeed: %w", ns, pod, err)
	}
	fmt.Fprint(out, stdout)
	if generated {
		fmt.Fprintf(out, "New password (shown once, not stored anywhere else): %s\n", password)
	}
	// The database has changed. The seed superuser's password also lives in a Secret that dcctl
	// (sim, the probes, a bootstrap re-run) and the chart read, and it is now wrong.
	if strings.EqualFold(strings.TrimSpace(email), auth.DefaultSuperuserEmail) {
		if perr := patchSuperuserSecret(ctx, typed, instance, password); perr != nil {
			fmt.Fprintf(errOut, "WARNING: the reset SUCCEEDED, but Secret %s/%s still holds the OLD password: %v\n"+
				"  dcctl sim, the probes and a bootstrap re-run read it and will fail to sign in until it is updated.\n"+
				"  Set its %q key to the new password, or pass --admin-password.\n",
				ns, superuserSecretName(instance), perr, superuserSecretKey)
		} else {
			fmt.Fprintf(out, "Updated Secret %s/%s to the new password.\n", ns, superuserSecretName(instance))
		}
	}
	return nil
}

// patchSuperuserSecret sets ONLY the password key of the instance's superuser Secret, leaving its
// labels, owner references and every other key as they were. An instance with no such Secret (one
// built before dcctl generated it) has nothing to update and is not an error.
func patchSuperuserSecret(ctx context.Context, typed kubernetes.Interface, instance, password string) error {
	ref := superuserSecretRef(instance)
	if _, err := typed.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	patch, err := json.Marshal(map[string]any{"data": map[string][]byte{ref.Key: []byte(password)}})
	if err != nil {
		return err
	}
	_, err = typed.CoreV1().Secrets(ref.Namespace).Patch(ctx, ref.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
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
