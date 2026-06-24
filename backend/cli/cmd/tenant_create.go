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

// Intialize command for creating a tenant
var createTenantCmd = NewCreateTenantCommand()

// Create command that will create a new tenant
func NewCreateTenantCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "tenant instance_id tenant_id",
		Short:        "Create a new DeviceChain tenant",
		Long:         `Creates a new DeviceChain tenant`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			desc, _ := cmd.Flags().GetString("desc")
			return createTenant(args, name, desc)
		}}
}

// Create a new DeviceChain tenant
func createTenant(args []string, name string, desc string) error {
	if len(args) < 1 {
		return errors.New("no instance id passed for new DeviceChain tenant")
	}
	if len(args) < 2 {
		return errors.New("no tenant id passed for new DeviceChain tenant")
	}

	req := buildTenantRequestFromArgs(args, name, desc)
	_, err := v1beta1.CreateTenant(req)
	if err != nil {
		return err
	}
	fmt.Printf(color.HiGreenString("Created DeviceChain tenant '%s' successfully.\n"), args[1])
	return nil
}

// Build tenant create request from command line args and flags
func buildTenantRequestFromArgs(args []string, name string, desc string) v1beta1.TenantCreateRequest {
	now := time.Now()
	instanceId := args[0]
	tenantId := args[1]

	// Default name if not passed
	if name == "" {
		name = fmt.Sprintf("Tenant '%s'", tenantId)
	}
	// Default description if not passed
	if desc == "" {
		desc = fmt.Sprintf("DeviceChain tenant '%s' created on %s", tenantId, now.Format(time.RFC3339))
	}
	return v1beta1.TenantCreateRequest{InstanceId: instanceId, TenantId: tenantId, Name: name, Description: desc}
}

func init() {
	createCmd.AddCommand(createTenantCmd)

	createTenantCmd.Flags().StringP("name", "n", "", "Specifies human-readable name for tenant")
	createTenantCmd.Flags().StringP("desc", "d", "", "Specifies human-readable description for tenant")
}
