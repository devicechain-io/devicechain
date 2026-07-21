// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

var simCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Mint a scoped identity + tenant for a sim and write its handshake",
	Long: `Mint a scoped per-sim identity and tenant on the instance, then write the
handshake file the dc-simulator actor reads to come up.

dcctl is the ONLY caller of the instance admin surface: it logs in as the superuser,
creates the sim's tenant, creates an identity with NO system roles, and binds it to
that tenant with the tenant-admin role (full authority scoped to that one tenant).
The sim actor authenticates as that identity and never touches the admin surface.

Idempotent: re-running create against an existing sim reconciles it (and keeps the
stored password in sync with the identity).`,
	Args:         cobra.ExactArgs(1),
	RunE:         runSimCreate,
	SilenceUsage: true,
}

func init() {
	simCreateCmd.Flags().Int64("seed", 1, "deterministic generation seed for the sim's populations")
	simCreateCmd.Flags().String("ingress", "", "device-plane HTTP ingress base URL (default http(s)://<server>:8081)")
	simCreateCmd.Flags().String("manifest", "devicepulse", "built-in scenario to run (devicepulse, buildingpulse)")
	simCreateCmd.Flags().String("tier", sim.DefaultTenantTier, "tenant tier to package the sim at (ADR-065)")
	simCreateCmd.Flags().Int("shed-priority", 0, "ADR-063 shed-priority override 1-100 (0 = inherit the tier's); a load-test lever to place a probe tenant in a shed band")
	simCmd.AddCommand(simCreateCmd)
}

func runSimCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := sim.ValidateName(name); err != nil {
		return err
	}

	server, _ := cmd.Flags().GetString("server")
	instance, _ := cmd.Flags().GetString("instance")
	adminEmail, _ := cmd.Flags().GetString("admin-email")
	adminPassword, _ := cmd.Flags().GetString("admin-password")
	tls, _ := cmd.Flags().GetBool("tls")
	controlAddr, _ := cmd.Flags().GetString("control-addr")
	ingress, _ := cmd.Flags().GetString("ingress")
	seed, _ := cmd.Flags().GetInt64("seed")
	manifestId, _ := cmd.Flags().GetString("manifest")
	if err := sim.ValidateManifestId(manifestId); err != nil {
		return err
	}
	tier, _ := cmd.Flags().GetString("tier")
	// shed-priority is an optional override: a 1-100 value places the tenant in a
	// specific band; 0 (the default, and an explicit 0) means inherit the tier's
	// shedPriority. Pass nil rather than 0 in the inherit case, since 0 is not a valid
	// band (the API rejects it) — keying on the value rather than Changed() keeps an
	// explicit `--shed-priority 0` matching the "0 = inherit" help instead of erroring.
	var shedPriority *int
	if v, _ := cmd.Flags().GetInt("shed-priority"); v > 0 {
		shedPriority = &v
	}

	tenant := sim.DeriveTenant(name)
	email := sim.DeriveEmail(name)

	// Keep the stored password in sync with any pre-existing identity: reuse the
	// existing record's password on re-create, else generate a fresh one. create
	// force-sets the password below either way, so the identity always matches.
	var password string
	if existing, loadErr := sim.Load(name); loadErr == nil {
		password = existing.SimPassword
	} else {
		p, genErr := sim.GeneratePassword()
		if genErr != nil {
			return genErr
		}
		password = p
	}

	endpoints := sim.ResolveEndpoints(server, ingress, tls)
	adminURL := sim.AdminURL(server, tls)
	admin := sim.NewAdmin(endpoints.UserGraphQL, adminURL, adminEmail, adminPassword)

	ctx := cmd.Context()
	if err := admin.EnsureSuperuser(ctx); err != nil {
		return err
	}
	if err := admin.CreateTenant(ctx, tenant, name+" (sim)", tier, shedPriority); err != nil {
		return err
	}
	if err := admin.CreateIdentity(ctx, email, password); err != nil {
		return err
	}
	// Force the scoped-identity invariants so re-create RECONCILES a pre-existing
	// identity rather than trusting first-create: no system roles (no admin power),
	// the stored password, and exactly the tenant-admin membership role.
	if err := admin.ForceNoSystemRoles(ctx, email); err != nil {
		return err
	}
	if err := admin.SetPassword(ctx, email, password); err != nil {
		return err
	}
	if err := admin.AddTenantAdmin(ctx, email, tenant); err != nil {
		return err
	}
	if err := admin.ForceTenantAdmin(ctx, email, tenant); err != nil {
		return err
	}

	rec := &sim.Record{
		Name:        name,
		Tenant:      tenant,
		SimEmail:    email,
		SimPassword: password,
		Endpoints:   endpoints,
		ManifestId:  manifestId,
		Seed:        seed,
		InstanceId:  instance,
		ControlAddr: controlAddr,
		AdminURL:    adminURL,
	}
	if err := sim.Save(rec); err != nil {
		return err
	}
	path, _ := sim.RecordPath(name)

	fmt.Printf("✅ sim %q created — tenant %q, scoped identity %q\n\n", name, tenant, email)
	fmt.Println("Run the sim actor (the Go reference runner):")
	fmt.Printf("    dc-simulator --handshake %s\n\n", path)
	fmt.Println("Then drive / inspect it:")
	fmt.Printf("    dcctl sim status %s\n", name)
	fmt.Printf("    dcctl sim stop %s   |   dcctl sim start %s\n", name, name)
	fmt.Printf("    dcctl sim destroy %s [--purge]\n", name)
	return nil
}
