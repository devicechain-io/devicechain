/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package gqlclient

// Device type entity.
type IDeviceType interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
}

// Device entity.
type IDevice interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
	GetDeviceType() DefaultDeviceDeviceType
}

// Device relationship type entity.
type IDeviceRelationshipType interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
}

// Device relationship entity.
type IDeviceRelationship interface {
	IModel
	ITokenReference
	IMetadataEntity
	GetSourceDevice() DefaultDeviceRelationshipSourceDevice
	GetTargets() DefaultDeviceRelationshipTargetsEntityRelationshipTargets
	GetRelationshipType() DefaultDeviceRelationshipRelationshipTypeDeviceRelationshipType
}

// Device group entity.
type IDeviceGroup interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
}

// Device group relationship type entity.
type IDeviceGroupRelationshipType interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
}

// Device group relationship entity.
type IDeviceGroupRelationship interface {
	IModel
	ITokenReference
	IMetadataEntity
	GetSourceDeviceGroup() DefaultDeviceGroupRelationshipSourceDeviceGroup
	GetTargets() DefaultDeviceGroupRelationshipTargetsEntityRelationshipTargets
	GetRelationshipType() DefaultDeviceGroupRelationshipRelationshipTypeDeviceGroupRelationshipType
}
