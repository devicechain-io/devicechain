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

// Intialize command for creating an instance
var createInstanceCmd = NewCreateInstanceCommand()

// Create command that will create a new instance
func NewCreateInstanceCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "instance instance_id config_id",
		Short:        "Create a new DeviceChain instance",
		Long:         `Creates a new DeviceChain instance`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			desc, _ := cmd.Flags().GetString("desc")
			return createInstance(args, name, desc)
		}}
}

// Create a new DeviceChain instance
func createInstance(args []string, name string, desc string) error {
	if len(args) < 1 {
		return errors.New("no instance id passed for new DeviceChain instance")
	}
	if len(args) < 2 {
		return errors.New("no configuration id passed for new DeviceChain instance")
	}

	req := buildInstanceRequestFromArgs(args, name, desc)
	_, err := v1beta1.CreateInstance(req)
	if err != nil {
		return err
	}
	fmt.Printf(color.HiGreenString("Created DeviceChain instance '%s' successfully.\n"), args[0])
	return nil
}

// Build instance create request from command line args and flags
func buildInstanceRequestFromArgs(args []string, name string, desc string) v1beta1.InstanceCreateRequest {
	now := time.Now()
	id := args[0]
	configId := args[1]

	// Default name if not passed
	if name == "" {
		name = fmt.Sprintf("Instance '%s'", id)
	}
	// Default description if not passed
	if desc == "" {
		desc = fmt.Sprintf("DeviceChain instance '%s' created on %s", id, now.Format(time.RFC3339))
	}
	return v1beta1.InstanceCreateRequest{Id: id, ConfigurationId: configId, Name: name, Description: desc}
}

func init() {
	createCmd.AddCommand(createInstanceCmd)

	createInstanceCmd.Flags().StringP("name", "n", "", "Specifies human-readable name for instance")
	createInstanceCmd.Flags().StringP("desc", "d", "", "Specifies human-readable description for instance")
}
