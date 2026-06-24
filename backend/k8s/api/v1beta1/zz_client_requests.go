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

package v1beta1

// ------------------
// Instance Mangement
// ------------------

// Information required to create a DeviceChain instance.
type InstanceCreateRequest struct {
	Id              string
	Name            string
	Description     string
	ConfigurationId string
}

// Information required to get a DeviceChain instance.
type InstanceGetRequest struct {
	Id string
}

// ------------------
// Tenant Mangement
// ------------------

// Information required to create a DeviceChain tenant.
type TenantCreateRequest struct {
	InstanceId  string
	TenantId    string
	Name        string
	Description string
}

// Information required to get a tenant.
type TenantGetRequest struct {
	InstanceId string
	TenantId   string
}

// ----------------------------------
// Microservice Configuration Catalog
// ----------------------------------

// Information required to get a microservice configuration.
type MicroserviceConfigurationGetRequest struct {
	Id string
}
