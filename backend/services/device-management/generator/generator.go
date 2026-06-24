// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package generator

import (
	"fmt"

	"github.com/devicechain-io/dc-device-management/config"
	gen "github.com/devicechain-io/dc-k8s/generators"
)

type ResourceProvider struct{}

// Get instance configuration CRs that should be created in tooling
func (ResourceProvider) GetConfigurationResources() ([]gen.ConfigurationResource, error) {
	resources := make([]gen.ConfigurationResource, 0)

	name := "device-management-default"
	msconfig := config.NewDeviceManagementConfiguration()
	content, err := gen.GenerateMicroserviceConfig(name, "device-management", "devicechain-io/device-management:0.0.1", msconfig)
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
