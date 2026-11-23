// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package v1 contains the core.devicechain.io/v1 Instance API. Its schema is
// STRUCTURALLY IDENTICAL to v1beta1's and must stay so for as long as both are
// served: the API server prunes an object to the schema of the version it is read
// or written through, so a field present in one version and absent in the other
// would be silently dropped by any write through the other, even a status patch.
// A field is therefore added to BOTH versions in the same release, or to neither.
// TestV1AndV1beta1SchemasAreIdentical pins this against the generated CRD.
//
// v1 is served beside v1beta1 and is a preview until the storage version moves;
// storage stays v1beta1 and there is no conversion webhook.
// +kubebuilder:object:generate=true
// +groupName=core.devicechain.io
package v1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "core.devicechain.io", Version: "v1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
