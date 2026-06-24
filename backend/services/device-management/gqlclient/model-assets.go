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

// Asset type entity.
type IAssetType interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
}

// Asset entity.
type IAsset interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
	GetAssetType() DefaultAssetAssetType
}

// Asset relationship type entity.
type IAssetRelationshipType interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
}

// Asset relationship entity.
type IAssetRelationship interface {
	IModel
	ITokenReference
	IMetadataEntity
	GetSourceAsset() DefaultAssetRelationshipSourceAsset
	GetTargets() DefaultAssetRelationshipTargetsEntityRelationshipTargets
	GetRelationshipType() DefaultAssetRelationshipRelationshipTypeAssetRelationshipType
}

// Asset group entity.
type IAssetGroup interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
}

// Asset group relationship type entity.
type IAssetGroupRelationshipType interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
}

// Asset group relationship entity.
type IAssetGroupRelationship interface {
	IModel
	ITokenReference
	IMetadataEntity
	GetSourceAssetGroup() DefaultAssetGroupRelationshipSourceAssetGroup
	GetTargets() DefaultAssetGroupRelationshipTargetsEntityRelationshipTargets
	GetRelationshipType() DefaultAssetGroupRelationshipRelationshipTypeAssetGroupRelationshipType
}
