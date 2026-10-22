// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-k8s/functionalarea"
	"github.com/devicechain-io/dc-microservice/config"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"
)

func renderRelease(t *testing.T, vals map[string]interface{}) *release.Release {
	t.Helper()
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	inst := action.NewInstall(&action.Configuration{})
	inst.ReleaseName = "dc-acme"
	inst.Namespace = "default"
	inst.DryRun = true
	inst.ClientOnly = true
	inst.APIVersions = []string{"monitoring.coreos.com/v1"}
	rel, err := inst.RunWithContext(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	return rel
}

func acmeValues(t *testing.T, profile string) map[string]interface{} {
	t.Helper()
	enabled, err := ResolveEnabledAreas(profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := loginState(t)
	st.Instance = "acme"
	st.Profile = profile
	st.EnabledAreas = enabled
	st.ImageRegistry, st.ImageVersion = DefaultImageRegistry, "v0.0.0-test"
	st.Values["ingressHost"] = "localhost"
	st.Values["secretsRootKey"] = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	return helmValues(st)
}

// 🔴 THE CHART'S ONE HOOK. It is the only workload that mounts the broker administrator's login, it
// runs before the release's own objects (pre-install and pre-upgrade) so every service starts
// against a broker that has already been shaped, and a failure keeps the Job (its pod log is the
// diagnosis) while a success removes it.
func TestTheChartRendersTheBrokerAdminHook(t *testing.T) {
	rel := renderRelease(t, acmeValues(t, "full"))

	var job *batchv1.Job
	var secrets []*corev1.Secret
	for _, h := range rel.Hooks {
		switch h.Kind {
		case "Job":
			job = &batchv1.Job{}
			if err := yaml.Unmarshal([]byte(h.Manifest), job); err != nil {
				t.Fatal(err)
			}
		case "Secret":
			s := &corev1.Secret{}
			if err := yaml.Unmarshal([]byte(h.Manifest), s); err != nil {
				t.Fatal(err)
			}
			secrets = append(secrets, s)
		default:
			t.Errorf("the chart renders a hook of kind %s; the broker administrator's Job (and its document) are the only hooks", h.Kind)
		}
		events := map[string]bool{}
		for _, e := range h.Events {
			events[string(e)] = true
		}
		if !events["pre-install"] || !events["pre-upgrade"] || len(events) != 2 {
			t.Errorf("hook %s runs on %v, want exactly pre-install and pre-upgrade", h.Name, h.Events)
		}
	}
	if job == nil {
		t.Fatal("the chart rendered no broker administrator Job")
	}
	if job.Name != brokerAdminJobName("acme") {
		t.Errorf("the Job is %q; dcctl looks for %q", job.Name, brokerAdminJobName("acme"))
	}
	if job.Namespace != InstanceNamespace("acme") {
		t.Errorf("the Job is in namespace %q, want the instance's %q", job.Namespace, InstanceNamespace("acme"))
	}
	if got := job.Annotations["helm.sh/hook-weight"]; got != "-5" {
		t.Errorf("hook-weight %q: the Job runs after its document (-10) and before everything else", got)
	}
	policy := job.Annotations["helm.sh/hook-delete-policy"]
	if !strings.Contains(policy, "before-hook-creation") || !strings.Contains(policy, "hook-succeeded") || strings.Contains(policy, "hook-failed") {
		t.Errorf("delete policy %q: it must be removed on success and KEPT on failure so its log can be read", policy)
	}
	if len(secrets) != 1 || secrets[0].Annotations["helm.sh/hook-weight"] != "-10" {
		t.Fatalf("hook Secrets %+v: want exactly the document copy, weight -10", secrets)
	}

	c := job.Spec.Template.Spec.Containers[0]
	if !strings.HasSuffix(c.Image, "/"+brokerAdminImageName+":v0.0.0-test") {
		t.Errorf("image %q is not {registry}/%s:{tag}", c.Image, brokerAdminImageName)
	}
	want, err := functionalarea.ResolveEnabled("full", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range c.Args {
		if list, ok := strings.CutPrefix(a, "--areas="); ok {
			got = strings.Split(list, ",")
		}
	}
	var wantNames []string
	for _, a := range want {
		wantNames = append(wantNames, string(a))
	}
	slices.Sort(got)
	slices.Sort(wantNames)
	if !slices.Equal(got, wantNames) {
		t.Errorf("the Job reconciles areas %v, but the full profile deploys %v", got, wantNames)
	}
	env := map[string]*corev1.SecretKeySelector{}
	for _, e := range c.Env {
		if e.ValueFrom != nil {
			env[e.Name] = e.ValueFrom.SecretKeyRef
		}
	}
	for name, key := range map[string]string{config.EnvNatsUser: "user", config.EnvNatsPassword: "password"} {
		ref := env[name]
		if ref == nil || ref.Name != natsLoginSecretName("acme", config.NatsAdminPrincipal) || ref.Key != key {
			t.Errorf("%s reads %+v; dcctl writes %s/%s", name, ref, natsLoginSecretName("acme", config.NatsAdminPrincipal), key)
		}
	}
	if got := job.Spec.Template.Spec.Volumes[0].Secret.SecretName; got != secrets[0].Name {
		t.Errorf("the Job mounts %q, not the document copy %q", got, secrets[0].Name)
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds >= int64(helmTimeout/time.Second) {
		t.Errorf("activeDeadlineSeconds %v must end the Job before Helm gives up on the hook (%s), or the failure arrives without a log",
			job.Spec.ActiveDeadlineSeconds, helmTimeout)
	}
}

// 🔴 THE ADMINISTRATOR'S SECRET IS THE ONE NO DEPLOYMENT MOUNTS. Every rendered object, hooks
// included, is searched for a reference to it: only the hook Job may name it. The control is that
// the Job does, so a render that lost the reference could not pass.
func TestOnlyTheBrokerAdminJobMountsTheAdministratorsLogin(t *testing.T) {
	rel := renderRelease(t, acmeValues(t, "full"))
	secret := `"` + natsLoginSecretName("acme", config.NatsAdminPrincipal) + `"`

	var holders []string
	check := func(manifest string) {
		var d map[string]interface{}
		if err := yaml.Unmarshal([]byte(manifest), &d); err != nil || d == nil {
			return
		}
		b, _ := json.Marshal(d)
		if strings.Contains(string(b), secret) {
			meta, _ := d["metadata"].(map[string]interface{})
			holders = append(holders, d["kind"].(string)+"/"+meta["name"].(string))
		}
	}
	for _, doc := range strings.Split(rel.Manifest, "\n---\n") {
		check(doc)
	}
	for _, h := range rel.Hooks {
		check(h.Manifest)
	}
	if len(holders) != 1 || holders[0] != "Job/"+brokerAdminJobName("acme") {
		t.Fatalf("the administrator's login is referenced by %v; only Job/%s may", holders, brokerAdminJobName("acme"))
	}
}

// With brokerAdmin.enabled=false the chart renders no hook at all.
func TestTheBrokerAdminHookCanBeSwitchedOff(t *testing.T) {
	vals := acmeValues(t, "default")
	vals["brokerAdmin"] = map[string]interface{}{"enabled": false}
	rel := renderRelease(t, vals)
	if len(rel.Hooks) != 0 {
		t.Fatalf("brokerAdmin.enabled=false still rendered %d hooks", len(rel.Hooks))
	}
}

// startManualReconcile runs runBrokerAdminJob against a fake cluster and returns the Job it
// created once it exists.
func startManualReconcile(t *testing.T, client *fake.Clientset, vals map[string]interface{}) (<-chan error, *batchv1.Job) {
	t.Helper()
	rel := renderRelease(t, vals)
	job, copies, err := brokerAdminHookObjects(rel, "acme")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runBrokerAdminJob(context.Background(), client, job, copies, &bytes.Buffer{}) }()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := client.BatchV1().Jobs(InstanceNamespace("acme")).List(context.Background(), metav1.ListOptions{})
		if len(list.Items) > 0 {
			return done, &list.Items[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no Job was created")
	return nil, nil
}

// A manual reconcile is built from the stored hook: the Job, renamed, without its hook
// annotations, with the document copy; and it leaves nothing behind when it succeeds.
func TestAManualReconcileIsBuiltFromTheStoredHookAndCleansUp(t *testing.T) {
	client := fake.NewClientset()
	done, created := startManualReconcile(t, client, acmeValues(t, "default"))
	if !strings.HasPrefix(created.Name, brokerAdminJobName("acme")+"-") {
		t.Errorf("the manual Job is named %q, which would collide with the hook's", created.Name)
	}
	for k := range created.Annotations {
		if strings.HasPrefix(k, "helm.sh/") {
			t.Errorf("the manual Job still carries the hook annotation %s, so Helm would own it", k)
		}
	}
	if created.Labels["devicechain.io/reconcile"] != "manual" {
		t.Error("the manual Job is not labelled as manual")
	}
	secrets, _ := client.CoreV1().Secrets(created.Namespace).List(context.Background(), metav1.ListOptions{})
	if len(secrets.Items) != 1 {
		t.Errorf("the Job's document copy was not written (%d Secrets)", len(secrets.Items))
	}

	created.Status.Succeeded = 1
	if _, err := client.BatchV1().Jobs(created.Namespace).UpdateStatus(context.Background(), created, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a succeeded Job was reported as %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the reconcile did not return after the Job succeeded")
	}
	left, _ := client.BatchV1().Jobs(created.Namespace).List(context.Background(), metav1.ListOptions{})
	if len(left.Items) != 0 {
		t.Error("a succeeded manual Job was left behind")
	}
	secrets, _ = client.CoreV1().Secrets(created.Namespace).List(context.Background(), metav1.ListOptions{})
	if len(secrets.Items) != 0 {
		t.Error("the copy of the instance document outlived the Job")
	}
}

// A failed administrator is an error that names the Job and where its log is, and the Job is kept.
func TestAFailedManualReconcileKeepsTheJobAndSaysSo(t *testing.T) {
	client := fake.NewClientset()
	done, created := startManualReconcile(t, client, acmeValues(t, "default"))
	created.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	if _, err := client.BatchV1().Jobs(created.Namespace).UpdateStatus(context.Background(), created, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "failed") || !strings.Contains(err.Error(), created.Name) ||
			!strings.Contains(err.Error(), "kubectl") {
			t.Fatalf("error %v does not name the failed Job and where to read it", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the reconcile did not return after the Job failed")
	}
	left, _ := client.BatchV1().Jobs(created.Namespace).List(context.Background(), metav1.ListOptions{})
	if len(left.Items) != 1 {
		t.Error("a failed Job must be left in place to be read")
	}
}

func TestAReleaseWithoutTheHookSaysHowToGetOne(t *testing.T) {
	vals := acmeValues(t, "default")
	vals["brokerAdmin"] = map[string]interface{}{"enabled": false}
	_, _, err := brokerAdminHookObjects(renderRelease(t, vals), "acme")
	if err == nil || !strings.Contains(err.Error(), "dcctl upgrade") {
		t.Fatalf("error %v does not say what to do", err)
	}
}

// Helm's own sentence for a failed hook is left alone unless it is about this Job, and the log is
// found by the Job's pod.
func TestAFailedHookIsOnlyRewrittenForTheBrokerAdminJob(t *testing.T) {
	if got := brokerAdminFailure(context.Background(), "", "acme", nil); got != nil {
		t.Fatalf("no error became %v", got)
	}
	other := errors.New("timed out waiting for the condition")
	if got := brokerAdminFailure(context.Background(), "", "acme", other); got.Error() != other.Error() {
		t.Fatalf("an unrelated failure was rewritten: %v", got)
	}
	cs := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "acme-broker-admin-x", Namespace: InstanceNamespace("acme"), Labels: map[string]string{"job-name": brokerAdminJobName("acme")},
	}})
	pods, _ := cs.CoreV1().Pods(InstanceNamespace("acme")).List(context.Background(), metav1.ListOptions{LabelSelector: "job-name=" + brokerAdminJobName("acme")})
	if len(pods.Items) != 1 {
		t.Fatal("the Job's pod is not found by the selector the log read uses")
	}
	if got := indent("a\nb\n"); got != "    a\n    b" {
		t.Errorf("indent = %q", got)
	}
}
