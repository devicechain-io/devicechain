// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	gen "github.com/devicechain-io/dc-k8s/generators"
	ms "github.com/devicechain-io/dc-microservice/config"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

const GenResFolder = "resources"

// Create common command for creating DeviceChain resources
var resgenCmd = &cobra.Command{
	Use:   "resgen",
	Short: "Generate configuration resources",
	Long:  `Generates configuration resources directly from the microservice codebase`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Generating resources from source code...")
		os.MkdirAll(GenResFolder, 0777)

		// Generate instance resources. Per-functional-area workload + config
		// rendering now lives in the Helm chart (deploy/helm/devicechain,
		// ADR-022 decision 4), so resgen no longer emits a microservice catalog.
		fmt.Println(GreenUnderline("\nInstance Resources"))
		if err := generateInstanceResources(); err != nil {
			return err
		}
		return nil
	},
}

// Get instance configuration CRs that should be created in tooling
func getInstanceResources() ([]gen.ConfigurationResource, error) {
	resources := make([]gen.ConfigurationResource, 0)

	name := "dcic-default"
	config := ms.NewDefaultInstanceConfiguration()
	content, err := gen.GenerateInstanceConfig(name, config)
	if err != nil {
		return nil, err
	}
	dcidefault := gen.ConfigurationResource{
		Name:    fmt.Sprintf("%s_%s", "core.devicechain.io", name),
		Content: content,
	}

	resources = append(resources, dcidefault)
	return resources, nil
}

// Generate instance resources by introspecting microservice configs
func generateInstanceResources() error {
	dcires, err := getInstanceResources()
	if err != nil {
		return err
	}
	for _, dci := range dcires {
		path := filepath.Join(GenResFolder, fmt.Sprintf("%s.yaml", dci.Name))
		err = os.WriteFile(path, dci.Content, 0644)
		if err != nil {
			return err
		}
		fmt.Printf(color.GreenString("Generated instance resource: %s\n"), color.HiWhiteString(path))
	}
	fmt.Println()
	return nil
}

func init() {
	rootCmd.AddCommand(resgenCmd)
}
