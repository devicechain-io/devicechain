// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

// ShardGroupsSpec opts the ingest streams into per-tenant group routing. Enable
// only after all service pods understand shardGroups. Broker placement uses the
// corresponding dc-nats pod name; absent placementTags are derived by the chart.
// +kubebuilder:validation:XValidation:rule="!has(self.placementTags) || self.placementTags.all(k, (k == '0' && self.count > 0 && self.placementTags[k] == ['dc-nats-0']) || (k == '1' && self.count > 1 && self.placementTags[k] == ['dc-nats-1']) || (k == '2' && self.count > 2 && self.placementTags[k] == ['dc-nats-2']) || (k == '3' && self.count > 3 && self.placementTags[k] == ['dc-nats-3']) || (k == '4' && self.count > 4 && self.placementTags[k] == ['dc-nats-4']))",message="placementTags must select the corresponding dc-nats pod of a configured group"
type ShardGroupsSpec struct {
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:validation:Maximum=5
	Count int `json:"count"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2
	// +listType=set
	// +kubebuilder:validation:items:Enum=inbound-events;resolved-events
	Suffixes []string `json:"suffixes"`
	// +kubebuilder:validation:MaxProperties=5
	// +optional
	PlacementTags map[string]ShardPlacementTags `json:"placementTags,omitempty"`
}

// ShardPlacementTags selects one broker server.
// +kubebuilder:validation:MaxItems=1
// +kubebuilder:validation:items:MaxLength=9
type ShardPlacementTags []string
