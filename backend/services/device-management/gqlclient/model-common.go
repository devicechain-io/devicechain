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

// Base model fields.
type IModel interface {
	GetId() string
	GetCreatedAt() *string
	GetUpdatedAt() *string
	GetDeletedAt() *string
}

// Entity that may be referenced by token.
type ITokenReference interface {
	GetToken() string
}

// Entity with name and description.
type INamedEntity interface {
	GetName() *string
	GetDescription() *string
}

// Information for branded entities.
type IBrandedEntity interface {
	GetImageUrl() *string
	GetIcon() *string
	GetBackgroundColor() *string
	GetForegroundColor() *string
	GetBorderColor() *string
}

// Entity with attached metadata.
type IMetadataEntity interface {
	GetMetadata() *string
}
