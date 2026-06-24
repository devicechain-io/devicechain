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

package generator

import (
	"fmt"

	gen "github.com/devicechain-io/dc-k8s/generators"
	"github.com/devicechain-io/dc-user-management/config"
)

type ResourceProvider struct{}

// Get instance configuration CRs that should be created in tooling
func (ResourceProvider) GetConfigurationResources() ([]gen.ConfigurationResource, error) {
	resources := make([]gen.ConfigurationResource, 0)

	name := "user-management-default"
	msconfig := config.NewUserManagementConfiguration()
	content, err := gen.GenerateMicroserviceConfig(name, "user-management", "devicechain-io/user-management:0.0.1", msconfig)
	if err != nil {
		return nil, err
	}
	dcmdefault := gen.ConfigurationResource{
		Name:    fmt.Sprintf("%s_%s", "core.devicechain.io", name),
		Content: content,
	}

	resources = append(resources, dcmdefault)
	return resources, nil
}
