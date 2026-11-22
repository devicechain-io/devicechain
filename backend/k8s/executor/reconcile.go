// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"os"
	"time"

	api "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/prometheus/client_golang/prometheus"
	equality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const reverifyInterval = 60 * time.Second

var (
	decisions    = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "dc_executor_decisions_total", Help: "Executor decisions by verdict and reason."}, []string{"verdict", "reason"})
	skipped      = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "dc_executor_operations_skipped_total", Help: "Operations left to a newer executor."}, []string{"reason"})
	statusWrites = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "dc_executor_status_writes_total", Help: "Status update attempts by kind and result."}, []string{"kind", "result"})
)

func init() { metrics.Registry.MustRegister(decisions, skipped, statusWrites) }

type Options struct{ Self SelfIdentity }
type EnrollmentReconciler struct {
	client.Client
	Reader client.Reader
	Self   SelfIdentity
}
type OperationReconciler struct {
	client.Client
	Reader client.Reader
	Self   SelfIdentity
}

func updateStatus(ctx context.Context, c client.Client, obj client.Object) (ctrl.Result, error) {
	err := c.Status().Update(ctx, obj)
	result := "success"
	if apierrors.IsConflict(err) {
		result = "conflict"
	} else if err != nil {
		result = "error"
	}
	kind := "InstanceOperation"
	if _, ok := obj.(*api.ExecutorEnrollment); ok {
		kind = "ExecutorEnrollment"
	}
	statusWrites.WithLabelValues(kind, result).Inc()
	if apierrors.IsConflict(err) {
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, client.IgnoreNotFound(err)
}

func (r *EnrollmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var enrollment api.ExecutorEnrollment
	if err := r.Get(ctx, req.NamespacedName, &enrollment); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	verification := VerifySelf(ctx, r.Reader, &enrollment, r.Self)
	before := enrollment.Status.DeepCopy()
	enrollment.Status.ObservedGeneration = enrollment.Generation
	enrollment.Status.ObservedImage = verification.ObservedImage
	enrollment.Status.ObservedImageID = verification.ObservedImageID
	enrollment.Status.Capability = verification.Capability
	condition := metav1.Condition{Type: api.ExecutorConditionReady, Status: metav1.ConditionFalse, Reason: verification.Reason, Message: verification.Message, ObservedGeneration: enrollment.Generation}
	if verification.Ready {
		condition.Status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&enrollment.Status.Conditions, condition)
	if !equality.Semantic.DeepEqual(*before, enrollment.Status) {
		result, err := updateStatus(ctx, r.Client, &enrollment)
		if err != nil || result.Requeue {
			return result, err
		}
	}
	return ctrl.Result{RequeueAfter: reverifyInterval}, nil
}

func (r *OperationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var operation api.InstanceOperation
	if err := r.Get(ctx, req.NamespacedName, &operation); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if Terminal(operation.Status.Phase) {
		return ctrl.Result{}, nil
	}
	// Decide rejects future requests before any enrollment or target can influence them.
	early := Decide(DecideInput{Operation: &operation})
	if early.SkippedReason != "" {
		skipped.WithLabelValues(early.SkippedReason).Inc()
		return ctrl.Result{}, nil
	}
	var enrollment api.ExecutorEnrollment
	if err := r.Get(ctx, client.ObjectKey{Name: "cluster"}, &enrollment); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: reverifyInterval}, nil
		}
		return ctrl.Result{}, err
	}
	verification := VerifySelf(ctx, r.Reader, &enrollment, r.Self)
	var instance *api.Instance
	if operation.Spec.Action != api.ActionInstall && verification.Ready && enrollment.Status.ObservedGeneration == enrollment.Generation {
		instance = &api.Instance{}
		if err := r.Get(ctx, client.ObjectKey{Name: operation.Spec.Instance}, instance); err != nil {
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			instance = nil
		}
	}
	verdict := Decide(DecideInput{Enrollment: &enrollment, Verification: verification, Operation: &operation, Instance: instance, Registry: executable})
	if !verdict.Write {
		return ctrl.Result{RequeueAfter: reverifyInterval}, nil
	}
	decisions.WithLabelValues(string(verdict.Phase), verdict.Reason).Inc()
	now := metav1.Now()
	operation.Status = api.InstanceOperationStatus{Phase: verdict.Phase, Reason: verdict.Reason, Message: verdict.Message, DecidedAt: &now, ObservedGeneration: operation.Generation, ExecutorProtocolVersion: CompiledProtocolVersion}
	if Terminal(verdict.Phase) {
		meta.SetStatusCondition(&operation.Status.Conditions, metav1.Condition{
			Type: api.OperationConditionDecided, Status: metav1.ConditionTrue,
			Reason: verdict.Reason, Message: verdict.Message,
			ObservedGeneration: operation.Generation, LastTransitionTime: now,
		})
	}
	return updateStatus(ctx, r.Client, &operation)
}

func (r *OperationReconciler) enrollmentOperations(ctx context.Context, _ client.Object) []reconcile.Request {
	var operations api.InstanceOperationList
	if err := r.List(ctx, &operations); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "listing pending operations")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(operations.Items))
	for _, op := range operations.Items {
		if !Terminal(op.Status.Phase) {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Name: op.Name}})
		}
	}
	return requests
}

func SetupExecutor(mgr ctrl.Manager, opts Options) error {
	if opts.Self.PodName == "" {
		opts.Self.PodName = os.Getenv("POD_NAME")
	}
	if opts.Self.PodNamespace == "" {
		opts.Self.PodNamespace = os.Getenv("POD_NAMESPACE")
	}
	enrollment := &EnrollmentReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Self: opts.Self}
	if err := ctrl.NewControllerManagedBy(mgr).Named("executor-enrollment").For(&api.ExecutorEnrollment{}).Complete(enrollment); err != nil {
		return err
	}
	operation := &OperationReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Self: opts.Self}
	return ctrl.NewControllerManagedBy(mgr).Named("executor-operation").For(&api.InstanceOperation{}).
		Watches(&api.ExecutorEnrollment{}, handler.EnqueueRequestsFromMapFunc(operation.enrollmentOperations)).Complete(operation)
}
