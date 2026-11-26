// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"strings"

	"github.com/fatih/color"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// An upgrade moves an instance one minor at a time. The versioning policy has always said that
// skipping a minor is not a tested path; this is where that rule is enforced, because later
// releases rely on it. A release can only depend on a protocol the release before it already
// follows on every pod if no pod older than that release is still running when it rolls out.
//
// 🔴 THE VERSION JUDGED IS THE ONE THE WORKLOADS RUN, NOT THE DECLARATION. `dcctl upgrade`
// records the target in the Instance before it moves anything (recordUpgradedVersion), so after
// a run that stopped half-way the declaration names a version some pods do not run. Judging the
// declaration would let "declared v1.9, running v1.8" license a move to v1.10. The running
// version is read from the area Deployments' images; while they disagree, the OLDEST is the
// version the instance has completed, and a resumed upgrade to the same target is judged from it.
//
// 🔴 FAIL CLOSED. A running version that cannot be read refuses, naming the override, because
// "I could not tell" must not read as "nothing older is running".

// skippedMinorProbe reads the version the instance has completed; a seam for tests.
var skippedMinorProbe = completedVersion

// completedVersion is the oldest release tag any functional-area POD runs.
//
// 🔴 PODS, NOT DEPLOYMENTS. A Deployment's template names the version it is rolling TO: after a
// rollout that stopped half-way every template can already say v1.9 while pods of the old
// ReplicaSet still run v1.8, and judging the templates would then license v1.10. Every pod that
// has not finished counts, including one being deleted, because until it is gone an older
// version is still running.
//
// 🔴 THE AREA'S OWN CONTAINER, BY NAME. The chart names each area's application container after
// the area (the devicechain.io/functional-area label). A pod without that container, or a
// container with no tag, cannot be read and refuses, rather than letting a sidecar's tag stand
// in for the service's.
//
// A tag that is not a release (a local "dev" build) names no position in the release order: it
// is returned only when no pod runs a release tag.
func completedVersion(ctx context.Context, typed kubernetes.Interface, instance string) (string, error) {
	pods, err := typed.CoreV1().Pods(InstanceNamespace(instance)).List(ctx, metav1.ListOptions{LabelSelector: areaLabel})
	if err != nil {
		return "", fmt.Errorf("listing the instance's area pods: %w", err)
	}
	oldest, oldestCore, other := "", [3]int{}, ""
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue // finished: it runs nothing
		}
		area := p.Labels[areaLabel]
		image := ""
		for _, c := range p.Spec.Containers {
			if c.Name == area {
				image = c.Image
			}
		}
		if image == "" {
			return "", fmt.Errorf("pod %s has no container named after its area %q", p.Name, area)
		}
		tag := imageTagOf(image)
		if tag == "" {
			return "", fmt.Errorf("pod %s runs an image with no tag (%s)", p.Name, image)
		}
		c, ok := releaseCore(tag)
		if !ok {
			other = tag
			continue
		}
		if oldest == "" || compareCore(c, oldestCore) < 0 {
			oldest, oldestCore = tag, c
		}
	}
	if oldest != "" {
		return oldest, nil
	}
	if other != "" {
		return other, nil
	}
	return "", fmt.Errorf("no area pod of instance %q is running", instance)
}

// imageTagOf is the tag of an image reference, "" when it has none (a digest-only reference).
func imageTagOf(image string) string {
	image = strings.SplitN(image, "@", 2)[0]
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon < 0 || colon < slash {
		return ""
	}
	return image[colon+1:]
}

func compareCore(a, b [3]int) int {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// checkSkippedMinor refuses a move from the version an instance runs to one more than a minor
// ahead. Allowed: the same minor (a patch or a re-run), the next minor, and across a major only
// to its first minor (x.0). A downgrade is left to the checks that govern it, and a version that
// is not a release tag (a local build) is outside the release order, so it is not judged here.
func checkSkippedMinor(from, to string, allow bool) error {
	f, okFrom := releaseCore(from)
	t, okTo := releaseCore(to)
	if !okFrom || !okTo {
		return nil
	}
	var next string
	switch {
	case t[0] < f[0], t[0] == f[0] && t[1] <= f[1]+1:
		return nil
	case t[0] == f[0]+1 && t[1] == 0:
		return nil
	case t[0] == f[0]:
		next = fmt.Sprintf("v%d.%d", f[0], f[1]+1)
	default:
		next = fmt.Sprintf("v%d.%d or v%d.0", f[0], f[1]+1, f[0]+1)
	}
	if allow {
		fmt.Println(color.RedString("  WARNING: --allow-skipped-minor moves this instance from %s to %s, skipping a minor. "+
			"That path is UNSUPPORTED and untested; it does not disable any protection the database enforces.", from, to))
		return nil
	}
	return fmt.Errorf("moving from %s to %s skips a minor release, which is not supported: upgrade to %s first, "+
		"then to %s. The running version is read from the instance's workloads, not its declaration. "+
		"--allow-skipped-minor overrides this (unsupported). Nothing has been changed", from, to, next, to)
}

// precheckSkippedMinor is checkSkippedMinor for an upgrade run, asked before anything is written.
func precheckSkippedMinor(ctx context.Context, typed kubernetes.Interface, st *State, allow bool) error {
	return precheckSkippedMinorWith(ctx, st, allow, func(ctx context.Context) (string, error) {
		return skippedMinorProbe(ctx, typed, st.Instance)
	})
}

func precheckSkippedMinorWith(ctx context.Context, st *State, allow bool, running func(context.Context) (string, error)) error {
	if _, ok := releaseCore(st.ImageVersion); !ok {
		return nil // an unversioned target is outside the release order
	}
	from, err := running(ctx)
	if err != nil {
		if allow {
			out(ctx).println(color.RedString("  WARNING: --allow-skipped-minor: the version instance %q runs could not be read (%v); "+
				"moving to %s without knowing whether a minor is skipped (unsupported)", st.Instance, err, st.ImageVersion))
			return nil
		}
		return fmt.Errorf("upgrading instance %q: the version it runs could not be read (%v), so it cannot be shown that the "+
			"move to %s skips no minor release. --allow-skipped-minor overrides this (unsupported). Nothing has been changed",
			st.Instance, err, st.ImageVersion)
	}
	if err := checkSkippedMinor(from, st.ImageVersion, allow); err != nil {
		return fmt.Errorf("upgrading instance %q: %w", st.Instance, err)
	}
	return nil
}
