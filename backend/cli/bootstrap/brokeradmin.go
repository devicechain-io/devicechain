// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// The broker administrator.
//
// A stream's configuration decides what is stored under which subject, so the right to create or
// change one belongs to a single login: the broker administrator. The chart runs it as a
// pre-install and pre-upgrade hook Job (templates/broker-admin-job.yaml) before any service starts
// or rolls, and the services only verify what it made. Two things here follow from that.
//
//   - A failed hook fails the Helm install or upgrade with Helm's own sentence, which names the Job
//     and the reason and says nothing of why the reconcile failed. brokerAdminFailure appends the
//     Job's log, because the log is the diagnosis: it names the stream, the field and what to do.
//   - `dcctl broker reconcile` runs the same Job again, for recovery: a stream lost at runtime, a
//     bucket deleted by hand, a pod that stops on "does not match what this build declares".
//     ReconcileBroker builds that Job from the hook manifest the release stores, so what runs is
//     exactly what the last install or upgrade ran, with the same image, areas and login.

const (
	// brokerAdminJobSuffix is the end of the hook Job's name (<instance>-broker-admin), spelled in
	// templates/broker-admin-job.yaml; TestTheChartRendersTheBrokerAdminHook holds the two together.
	brokerAdminJobSuffix = "-broker-admin"
	// brokerAdminLogTail bounds how much of the Job's log is added to an error.
	brokerAdminLogTail = 60
	// brokerAdminManualTimeout bounds a manual reconcile: the Job's own deadline plus the time to
	// schedule and pull.
	brokerAdminManualTimeout = 15 * time.Minute
)

func brokerAdminJobName(instance string) string { return instance + brokerAdminJobSuffix }

// brokerAdminFailure adds the hook Job's log to err when err is Helm reporting that the broker
// administrator's hook failed. Any other error is returned as it came, and a log that cannot be
// read leaves the error as it was: this is a diagnosis added on top of a failure that is already
// being reported.
func brokerAdminFailure(ctx context.Context, kubeContext, instance string, err error) error {
	if err == nil || !strings.Contains(err.Error(), brokerAdminJobName(instance)) {
		return err
	}
	_, _, typed, cerr := kubeClients(kubeContext)
	if cerr != nil {
		return err
	}
	logs := jobLogs(ctx, typed, InstanceNamespace(instance), brokerAdminJobName(instance), brokerAdminLogTail)
	if logs == "" {
		return fmt.Errorf("%w\n\nThe broker administrator's pod log could not be read (kubectl -n %s logs job/%s)",
			err, InstanceNamespace(instance), brokerAdminJobName(instance))
	}
	return fmt.Errorf("%w\n\nThe broker administrator (the chart's pre-install/pre-upgrade hook) failed, so nothing "+
		"was rolled. Its log, last lines:\n\n%s\n\nFix what it names and run the upgrade again, or run "+
		"`dcctl broker reconcile %s` to retry only the reconcile", err, indent(logs), instance)
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

// jobLogs returns the tail of the log of the newest pod of a Job, or "" when there is none to read.
func jobLogs(ctx context.Context, typed kubernetes.Interface, namespace, job string, tail int64) string {
	pods, err := typed.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + job})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	newest := pods.Items[0]
	for _, p := range pods.Items[1:] {
		if p.CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = p
		}
	}
	raw, err := typed.CoreV1().Pods(namespace).GetLogs(newest.Name, &corev1.PodLogOptions{TailLines: &tail}).DoRaw(ctx)
	if err != nil {
		return ""
	}
	return string(raw)
}

// ReconcileBrokerOptions names the instance whose broker is reconciled.
type ReconcileBrokerOptions struct {
	Options
	// Out receives the administrator's log. Defaults to discarding it.
	Out io.Writer
}

// ReconcileBroker runs the broker administrator again for an installed instance and waits for it.
//
// The Job is not made up here. It is the hook the instance's last Helm install or upgrade ran,
// read back from the release record, renamed so it does not collide with that one, and created
// without its hook annotations (Helm would otherwise own and delete it). When the instance
// document was rendered by the chart rather than written by dcctl, the hook's own copy of it is
// created too and removed after.
//
// It returns an error, with the Job's log, when the administrator fails, and leaves that Job in
// place to be read. A Job that succeeds is removed.
func ReconcileBroker(ctx context.Context, opts ReconcileBrokerOptions) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	binding, source := ResolveBinding(opts.Options)
	if err := refuseUnreadable(source, opts.Instance); err != nil {
		return err
	}
	if err := RefuseConflictingContext(source, binding, opts.KubeContext, opts.Instance); err != nil {
		return err
	}
	announceBinding(binding, source, opts.Instance)

	cfg, err := helmActionConfigFor(binding.KubeContext)
	if err != nil {
		return fmt.Errorf("reading the instance's Helm release: %w", err)
	}
	rel, err := action.NewGet(cfg).Run(helmReleaseNameFor(opts.Instance))
	if err != nil {
		return fmt.Errorf("reading the instance's Helm release %q: %w", helmReleaseNameFor(opts.Instance), err)
	}
	job, copies, err := brokerAdminHookObjects(rel, opts.Instance)
	if err != nil {
		return err
	}
	_, _, typed, err := kubeClients(binding.KubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster: %w", err)
	}
	return runBrokerAdminJob(ctx, typed, job, copies, out)
}

// brokerAdminHookObjects picks the broker administrator's Job, and the Secrets it needs that the
// release itself does not keep, out of a release's stored hooks.
func brokerAdminHookObjects(rel *release.Release, instance string) (*batchv1.Job, []*corev1.Secret, error) {
	var job *batchv1.Job
	var copies []*corev1.Secret
	for _, h := range rel.Hooks {
		switch h.Kind {
		case "Job":
			var j batchv1.Job
			if err := yaml.Unmarshal([]byte(h.Manifest), &j); err != nil {
				return nil, nil, fmt.Errorf("reading the broker administrator's hook from the release: %w", err)
			}
			if j.Name == brokerAdminJobName(instance) {
				job = &j
			}
		case "Secret":
			var s corev1.Secret
			if err := yaml.Unmarshal([]byte(h.Manifest), &s); err != nil {
				return nil, nil, fmt.Errorf("reading a hook Secret from the release: %w", err)
			}
			if s.Labels["devicechain.io/component"] == "broker-admin" {
				copies = append(copies, &s)
			}
		}
	}
	if job == nil {
		return nil, nil, fmt.Errorf("instance %q has no broker administrator hook: its release was installed with "+
			"brokerAdmin.enabled=false, or by a version before the broker administrator existed. "+
			"Run `dcctl upgrade` to install it, or reconcile the streams and buckets yourself with the "+
			"dc-broker-admin image", instance)
	}
	return job, copies, nil
}

// runBrokerAdminJob creates job (as a one-off, not a hook) and the Secrets it mounts, waits for it
// to end, prints its log, and removes what it created when it succeeded.
func runBrokerAdminJob(ctx context.Context, typed kubernetes.Interface, job *batchv1.Job, copies []*corev1.Secret, out io.Writer) error {
	ns := job.Namespace
	stamp := fmt.Sprintf("%d", time.Now().Unix())
	job.Name = fmt.Sprintf("%s-%s", job.Name, stamp)
	job.ResourceVersion = ""
	for k := range job.Annotations {
		if strings.HasPrefix(k, "helm.sh/") {
			delete(job.Annotations, k)
		}
	}
	if job.Labels == nil {
		job.Labels = map[string]string{}
	}
	job.Labels["devicechain.io/reconcile"] = "manual"
	// The Job selects its pods by labels the controller adds; a stored hook carries none of its own.
	job.Spec.Selector, job.Spec.Template.Labels = nil, mergeLabels(job.Spec.Template.Labels, job.Labels)

	var createdSecrets []string
	for _, s := range copies {
		s.ResourceVersion = ""
		for k := range s.Annotations {
			if strings.HasPrefix(k, "helm.sh/") {
				delete(s.Annotations, k)
			}
		}
		_, err := typed.CoreV1().Secrets(ns).Create(ctx, s, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			_, err = typed.CoreV1().Secrets(ns).Update(ctx, s, metav1.UpdateOptions{})
		}
		if err != nil {
			return fmt.Errorf("writing the instance document the broker administrator mounts: %w", err)
		}
		createdSecrets = append(createdSecrets, s.Name)
	}
	cleanupSecrets := func() {
		for _, n := range createdSecrets {
			_ = typed.CoreV1().Secrets(ns).Delete(context.Background(), n, metav1.DeleteOptions{})
		}
	}

	if _, err := typed.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		cleanupSecrets()
		return fmt.Errorf("creating the broker administrator's Job: %w", err)
	}
	fmt.Fprintf(out, "Running the broker administrator (job/%s in %s)...\n", job.Name, ns)

	waitCtx, cancel := context.WithTimeout(ctx, brokerAdminManualTimeout)
	defer cancel()
	var failure error
	for {
		cur, err := typed.BatchV1().Jobs(ns).Get(waitCtx, job.Name, metav1.GetOptions{})
		if err != nil {
			failure = fmt.Errorf("watching the broker administrator's Job: %w", err)
			break
		}
		if cur.Status.Succeeded > 0 {
			break
		}
		if jobFailed(cur) {
			failure = fmt.Errorf("the broker administrator failed (job/%s in %s)", job.Name, ns)
			break
		}
		select {
		case <-waitCtx.Done():
			failure = fmt.Errorf("the broker administrator did not finish within %s (job/%s in %s)",
				brokerAdminManualTimeout, job.Name, ns)
		case <-time.After(2 * time.Second):
			continue
		}
		break
	}

	logs := jobLogs(context.Background(), typed, ns, job.Name, 400)
	if logs != "" {
		_, _ = io.Copy(out, bytes.NewBufferString(logs))
	}
	cleanupSecrets()
	if failure != nil {
		return fmt.Errorf("%w. The Job is left in place to read: kubectl -n %s logs job/%s", failure, ns, job.Name)
	}
	prop := metav1.DeletePropagationBackground
	_ = typed.BatchV1().Jobs(ns).Delete(context.Background(), job.Name, metav1.DeleteOptions{PropagationPolicy: &prop})
	return nil
}

func jobFailed(j *batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func mergeLabels(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

const (
	// brokerAdminModuleDir is where the broker administrator's source lives, relative to the
	// repository root, and brokerAdminImageName what the chart pulls it as
	// ({registry}/dc-broker-admin:{tag}, devicechain.brokerAdminImage).
	brokerAdminModuleDir = "backend/admin/dc-broker-admin"
	brokerAdminImageName = "dc-broker-admin"
)
