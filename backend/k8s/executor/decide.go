// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package executor

import api "github.com/devicechain-io/dc-k8s/api/v1beta1"

type DecideInput struct {
	Enrollment   *api.ExecutorEnrollment
	Verification Verification
	Operation    *api.InstanceOperation
	Instance     *api.Instance
	Registry     Registry
}

type Verdict struct {
	Write         bool
	Phase         api.OperationPhase
	Reason        string
	Message       string
	SkippedReason string
}

func Decide(in DecideInput) Verdict {
	op := in.Operation
	if Terminal(op.Status.Phase) {
		return Verdict{}
	}
	if op.Spec.ProtocolVersion > CompiledProtocolVersion {
		return Verdict{SkippedReason: "protocol"}
	}
	switch op.Spec.Action {
	case api.ActionInstall, api.ActionBootstrap, api.ActionUpgrade, api.ActionTeardown:
	default:
		return Verdict{SkippedReason: "action"}
	}
	if in.Enrollment == nil || !in.Verification.Ready || in.Enrollment.Status.ObservedGeneration != in.Enrollment.Generation {
		return Verdict{}
	}
	refuse := func(reason, message string) Verdict {
		return Verdict{Write: true, Phase: api.OperationRefused, Reason: reason, Message: message}
	}
	if op.Spec.EnrollmentUID != string(in.Enrollment.UID) {
		return refuse("StaleEnrollment", "Enrollment identity changed; submit a new operation.")
	}
	if op.Spec.Action == api.ActionBootstrap {
		if in.Instance != nil {
			return refuse("InstanceExists", "An Instance with this name already exists.")
		}
	} else if op.Spec.Action != api.ActionInstall {
		if in.Instance == nil {
			return refuse("InstanceNotFound", "The target Instance does not exist.")
		}
		if op.Spec.InstanceUID != string(in.Instance.UID) {
			return refuse("StaleTarget", "Instance identity changed; submit a new operation.")
		}
	}
	if _, ok := in.Registry[op.Spec.Action]; !ok {
		return refuse("NotExecutableInThisBuild", "The action is recognised, but this Preview build executes no operations. See the release notes.")
	}
	return Verdict{Write: true, Phase: api.OperationRunning, Reason: "Executable"}
}
