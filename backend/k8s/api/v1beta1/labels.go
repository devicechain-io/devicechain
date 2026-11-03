// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

// The labels that say a NAMESPACE is DeviceChain's. dcctl writes them, and the
// operator reads them before it acts on anything it found by listing the whole
// cluster: a namespace is cluster-scoped, so labelling one takes rights a user who
// only edits objects inside it does not have. That is what makes the check worth
// something where a label on the object itself, which anyone who can edit it can
// copy, would not be.
const (
	// InstanceNamespaceLabel is on every instance's namespace, with the instance id
	// as its value.
	InstanceNamespaceLabel = "devicechain.io/instance"

	// InstanceNamespacePrefix is what an instance's namespace name starts with; the rest is
	// the instance id. It is spelled HERE and nowhere else: dcctl's InstanceNamespace
	// delegates to InstanceNamespace below and the operator calls it, and the chart's
	// devicechain.instanceNamespace is held equal by a dcctl test that renders it.
	InstanceNamespacePrefix = "dci-"

	// ComponentLabel, set to InfrastructureComponent, is on the cluster's shared
	// namespace (dc-system): the one the OpenTofu cluster configuration stamps, and
	// dcctl creates ahead of it with the same labels.
	ComponentLabel          = "devicechain.io/component"
	InfrastructureComponent = "infrastructure"
)

// InstanceNamespace is the namespace an instance's workloads live in.
func InstanceNamespace(instance string) string { return InstanceNamespacePrefix + instance }
