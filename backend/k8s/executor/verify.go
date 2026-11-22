// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	api "github.com/devicechain-io/dc-k8s/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SelfIdentity struct{ PodName, PodNamespace string }
type Verification struct {
	Ready                          bool
	Reason, Message                string
	ObservedImage, ObservedImageID string
	Capability                     *api.ExecutorCapability
}

// reader must be the manager's uncached APIReader: get-only Pod authority cannot start an informer.
func VerifySelf(ctx context.Context, reader client.Reader, enrollment *api.ExecutorEnrollment, self SelfIdentity) Verification {
	result := Verification{Reason: api.ExecutorReasonNotObserved, Message: "Executor Pod has not been observed."}
	if enrollment == nil {
		return result
	}
	if enrollment.Spec.ProtocolVersion > CompiledProtocolVersion {
		result.Reason = api.ExecutorReasonProtocolUnsupported
		result.Message = "Enrollment protocol is newer than this executor."
		return result
	}
	if self.PodName == "" || self.PodNamespace == "" {
		return result
	}
	var pod corev1.Pod
	if err := reader.Get(ctx, client.ObjectKey{Name: self.PodName, Namespace: self.PodNamespace}, &pod); err != nil {
		return result
	}
	for _, c := range pod.Spec.Containers {
		if c.Name == "manager" {
			result.ObservedImage = c.Image
			break
		}
	}
	for _, c := range pod.Status.ContainerStatuses {
		if c.Name == "manager" {
			result.ObservedImageID = c.ImageID
			break
		}
	}
	if result.ObservedImage == "" {
		return result
	}
	if result.ObservedImage != enrollment.Spec.ExecutorImage {
		result.Reason = api.ExecutorReasonImageMismatch
		result.Message = "Executor Pod spec image differs from the enrolled image."
		return result
	}
	// A runtime observation is required, but its platform digest is never compared to the index pin.
	if result.ObservedImageID == "" {
		return result
	}
	result.Ready = true
	result.Reason = api.ExecutorReasonEnrolled
	result.Message = "Executor image verified; Preview executes no operations."
	result.Capability = &api.ExecutorCapability{ProtocolVersion: CompiledProtocolVersion, ExecutableActions: ExecutableActions()}
	return result
}
