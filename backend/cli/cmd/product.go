// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/product"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var (
	productTenant   string
	productEmail    string
	productPassword string
)

var productCmd = &cobra.Command{
	Use:   "product",
	Short: "Export, plan and apply a tenant's product as files (preview)",
	Long: `Promote a tenant's product through git: export it to a directory of files, review a change
as a plan, and apply the files to the same or another tenant.

The product is the device types, device profiles (metrics, calculated metrics, payload
decoders with their test vectors, typed commands, detection rules and canvas graphs),
dashboards, outbound connectors and inbound integrations. Everything is read and written over
the same tenant GraphQL the console uses, as the identity you sign in with, so the server
authorizes every call; this command checks no permission itself.

This command tree is PREVIEW: the bundle format (apiVersion product.devicechain.io/v1alpha1)
is new and may change in a later minor release.

SECRETS ARE HANDLES, NEVER VALUES. The platform never reveals a stored connector credential or
an integration's bearer token, so an export cannot contain one. A connector's credential is
named by a handle (credential: {handle: env:NAME}) that apply resolves from the environment.
Every bundle is also scanned for values shaped like a secret, and one that holds any is refused.`,
}

var productExportCmd = &cobra.Command{
	Use:   "export <dir>",
	Short: "Write the tenant's product to a directory",
	Long: `Write the tenant's product to <dir> as a product.yaml and one file per resource.

<dir> must be empty, absent, or already a bundle (it holds a product.yaml), in which case the
files of resources that no longer exist are removed. Exporting twice with nothing changed in
between writes the same bytes both times.

What is exported is the DRAFT of each profile, connector and dashboard; apply publishes it
again. Device-type maintenance windows, dynamic groups and customers are NOT part of the
bundle: a device type or rule that names one carries its token as a reference, which must
already exist where the bundle is applied.

An opt-in area this instance does not serve (outbound connectors) is skipped with a warning
unless you name it in --kinds.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runProductExport,
	SilenceUsage: true,
}

var productPlanCmd = &cobra.Command{
	Use:   "plan <dir>",
	Short: "Show what applying a bundle would change, without changing it",
	Long: `Read the tenant and print what 'apply' would do to make it hold the bundle in <dir>.
Nothing is written.

A line starts with + (create), ~ (update), - (delete) or ^ (publish). Deletes happen only to
the metric, command and rule definitions UNDER a profile the bundle carries; a device type,
profile, integration, connector or dashboard that is in the tenant and not in the bundle is
left alone and reported as a note.

Against a tenant that already holds the bundle the plan reports no changes. Use
--fail-on-changes to make that an exit status, for drift detection in CI.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runProductPlan,
	SilenceUsage: true,
}

var productApplyCmd = &cobra.Command{
	Use:   "apply <dir>",
	Short: "Make the tenant hold the bundle, then publish",
	Long: `Make the tenant hold the bundle in <dir>: an idempotent upsert by token, converging drafts
and then publishing what changed. Running it twice is the same as running it once, and a run
that fails part-way is finished by running it again.

Order: connectors, profiles (with their definitions), device types, integrations, dashboards.

Concurrency: connector and dashboard writes are conditional on the version stamp read when the
plan was made, so an edit made by someone else in between is refused rather than overwritten.
Device types, profiles and their definitions, and integrations carry no such stamp, so a
concurrent edit to those is last-writer-wins.

Credentials: a connector's credential is sent only to a connector that has none, from the
environment variable its handle names; --rotate-secrets sends it to every connector. A new
inbound integration gets a server-generated bearer token that this command does not capture;
rotate it to issue one.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runProductApply,
	SilenceUsage: true,
}

func init() {
	productCmd.PersistentFlags().StringVarP(&productTenant, "tenant", "t", "", "tenant to read or change (required)")
	productCmd.PersistentFlags().StringP("server", "s", "localhost", "instance host for platform API calls")
	productCmd.PersistentFlags().Bool("tls", false, "use https for platform endpoints")
	productCmd.PersistentFlags().StringVar(&productEmail, "email", "", "identity to authenticate as; it must be a member of the tenant (required)")
	productCmd.PersistentFlags().StringVar(&productPassword, "password", "", "password for the identity (required)")
	productCmd.PersistentFlags().String("otp", "", otpFlagHelp)
	productCmd.PersistentFlags().String("kinds", "", "comma-separated kinds to include: "+strings.Join(product.AllKinds, ", ")+" (export default: all)")

	productExportCmd.Flags().String("name", "", "label for the product, written to product.yaml (default: the existing bundle's)")
	productPlanCmd.Flags().Bool("fail-on-changes", false, "exit non-zero when the plan has changes")
	for _, c := range []*cobra.Command{productPlanCmd, productApplyCmd} {
		c.Flags().Bool("allow-suspected-secrets", false, "proceed although the bundle holds values shaped like a secret")
		c.Flags().Bool("no-publish", false, "converge drafts and stop; do not publish")
		c.Flags().Bool("rotate-secrets", false, "send every connector credential whose handle is set, not only to connectors without one")
	}
	productApplyCmd.Flags().Bool("yes", false, "apply without printing the plan first")

	productCmd.AddCommand(productExportCmd, productPlanCmd, productApplyCmd)
	rootCmd.AddCommand(productCmd)
}

// connectorsURL derives the outbound-connectors endpoint, which sim.Endpoints does not carry
// (that struct is the simulator handshake's shape), from the dashboard one on the same host.
func connectorsURL(dashboardURL string) string {
	return strings.Replace(dashboardURL, "/api/dashboard-management/", "/api/outbound-connectors/", 1)
}

func productSession(cmd *cobra.Command) (product.Client, product.Endpoints, error) {
	server, _ := cmd.Flags().GetString("server")
	tls, _ := cmd.Flags().GetBool("tls")
	if strings.TrimSpace(productTenant) == "" {
		return nil, product.Endpoints{}, fmt.Errorf("--tenant is required: a product is per-tenant, and there is no default")
	}
	if strings.TrimSpace(productEmail) == "" || strings.TrimSpace(productPassword) == "" {
		return nil, product.Endpoints{}, fmt.Errorf("--email and --password are required: the change is authorized as the identity that makes it")
	}
	e := sim.ResolveEndpoints(server, "", "", tls)
	session := userclient.NewTenantSession(nil, e.UserGraphQL, productEmail, productPassword, productTenant).
		WithOTP(otpFunc(otpFlag(cmd)))
	return session, product.Endpoints{
		DeviceManagement: e.DeviceMgmtGraphQL,
		Connectors:       connectorsURL(e.DashboardMgmtGraphQL),
		Dashboards:       e.DashboardMgmtGraphQL,
	}, nil
}

func runProductExport(cmd *cobra.Command, args []string) error {
	c, ep, err := productSession(cmd)
	if err != nil {
		return err
	}
	kindsFlag, _ := cmd.Flags().GetString("kinds")
	kinds, err := product.ParseKinds(kindsFlag)
	if err != nil {
		return err
	}
	name, _ := cmd.Flags().GetString("name")
	skipped, err := product.Export(cmd.Context(), c, ep, kinds, strings.TrimSpace(kindsFlag) == "", args[0], name)
	if err != nil {
		return err
	}
	for _, s := range skipped {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s %s skipped: this instance does not serve that area\n", color.YellowString("warning:"), s)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s the product of tenant %s to %s\n", color.GreenString("Exported"), productTenant, args[0])
	return nil
}

// readBundle loads a bundle and applies the secret scan unless it is waived.
func readBundle(cmd *cobra.Command, dir string) (*product.Bundle, error) {
	b, err := product.Read(dir)
	if err != nil {
		return nil, err
	}
	allow, _ := cmd.Flags().GetBool("allow-suspected-secrets")
	if err := product.ScanError(b.Scan()); err != nil && !allow {
		return nil, fmt.Errorf("%w\n(pass --allow-suspected-secrets only if these are not secrets)", err)
	}
	return b, nil
}

func productOptions(cmd *cobra.Command) product.Options {
	noPublish, _ := cmd.Flags().GetBool("no-publish")
	rotate, _ := cmd.Flags().GetBool("rotate-secrets")
	return product.Options{NoPublish: noPublish, RotateSecrets: rotate}
}

// restrict drops the kinds --kinds leaves out, so a plan or apply touches only what was asked.
func restrict(cmd *cobra.Command, b *product.Bundle) error {
	flag, _ := cmd.Flags().GetString("kinds")
	if strings.TrimSpace(flag) == "" {
		return nil
	}
	kinds, err := product.ParseKinds(flag)
	if err != nil {
		return err
	}
	if !kinds["device-types"] {
		b.DeviceTypes = nil
	}
	if !kinds["profiles"] {
		b.Profiles = nil
	}
	if !kinds["integrations"] {
		b.Integrations = nil
	}
	if !kinds["connectors"] {
		b.Connectors = nil
	}
	if !kinds["dashboards"] {
		b.Dashboards = nil
	}
	return nil
}

func runProductPlan(cmd *cobra.Command, args []string) error {
	c, ep, err := productSession(cmd)
	if err != nil {
		return err
	}
	b, err := readBundle(cmd, args[0])
	if err != nil {
		return err
	}
	if err := restrict(cmd, b); err != nil {
		return err
	}
	p, err := product.PlanBundle(cmd.Context(), c, ep, b, productOptions(cmd))
	if err != nil {
		return err
	}
	p.Fprint(cmd.OutOrStdout())
	if len(p.Blockers) > 0 {
		return fmt.Errorf("the plan has %d blocker(s); apply would refuse it", len(p.Blockers))
	}
	if fail, _ := cmd.Flags().GetBool("fail-on-changes"); fail && !p.Empty() {
		return fmt.Errorf("the tenant differs from the bundle: %d change(s)", len(p.Changes))
	}
	return nil
}

func runProductApply(cmd *cobra.Command, args []string) error {
	c, ep, err := productSession(cmd)
	if err != nil {
		return err
	}
	b, err := readBundle(cmd, args[0])
	if err != nil {
		return err
	}
	if err := restrict(cmd, b); err != nil {
		return err
	}
	p, err := product.PlanBundle(cmd.Context(), c, ep, b, productOptions(cmd))
	if err != nil {
		return err
	}
	if yes, _ := cmd.Flags().GetBool("yes"); !yes {
		p.Fprint(cmd.OutOrStdout())
	}
	if len(p.Blockers) > 0 {
		return p.Apply(cmd.Context(), cmd.OutOrStdout()) // refuses, naming the blockers
	}
	if p.Empty() {
		return nil
	}
	if err := p.Apply(cmd.Context(), cmd.OutOrStdout()); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s %d change(s) to tenant %s\n", color.GreenString("Applied"), len(p.Changes), productTenant)
	return nil
}
