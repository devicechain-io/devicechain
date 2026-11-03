// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/attr"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// Attr command flags that are shared by every subcommand (the per-run ones are read from
// the flag set the run function is handed).
var (
	attrTenant   string
	attrEmail    string
	attrPassword string
)

var attrCmd = &cobra.Command{
	Use:   "attr",
	Short: "Set, read and remove an entity's attributes (facet values)",
	Long: `Set, read and remove the current-state attributes of a device, asset, area or customer.

A facet's values ARE attributes: the facet declaration says which keys classify an entity,
and the value lives on the entity as an attribute in the SHARED scope. This command is the
scriptable way to write them, the same rows the console's Facets tab edits.

The entity is addressed by kind and token: dcctl attr set device meter-0042 climate arid.

Writes default to the SHARED scope because it is the only one facet selectors read. A value
written at CLIENT or SERVER scope is valid, reads back, and matches nothing in Browse or in
a dynamic group; pass --scope only for an attribute that is not a facet value.

SHARED is also the scope a device reads its configuration from. Writing or removing a SHARED
attribute on a device whose profile declares that key as configuration creates a new
configuration revision for the device, whether or not you meant it as a facet value.

The server authorizes every call as the identity you sign in with. This command checks no
permission itself.`,
}

var attrSetCmd = &cobra.Command{
	Use:   "set <kind> <token> <key> <value>",
	Short: "Set one attribute (a facet value) on an entity",
	Long: `Set one attribute on an entity, creating it or replacing its value.

<kind> is device, asset, area or customer, and <token> is the entity's token.

For a SHARED-scope write the value type is taken from the facet's declaration, so
"dcctl attr set device meter-0042 floor 3" is written as the LONG the facet declares. A
--type that contradicts the declaration is refused, because the write would save and never
match. A key that is not declared as a facet has no type to take, so --type is required.

A facet value must match the declared vocabulary's spelling and case exactly: "Arid" does
not match a facet value "arid".

A negative number looks like a flag, so put it after "--" (flags first):

  dcctl attr set device m1 offset --tenant acme --email you@example.com --password <password> -- -5

Writing one key never touches another: this is an upsert on (entity, scope, key).`,
	Args:         cobra.ExactArgs(4),
	RunE:         runAttrSet,
	SilenceUsage: true,
}

var attrGetCmd = &cobra.Command{
	Use:   "get <kind> <token> [key]",
	Short: "Show an entity's attributes",
	Long: `Show an entity's attributes in every scope, or one key when [key] is given.

Attributes outside the SHARED scope are listed too, which explains a facet that reads set
but matches nothing: the device reported its own value at CLIENT scope. Narrow with --scope.

A token that names no entity answers exactly like an entity with no attributes, so an empty
result is not proof the entity exists.`,
	Args:         cobra.RangeArgs(2, 3),
	RunE:         runAttrGet,
	SilenceUsage: true,
}

var attrUnsetCmd = &cobra.Command{
	Use:   "unset <kind> <token> <key>",
	Short: "Remove one attribute (a facet value) from an entity",
	Long: `Remove one attribute from an entity.

This deletes the row; it does not write an empty value, which would stay on the entity and
still count as a value. Removing a key that is not set succeeds and says so.`,
	Args:         cobra.ExactArgs(3),
	RunE:         runAttrUnset,
	SilenceUsage: true,
}

func init() {
	attrCmd.PersistentFlags().StringVarP(&attrTenant, "tenant", "t", "", "tenant that owns the entity (required)")
	attrCmd.PersistentFlags().StringP("server", "s", "localhost", "instance host for platform API calls")
	attrCmd.PersistentFlags().Bool("tls", false, "use https for platform endpoints")
	attrCmd.PersistentFlags().StringVar(&attrEmail, "email", "", "identity to authenticate as; it must be a member of the tenant (required)")
	attrCmd.PersistentFlags().StringVar(&attrPassword, "password", "", "password for the identity (required)")
	attrCmd.PersistentFlags().String("otp", "", otpFlagHelp)

	scopeHelp := "attribute scope: " + strings.Join(attr.Scopes, ", ")
	attrSetCmd.Flags().String("scope", attr.FacetScope, scopeHelp+"; SHARED is the only scope facets read")
	attrSetCmd.Flags().String("type", "", "value type: "+strings.Join(attr.ValueTypes, ", ")+
		" (default: the facet's declared type)")
	attrGetCmd.Flags().String("scope", "", scopeHelp+" (default: every scope)")
	attrGetCmd.Flags().StringP("output", "o", "table", "output format: table or json")
	attrUnsetCmd.Flags().String("scope", attr.FacetScope, scopeHelp)

	attrCmd.AddCommand(attrSetCmd, attrGetCmd, attrUnsetCmd)
	rootCmd.AddCommand(attrCmd)
}

// attrSession builds the tenant session for the identity and returns it with the
// device-management endpoint, or the reason the flags are not enough.
func attrSession(cmd *cobra.Command) (attr.Client, string, error) {
	server, _ := cmd.Flags().GetString("server")
	tls, _ := cmd.Flags().GetBool("tls")
	if strings.TrimSpace(attrTenant) == "" {
		return nil, "", fmt.Errorf("--tenant is required: attributes are per-tenant, and there is no default")
	}
	if strings.TrimSpace(attrEmail) == "" || strings.TrimSpace(attrPassword) == "" {
		return nil, "", fmt.Errorf("--email and --password are required: the change is authorized as the identity that makes it")
	}
	endpoints := sim.ResolveEndpoints(server, "", "", tls)
	session := userclient.NewTenantSession(nil, endpoints.UserGraphQL, attrEmail, attrPassword, attrTenant).
		WithOTP(otpFunc(otpFlag(cmd)))
	return session, endpoints.DeviceMgmtGraphQL, nil
}

func runAttrSet(cmd *cobra.Command, args []string) error {
	c, endpoint, err := attrSession(cmd)
	if err != nil {
		return err
	}
	scope, _ := cmd.Flags().GetString("scope")
	vt, _ := cmd.Flags().GetString("type")
	set, err := attr.Set(cmd.Context(), c, attr.SetOptions{
		Target: attr.Target{Endpoint: endpoint, Kind: args[0], Token: args[1]},
		Key:    args[2], Value: args[3], Scope: scope, ValueType: vt,
	})
	if err != nil {
		return err
	}
	value := ""
	if set.Value != nil {
		value = *set.Value
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s %s = %s (%s)\n", color.GreenString("Set"),
		args[0], args[1], set.Key, value, set.ValueType)
	return nil
}

func runAttrGet(cmd *cobra.Command, args []string) error {
	asJSON, err := attrOutputIsJSON(cmd)
	if err != nil {
		return err
	}
	c, endpoint, err := attrSession(cmd)
	if err != nil {
		return err
	}
	scope, _ := cmd.Flags().GetString("scope")
	key := ""
	if len(args) == 3 {
		key = args[2]
	}
	attrs, err := attr.Get(cmd.Context(), c, attr.GetOptions{
		Target: attr.Target{Endpoint: endpoint, Kind: args[0], Token: args[1]},
		Key:    key, Scope: scope,
	})
	if err != nil {
		return err
	}
	if len(attrs) == 0 && !asJSON && key != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "%s is not set on %s %q, or the token is unknown: the API answers "+
			"the same for both, so check the token if you expected it to exist.\n", key, args[0], args[1])
		return nil
	}
	if len(attrs) == 0 && !asJSON {
		fmt.Fprintf(cmd.OutOrStdout(), "No attributes found on %s %q. If you expected some, check the token: "+
			"an unknown token reads the same as an entity with none.\n", args[0], args[1])
		return nil
	}
	return attr.Print(cmd.OutOrStdout(), attrs, asJSON)
}

func attrOutputIsJSON(cmd *cobra.Command) (bool, error) {
	out, _ := cmd.Flags().GetString("output")
	switch out {
	case "table", "":
		return false, nil
	case "json":
		return true, nil
	}
	return false, fmt.Errorf("--output must be table or json, got %q", out)
}

func runAttrUnset(cmd *cobra.Command, args []string) error {
	c, endpoint, err := attrSession(cmd)
	if err != nil {
		return err
	}
	scope, _ := cmd.Flags().GetString("scope")
	removed, err := attr.Unset(cmd.Context(), c, attr.UnsetOptions{
		Target: attr.Target{Endpoint: endpoint, Kind: args[0], Token: args[1]},
		Key:    args[2], Scope: scope,
	})
	if err != nil {
		return err
	}
	if !removed {
		fmt.Fprintf(cmd.OutOrStdout(), "%s was not set on %s %s; nothing removed.\n", args[2], args[0], args[1])
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s from %s %s\n", color.GreenString("Removed"), args[2], args[0], args[1])
	return nil
}
