// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	api "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-k8s/executor"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"
)

func enrollmentFixture() *api.ExecutorEnrollment {
	return &api.ExecutorEnrollment{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Spec: api.ExecutorEnrollmentSpec{Owner: "owner", ExecutorImage: "registry/operator@sha256:" + strings.Repeat("a", 64), EscrowDelivery: "OwnerOffline", ProtocolVersion: 1}}
}
func operationFixture(name string) *api.InstanceOperation {
	return &api.InstanceOperation{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: api.InstanceOperationSpec{Instance: "executor-target", InstanceUID: "target", EnrollmentUID: "enrollment", Action: api.ActionUpgrade, ProtocolVersion: 1}}
}
func operationWrites() float64 {
	families, err := metrics.Registry.Gather()
	Expect(err).NotTo(HaveOccurred())
	var count float64
	for _, f := range families {
		if f.GetName() != "dc_executor_status_writes_total" {
			continue
		}
		for _, m := range f.Metric {
			for _, label := range m.Label {
				if label.GetName() == "kind" && label.GetValue() == "InstanceOperation" {
					count += m.GetCounter().GetValue()
				}
			}
		}
	}
	return count
}

var _ = Describe("Executor admission and inert reconciliation", func() {
	It("admits Install only at cluster scope and requires targets for other actions", func(ctx SpecContext) {
		for _, action := range []string{"Install", "Bootstrap", "Upgrade", "Teardown"} {
			for _, instance := range []bool{false, true} {
				for _, uid := range []bool{false, true} {
					spec := map[string]interface{}{"action": action, "enrollmentUID": "enrollment", "protocolVersion": int64(1)}
					if instance {
						spec["instance"] = "prod"
					}
					if uid {
						spec["instanceUID"] = "target"
					}
					op := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": api.GroupVersion.String(), "kind": "InstanceOperation", "metadata": map[string]interface{}{"name": fmt.Sprintf("shape-%s-%t-%t", strings.ToLower(action), instance, uid)}, "spec": spec}}
					valid := instance && (uid || action == "Bootstrap")
					if action == "Install" {
						valid = !instance && !uid
					}
					err := k8sClient.Create(ctx, op)
					if valid {
						Expect(err).NotTo(HaveOccurred())
						Expect(k8sClient.Delete(ctx, op)).To(Succeed())
					} else {
						Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%s instance=%t uid=%t: %v", action, instance, uid, err)
					}
				}
			}
		}
	})
	It("bounds UIDs, image and verdict strings and validates capability items", func(ctx SpecContext) {
		for _, field := range []string{"instanceUID", "enrollmentUID"} {
			for _, size := range []int{36, 37} {
				op := operationFixture(fmt.Sprintf("bounds-%s-%d", strings.ToLower(field), size))
				if field == "instanceUID" {
					op.Spec.InstanceUID = strings.Repeat("a", size)
				} else {
					op.Spec.EnrollmentUID = strings.Repeat("a", size)
				}
				err := k8sClient.Create(ctx, op)
				if size == 36 {
					Expect(err).NotTo(HaveOccurred())
					Expect(k8sClient.Delete(ctx, op)).To(Succeed())
				} else {
					Expect(apierrors.IsInvalid(err)).To(BeTrue())
				}
			}
		}
		for _, size := range []int{512, 513} {
			e := enrollmentFixture()
			e.Spec.ExecutorImage = strings.Repeat("r", size-72) + "@sha256:" + strings.Repeat("a", 64)
			err := k8sClient.Create(ctx, e)
			if size == 512 {
				Expect(err).NotTo(HaveOccurred())
				Expect(k8sClient.Delete(ctx, e)).To(Succeed())
			} else {
				Expect(apierrors.IsInvalid(err)).To(BeTrue())
			}
		}
		op := operationFixture("verdict-bounds")
		Expect(k8sClient.Create(ctx, op)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), op)).To(Succeed()) })
		for _, tc := range []struct {
			reason, message string
			valid           bool
		}{
			{strings.Repeat("A", 128), strings.Repeat("m", 32768), true},
			{strings.Repeat("A", 129), "Preview", false},
			{"NotExecutableInThisBuild", strings.Repeat("m", 32769), false},
			{"lowercase", "Preview", false}, {"Has-Hyphen", "Preview", false},
		} {
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: op.Name}, op)).To(Succeed())
			op.Status = api.InstanceOperationStatus{Phase: api.OperationPending, Reason: tc.reason, Message: tc.message}
			err := k8sClient.Status().Update(ctx, op)
			if tc.valid {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(apierrors.IsInvalid(err)).To(BeTrue())
			}
		}
		e := enrollmentFixture()
		Expect(k8sClient.Create(ctx, e)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), e)).To(Succeed()) })
		for _, action := range []string{"Install", "Bootstrap", "Upgrade", "Teardown", "Future"} {
			raw := &unstructured.Unstructured{}
			raw.SetGroupVersionKind(api.GroupVersion.WithKind("ExecutorEnrollment"))
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "cluster"}, raw)).To(Succeed())
			raw.Object["status"] = map[string]interface{}{"capability": map[string]interface{}{"protocolVersion": int64(1), "executableActions": []interface{}{action}}}
			err := k8sClient.Status().Update(ctx, raw)
			if action == "Future" {
				Expect(apierrors.IsInvalid(err)).To(BeTrue())
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		}
	})
	It("enforces singleton, digest, immutable enrollment and operation specs", func(ctx SpecContext) {
		e := enrollmentFixture()
		e.Name = "other"
		Expect(apierrors.IsInvalid(k8sClient.Create(ctx, e))).To(BeTrue())
		e = enrollmentFixture()
		e.Spec.ExecutorImage = "operator:latest"
		Expect(apierrors.IsInvalid(k8sClient.Create(ctx, e))).To(BeTrue())
		e = enrollmentFixture()
		Expect(k8sClient.Create(ctx, e)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), e)).To(Succeed()) })
		for _, field := range []string{"owner", "escrow"} {
			live := &api.ExecutorEnrollment{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: e.Name}, live)).To(Succeed())
			if field == "owner" {
				live.Spec.Owner = "other"
			} else {
				live.Spec.EscrowDelivery = "Other"
			}
			Expect(apierrors.IsInvalid(k8sClient.Update(ctx, live))).To(BeTrue())
		}
		live := &api.ExecutorEnrollment{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: e.Name}, live)).To(Succeed())
		live.Spec.ExecutorImage = "other@sha256:" + strings.Repeat("b", 64)
		Expect(k8sClient.Update(ctx, live)).To(Succeed())
		op := operationFixture("immutable-operation")
		Expect(k8sClient.Create(ctx, op)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), op)).To(Succeed()) })
		op.Spec.ImageVersion = "v2"
		Expect(apierrors.IsInvalid(k8sClient.Update(ctx, op))).To(BeTrue())
		for _, action := range []api.OperationAction{api.ActionInstall, api.ActionUpgrade, api.ActionTeardown, api.ActionBootstrap} {
			missing := operationFixture("missing-uid-" + strings.ToLower(string(action)))
			missing.Spec.Action = action
			missing.Spec.InstanceUID = ""
			err := k8sClient.Create(ctx, missing)
			if action == api.ActionBootstrap {
				Expect(err).NotTo(HaveOccurred())
				Expect(k8sClient.Delete(ctx, missing)).To(Succeed())
			} else {
				Expect(apierrors.IsInvalid(err)).To(BeTrue())
			}
		}
	})
	It("protects terminal verdicts through the status subresource", func(ctx SpecContext) {
		for _, phase := range []api.OperationPhase{api.OperationRefused, api.OperationSucceeded, api.OperationFailed} {
			op := operationFixture("terminal-" + strings.ToLower(string(phase)))
			Expect(k8sClient.Create(ctx, op)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), op)).To(Succeed()) })
			now := metav1.Now()
			op.Status = api.InstanceOperationStatus{Phase: phase, Reason: "Original", Message: "Preview", DecidedAt: &now}
			Expect(k8sClient.Status().Update(ctx, op)).To(Succeed())
			for _, mutation := range []string{"status", "phase", "reason", "message", "decidedAt", "pending"} {
				raw := &unstructured.Unstructured{}
				raw.SetGroupVersionKind(api.GroupVersion.WithKind("InstanceOperation"))
				Expect(k8sClient.Get(ctx, client.ObjectKey{Name: op.Name}, raw)).To(Succeed())
				switch mutation {
				case "status":
					delete(raw.Object, "status")
				case "phase":
					unstructured.RemoveNestedField(raw.Object, "status", "phase")
				case "pending":
					Expect(unstructured.SetNestedField(raw.Object, "Pending", "status", "phase")).To(Succeed())
				case "decidedAt":
					Expect(unstructured.SetNestedField(raw.Object, "2027-01-01T00:00:00Z", "status", "decidedAt")).To(Succeed())
				default:
					Expect(unstructured.SetNestedField(raw.Object, "Changed", "status", mutation)).To(Succeed())
				}
				Expect(apierrors.IsInvalid(k8sClient.Status().Update(ctx, raw))).To(BeTrue(), "mutation %s", mutation)
			}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: op.Name}, op)).To(Succeed())
			op.Status.Conditions = []metav1.Condition{{Type: "Decided", Status: metav1.ConditionTrue, Reason: "Original", Message: "Preview", LastTransitionTime: metav1.NewTime(time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC))}}
			Expect(k8sClient.Status().Update(ctx, op)).To(Succeed())
			op.Status.Conditions[0].LastTransitionTime = metav1.NewTime(time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC))
			Expect(k8sClient.Status().Update(ctx, op)).To(Succeed())
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: op.Name}, op)).To(Succeed())
			Expect(op.Status.Conditions).To(HaveLen(1))
			Expect(op.Status.Conditions[0].LastTransitionTime.Time.Day()).To(Equal(22))
		}
		op := operationFixture("status-without-phase")
		Expect(k8sClient.Create(ctx, op)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), op)).To(Succeed()) })
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(api.GroupVersion.WithKind("InstanceOperation"))
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: op.Name}, raw)).To(Succeed())
		raw.Object["status"] = map[string]interface{}{"reason": "Seen"}
		Expect(apierrors.IsInvalid(k8sClient.Status().Update(ctx, raw))).To(BeTrue())
	})
	It("verifies its Pod spec, refuses once, and leaves mismatched requests untouched", func(ctx SpecContext) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "executor-test"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), ns)).To(Succeed()) })
		enrollment := enrollmentFixture()
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "executor", Namespace: ns.Name}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Image: enrollment.Spec.ExecutorImage}}}}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), pod)).To(Succeed()) })
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "manager", Image: enrollment.Spec.ExecutorImage, ImageID: "containerd://platform-manifest", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		Expect(k8sClient.Create(ctx, enrollment)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), enrollment)).To(Succeed()) })
		instance := &api.Instance{ObjectMeta: metav1.ObjectMeta{Name: "executor-target"}, Spec: api.InstanceSpec{Provider: "local", Cluster: "kind-devicechain", Managed: true, Profile: "default"}}
		Expect(k8sClient.Create(ctx, instance)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), instance)).To(Succeed()) })
		mgr, err := ctrl.NewManager(testEnv.Config, ctrl.Options{Scheme: scheme.Scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
		Expect(err).NotTo(HaveOccurred())
		Expect(executor.SetupExecutor(mgr, executor.Options{Self: executor.SelfIdentity{PodName: pod.Name, PodNamespace: ns.Name}})).To(Succeed())
		runCtx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- mgr.Start(runCtx) }()
		DeferCleanup(func() { cancel(); Eventually(done, 10*time.Second).Should(Receive(BeNil())) })
		Eventually(func() bool {
			live := &api.ExecutorEnrollment{}
			if k8sClient.Get(ctx, client.ObjectKey{Name: "cluster"}, live) != nil {
				return false
			}
			return meta.IsStatusConditionTrue(live.Status.Conditions, "Ready") && live.Status.Capability != nil && live.Status.Capability.ProtocolVersion == 1 && live.Status.Capability.ExecutableActions != nil && len(live.Status.Capability.ExecutableActions) == 0 && live.Status.ObservedImage == enrollment.Spec.ExecutorImage
		}, 15*time.Second).Should(BeTrue())
		makeOp := func(name, uid string) *api.InstanceOperation {
			op := operationFixture(name)
			op.Spec.EnrollmentUID = string(enrollment.UID)
			op.Spec.InstanceUID = uid
			Expect(k8sClient.Create(ctx, op)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), op)).To(Succeed()) })
			return op
		}
		op := makeOp("preview-operation", string(instance.UID))
		Eventually(func() string {
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: op.Name}, op)).To(Succeed())
			return op.Status.Reason
		}, 15*time.Second).Should(Equal("NotExecutableInThisBuild"))
		Expect(op.Status.Phase).To(Equal(api.OperationRefused))
		condition := meta.FindStatusCondition(op.Status.Conditions, "Decided")
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		Expect(condition.Reason).To(Equal(op.Status.Reason))
		rv := op.ResourceVersion
		Eventually(operationWrites, 5*time.Second).Should(BeNumerically(">=", 1))
		writes := operationWrites()
		Expect(writes).To(BeNumerically(">=", 1))
		Consistently(func() string {
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: op.Name}, op)).To(Succeed())
			Expect(operationWrites()).To(Equal(writes))
			return op.ResourceVersion
		}, 3*time.Second, 100*time.Millisecond).Should(Equal(rv))
		stale := makeOp("stale-target-operation", "old-target")
		Eventually(func() string {
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: stale.Name}, stale)).To(Succeed())
			return stale.Status.Reason
		}, 10*time.Second).Should(Equal("StaleTarget"))
		install := operationFixture("cluster-install-operation")
		install.Spec.Action, install.Spec.Instance, install.Spec.InstanceUID = api.ActionInstall, "", ""
		install.Spec.EnrollmentUID = string(enrollment.UID)
		Expect(k8sClient.Create(ctx, install)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), install)).To(Succeed()) })
		Eventually(func() string {
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: install.Name}, install)).To(Succeed())
			return install.Status.Reason
		}, 10*time.Second).Should(Equal("NotExecutableInThisBuild"))
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "cluster"}, enrollment)).To(Succeed())
		enrollment.Spec.ExecutorImage = "other@sha256:" + strings.Repeat("b", 64)
		Expect(k8sClient.Update(ctx, enrollment)).To(Succeed())
		Eventually(func() bool {
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "cluster"}, enrollment)).To(Succeed())
			c := meta.FindStatusCondition(enrollment.Status.Conditions, "Ready")
			return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "ImageMismatch" && enrollment.Status.Capability == nil
		}, 10*time.Second).Should(BeTrue())
		pending := makeOp("mismatch-pending-operation", string(instance.UID))
		Consistently(func() api.OperationPhase {
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: pending.Name}, pending)).To(Succeed())
			return pending.Status.Phase
		}, 3*time.Second, 100*time.Millisecond).Should(BeEmpty())
		unchanged := &api.Instance{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: instance.Name}, unchanged)).To(Succeed())
		Expect(unchanged.ResourceVersion).To(Equal(instance.ResourceVersion))
	})
	It("grants requester CR creation and denies every workload, secret and privilege write", func(ctx SpecContext) {
		raw, err := os.ReadFile("../config/executor/requester-role.yaml")
		Expect(err).NotTo(HaveOccurred())
		role := &rbacv1.ClusterRole{}
		Expect(yaml.Unmarshal(raw, role)).To(Succeed())
		Expect(k8sClient.Create(ctx, role)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), role)).To(Succeed()) })
		binding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "executor-requester-test"}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name}, Subjects: []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: "executor-requester-test"}}}
		Expect(k8sClient.Create(ctx, binding)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), binding)).To(Succeed()) })
		cfg := rest.CopyConfig(testEnv.Config)
		cfg.Impersonate = rest.ImpersonationConfig{UserName: "executor-requester-test", Groups: []string{"system:authenticated"}}
		limited, err := kubernetes.NewForConfig(cfg)
		Expect(err).NotTo(HaveOccurred())
		allowed := func(verb, group, resource, subresource string) bool {
			review, err := limited.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{Verb: verb, Group: group, Resource: resource, Subresource: subresource, Namespace: "default"}}}, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			return review.Status.Allowed
		}
		Eventually(func() bool { return allowed("create", "core.devicechain.io", "instanceoperations", "") }, 10*time.Second).Should(BeTrue())
		for _, check := range [][4]string{{"get", "", "secrets", ""}, {"create", "", "pods", ""}, {"create", "batch", "jobs", ""}, {"create", "", "pods", "exec"}, {"create", "rbac.authorization.k8s.io", "rolebindings", ""}, {"patch", "core.devicechain.io", "instances", ""}, {"update", "core.devicechain.io", "executorenrollments", ""}, {"update", "core.devicechain.io", "instanceoperations", "status"}} {
			Expect(allowed(check[0], check[1], check[2], check[3])).To(BeFalse(), "unexpected grant: %v", check)
		}
		requester, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		op := operationFixture("requester-positive-control")
		Expect(requester.Create(ctx, op)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), op)).To(Succeed()) })
		_, err = limited.CoreV1().Secrets("default").Get(ctx, "anything", metav1.GetOptions{})
		Expect(apierrors.IsForbidden(err)).To(BeTrue())
	})
})
