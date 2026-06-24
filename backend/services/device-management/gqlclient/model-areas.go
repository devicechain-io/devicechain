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

// Area type entity.
type IAreaType interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
}

// Area entity.
type IArea interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
	GetAreaType() DefaultAreaAreaType
}

// Area relationship type entity.
type IAreaRelationshipType interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
}

// Area relationship entity.
type IAreaRelationship interface {
	IModel
	ITokenReference
	IMetadataEntity
	GetSourceArea() DefaultAreaRelationshipSourceArea
	GetTargets() DefaultAreaRelationshipTargetsEntityRelationshipTargets
	GetRelationshipType() DefaultAreaRelationshipRelationshipTypeAreaRelationshipType
}

// Area group entity.
type IAreaGroup interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
}

// Area group relationship type entity.
type IAreaGroupRelationshipType interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
}

// Area group relationship entity.
type IAreaGroupRelationship interface {
	IModel
	ITokenReference
	IMetadataEntity
	GetSourceAreaGroup() DefaultAreaGroupRelationshipSourceAreaGroup
	GetTargets() DefaultAreaGroupRelationshipTargetsEntityRelationshipTargets
	GetRelationshipType() DefaultAreaGroupRelationshipRelationshipTypeAreaGroupRelationshipType
}
