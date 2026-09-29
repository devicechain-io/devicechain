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

	// ComponentLabel, set to InfrastructureComponent, is on the cluster's shared
	// namespace (dc-system): the one the OpenTofu cluster configuration stamps, and
	// dcctl creates ahead of it with the same labels.
	ComponentLabel          = "devicechain.io/component"
	InfrastructureComponent = "infrastructure"
)
