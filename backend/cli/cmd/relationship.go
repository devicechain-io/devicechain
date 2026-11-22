// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/apiclient"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

const relationshipFields = `token sourceType source { token } targetType target { token } relationshipType { token } metadata`
const relationshipAddMutation = `mutation($request:EntityRelationshipCreateRequest!){createEntityRelationship(request:$request){` + relationshipFields + `}}`
const relationshipListQuery = `query($criteria:EntityRelationshipSearchCriteria!){entityRelationships(criteria:$criteria){results{` + relationshipFields + `} pagination{pageStart pageEnd totalRecords}}}`
const relationshipRemoveMutation = `mutation($tokens:[String!]!){removeEntityRelationships(tokens:$tokens)}`

type relationshipReference struct {
	Token string `json:"token"`
}

type relationshipRecord struct {
	Token            string                `json:"token"`
	SourceType       string                `json:"sourceType"`
	Source           relationshipReference `json:"source"`
	TargetType       string                `json:"targetType"`
	Target           relationshipReference `json:"target"`
	RelationshipType relationshipReference `json:"relationshipType"`
	Metadata         *string               `json:"metadata"`
}

func init() { rootCmd.AddCommand(newRelationshipCmd()) }

func newRelationshipCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "relationship",
		Short: "Add, list and remove typed entity relationships",
		Long: `Manage a tenant's typed entity relationship graph as the identity you sign in with.
Endpoints are identified by entity type and token. The relationship type must already exist;
the server validates entity types, endpoints and permissions.`,
	}
	f := c.PersistentFlags()
	f.StringP("server", "s", "localhost", "instance host for platform API calls")
	addInstanceTargetFlags(c)
	f.Bool("tls", false, "use https for platform endpoints")
	f.StringP("tenant", "t", "", "tenant that owns the relationships (required)")
	f.String("email", "", "identity to authenticate as; it must be a member of the tenant (required)")
	f.String("password", "", "password for the identity (required)")
	f.String("otp", "", otpFlagHelp)
	f.StringP("output", "o", "table", "output format: table or json")
	f.Bool("json", false, "print JSON (equivalent to --output json)")

	add := &cobra.Command{
		Use: "add <token>", Short: "Create one typed entity relationship",
		Long: `Create a relationship with its own token between a source and a target entity.
Give --source-type, --source, --target-type, --target and --relationship-type.
Optional --metadata is passed as the API's metadata string.`,
		Args: cobra.ExactArgs(1), SilenceUsage: true, RunE: runRelationshipAdd,
	}
	list := &cobra.Command{
		Use: "list", Short: "List one page of typed entity relationships",
		Long: `List one page of relationships, including pagination totals in table and JSON output.
Narrow by source, target, relationship type or whether the relationship type is tracked.
An endpoint token filter requires its corresponding entity type. --tracked=false selects
untracked relationships; omitting --tracked includes both.`,
		Args: cobra.NoArgs, SilenceUsage: true, RunE: runRelationshipList,
	}
	for _, cmd := range []*cobra.Command{add, list} {
		cmd.Flags().String("source-type", "", "source entity type")
		cmd.Flags().String("source", "", "source entity token")
		cmd.Flags().String("target-type", "", "target entity type")
		cmd.Flags().String("target", "", "target entity token")
		cmd.Flags().String("relationship-type", "", "relationship type token")
	}
	add.Flags().String("metadata", "", "optional relationship metadata string")
	list.Flags().Int("page-number", 1, "page to read (starting at 1)")
	list.Flags().Int("page-size", 100, "number of relationships per page")
	list.Flags().Bool("tracked", false, "filter by whether the relationship type is tracked (default: both)")
	remove := &cobra.Command{
		Use: "remove <token> [token...]", Short: "Remove relationships by their tokens",
		Long: `Remove the relationships named by their own tokens in one API request.
If none of the tokens match, the command succeeds and reports that nothing was removed.`,
		Args: cobra.MinimumNArgs(1), SilenceUsage: true, RunE: runRelationshipRemove,
	}
	c.AddCommand(add, list, remove)
	return c
}

func relationshipString(cmd *cobra.Command, flag string) string {
	v, _ := cmd.Flags().GetString(flag)
	return v
}

func relationshipOutputIsJSON(cmd *cobra.Command) (bool, error) {
	output := relationshipString(cmd, "output")
	asJSON, _ := cmd.Flags().GetBool("json")
	if output != "table" && output != "json" {
		return false, fmt.Errorf("--output must be table or json, got %q", output)
	}
	if asJSON && cmd.Flags().Changed("output") && output != "json" {
		return false, fmt.Errorf("--json conflicts with --output %s", output)
	}
	return asJSON || output == "json", nil
}

func relationshipSession(cmd *cobra.Command) (*userclient.TenantSession, string, error) {
	server, tls, err := targetFlags(cmd)
	if err != nil {
		return nil, "", err
	}
	tenant := relationshipString(cmd, "tenant")
	email, password := relationshipString(cmd, "email"), relationshipString(cmd, "password")
	if strings.TrimSpace(tenant) == "" {
		return nil, "", fmt.Errorf("--tenant is required: relationships are per-tenant, and there is no default")
	}
	if strings.TrimSpace(email) == "" || strings.TrimSpace(password) == "" {
		return nil, "", fmt.Errorf("--email and --password are required")
	}
	e := sim.ResolveEndpoints(server, "", "", tls)
	session := apiclient.NewTenantSession(nil, e.UserGraphQL, email, password, tenant, recordedEndpointVersions(cmd, e.DeviceMgmtGraphQL)...).
		WithOTP(otpFunc(otpFlag(cmd)))
	return session, e.DeviceMgmtGraphQL, nil
}

func runRelationshipAdd(cmd *cobra.Command, args []string) error {
	asJSON, err := relationshipOutputIsJSON(cmd)
	if err != nil {
		return err
	}
	request := map[string]any{"token": args[0]}
	for flag, key := range map[string]string{
		"source-type": "sourceType", "source": "source", "target-type": "targetType",
		"target": "target", "relationship-type": "relationshipType",
	} {
		value := relationshipString(cmd, flag)
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("--%s is required", flag)
		}
		request[key] = value
	}
	if strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("relationship token must not be empty")
	}
	if cmd.Flags().Changed("metadata") {
		request["metadata"] = relationshipString(cmd, "metadata")
	}
	c, endpoint, err := relationshipSession(cmd)
	if err != nil {
		return err
	}
	var data struct{ CreateEntityRelationship relationshipRecord }
	if err := c.Query(cmd.Context(), endpoint, relationshipAddMutation, map[string]any{"request": request}, &data); err != nil {
		return fmt.Errorf("add relationship: %w", err)
	}
	if asJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(data.CreateEntityRelationship)
	}
	return printRelationships(cmd, []relationshipRecord{data.CreateEntityRelationship})
}

func runRelationshipList(cmd *cobra.Command, _ []string) error {
	asJSON, err := relationshipOutputIsJSON(cmd)
	if err != nil {
		return err
	}
	page, _ := cmd.Flags().GetInt("page-number")
	size, _ := cmd.Flags().GetInt("page-size")
	if page < 1 || size < 1 || int64(page) > 2147483647 || int64(size) > 2147483647 {
		return fmt.Errorf("--page-number and --page-size must be between 1 and 2147483647")
	}
	criteria := map[string]any{"pageNumber": page, "pageSize": size}
	for flag, key := range map[string]string{
		"source-type": "sourceType", "source": "source", "target-type": "targetType",
		"target": "target", "relationship-type": "relationshipType",
	} {
		if value := relationshipString(cmd, flag); value != "" {
			criteria[key] = value
		}
	}
	for _, end := range []string{"source", "target"} {
		if relationshipString(cmd, end) != "" && strings.TrimSpace(relationshipString(cmd, end+"-type")) == "" {
			return fmt.Errorf("--%s requires --%s-type", end, end)
		}
	}
	if cmd.Flags().Changed("tracked") {
		criteria["tracked"], _ = cmd.Flags().GetBool("tracked")
	}
	c, endpoint, err := relationshipSession(cmd)
	if err != nil {
		return err
	}
	var data struct {
		EntityRelationships struct {
			Results    []relationshipRecord `json:"results"`
			Pagination struct {
				PageStart    int `json:"pageStart"`
				PageEnd      int `json:"pageEnd"`
				TotalRecords int `json:"totalRecords"`
			} `json:"pagination"`
		}
	}
	if err := c.Query(cmd.Context(), endpoint, relationshipListQuery, map[string]any{"criteria": criteria}, &data); err != nil {
		return fmt.Errorf("list relationships: %w", err)
	}
	result := data.EntityRelationships
	if asJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	if err := printRelationships(cmd, result.Results); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Page %d (size %d); %d total relationships.\n",
		page, size, result.Pagination.TotalRecords)
	return err
}

func runRelationshipRemove(cmd *cobra.Command, args []string) error {
	asJSON, err := relationshipOutputIsJSON(cmd)
	if err != nil {
		return err
	}
	for _, token := range args {
		if strings.TrimSpace(token) == "" {
			return fmt.Errorf("relationship token must not be empty")
		}
	}
	c, endpoint, err := relationshipSession(cmd)
	if err != nil {
		return err
	}
	var data struct{ RemoveEntityRelationships bool }
	if err := c.Query(cmd.Context(), endpoint, relationshipRemoveMutation, map[string]any{"tokens": args}, &data); err != nil {
		return fmt.Errorf("remove relationships: %w", err)
	}
	if asJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]bool{"removed": data.RemoveEntityRelationships})
	}
	if !data.RemoveEntityRelationships {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "No matching relationships; nothing removed.")
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "Removed relationships:", strings.Join(args, ", "))
	return err
}

func printRelationships(cmd *cobra.Command, rows []relationshipRecord) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TOKEN\tSOURCE TYPE\tSOURCE\tTARGET TYPE\tTARGET\tRELATIONSHIP TYPE")
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", row.Token, row.SourceType, row.Source.Token,
			row.TargetType, row.Target.Token, row.RelationshipType.Token)
	}
	return w.Flush()
}
