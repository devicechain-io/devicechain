/*
Copyright © 2022 SiteWhere LLC - All Rights Reserved
Unauthorized copying of this file, via any medium is strictly prohibited.
Proprietary and confidential.
*/

package cmd

import (
	"errors"
	"fmt"
	"time"

	v1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// Intialize command for creating a microservice
var createMicroserviceCmd = NewCreateMicroserviceCommand()

// Create command that will create a new microservice
func NewCreateMicroserviceCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "microservice instance_id microservice_id config_id",
		Short:        "Create a new DeviceChain microservice",
		Long:         `Creates a new DeviceChain microservice`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			desc, _ := cmd.Flags().GetString("desc")
			return createMicroservice(args, name, desc)
		}}
}

// Create a new DeviceChain microservice
func createMicroservice(args []string, name string, desc string) error {
	if len(args) < 1 {
		return errors.New("no instance id passed for new DeviceChain microservice")
	}
	if len(args) < 2 {
		return errors.New("no id passed for new DeviceChain microservice")
	}
	if len(args) < 3 {
		return errors.New("no configuration id passed for new DeviceChain microservice")
	}

	req := buildMicroserviceRequestFromArgs(args, name, desc)
	_, err := v1beta1.CreateMicroservice(req)
	if err != nil {
		return err
	}
	fmt.Printf(color.HiGreenString("Created DeviceChain microservice '%s' successfully.\n"), args[1])
	return nil
}

// Build microservice create request from command line args and flags
func buildMicroserviceRequestFromArgs(args []string, name string, desc string) v1beta1.MicroserviceCreateRequest {
	now := time.Now()
	instanceId := args[0]
	msId := args[1]
	cfgId := args[2]

	// Default name if not passed
	if name == "" {
		name = fmt.Sprintf("Microservice '%s'", msId)
	}
	// Default description if not passed
	if desc == "" {
		desc = fmt.Sprintf("DeviceChain microservice '%s' created on %s", msId, now.Format(time.RFC3339))
	}
	return v1beta1.MicroserviceCreateRequest{Id: msId, InstanceId: instanceId, ConfigurationId: cfgId, Name: name, Description: desc}
}

func init() {
	createCmd.AddCommand(createMicroserviceCmd)

	createMicroserviceCmd.Flags().StringP("name", "n", "", "Specifies human-readable name for microservice")
	createMicroserviceCmd.Flags().StringP("desc", "d", "", "Specifies human-readable description for microservice")
}
