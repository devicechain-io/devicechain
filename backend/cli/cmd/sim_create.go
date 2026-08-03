// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
	registerMqttFlags(simCreateCmd.Flags())
	simCreateCmd.Flags().String("manifest", "devicepulse",
		"built-in scenario to run ("+strings.Join(sim.KnownManifestIds, ", ")+")")
	simCreateCmd.Flags().String("tier", sim.DefaultTenantTier, "tenant tier to package the sim at (ADR-065)")
	simCreateCmd.Flags().Int("shed-priority", 0, "ADR-063 shed-priority override 1-100 (0 = inherit the tier's); a load-test lever to place a probe tenant in a shed band")
	simCmd.AddCommand(simCreateCmd)
}

// flagMqttBroker/flagMqttInsecure name the two flags that address the MQTT gateway a
// scenario's command far end dials. Named once so registration, the resolver below,
// and the test that drives them through real pflag parsing cannot drift apart on a
// rename — a mismatch would make Changed() always false and silently pin the default.
const (
	flagMqttBroker   = "mqtt-broker"
	flagMqttInsecure = "mqtt-insecure"
)

// registerMqttFlags adds the far-end addressing flags to a flag set. Shared with the
// test so the behaviour is exercised through the SAME registration the command uses.
func registerMqttFlags(fs *pflag.FlagSet) {
	fs.String(flagMqttBroker, "",
		"NATS MQTT gateway a scenario's command far end dials (default ssl://<server>:1883)")
	fs.Bool(flagMqttInsecure, false,
		"skip verification of the MQTT gateway's certificate (defaults ON without --tls, since a "+
			"local bring-up's gateway cert is self-signed; pass --mqtt-insecure=false to force verification)")
}

// resolveMqttInsecure decides whether the sim's command far end verifies the MQTT
// gateway's certificate.
//
// The broker's TLS is terminated with whatever cert its deployment holds, and a local
// bring-up's is self-signed — nothing chains it to a system root, so a verifying far
// end cannot connect there at all. --tls is the available signal for "this is a real
// deployment reachable at its cert's SAN": a run that does not even use TLS to the
// platform's own HTTP endpoints has already given up more than this protects.
//
// An explicit --mqtt-insecure overrides in BOTH directions, which is why it reads
// Changed rather than the value — a plain GetBool could not tell "--mqtt-insecure=false"
// from "not passed", and the derived default would win over an explicit instruction.
func resolveMqttInsecure(fs *pflag.FlagSet, tls bool) bool {
	if fs.Changed(flagMqttInsecure) {
		v, _ := fs.GetBool(flagMqttInsecure)
		return v
	}
	return !tls
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
	mqttBroker, _ := cmd.Flags().GetString(flagMqttBroker)
	mqttInsecure := resolveMqttInsecure(cmd.Flags(), tls)
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

	endpoints := sim.ResolveEndpoints(server, ingress, mqttBroker, tls)
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

		MqttTLSInsecure: mqttInsecure,
	}
	if err := sim.Save(rec); err != nil {
		return err
	}
	path, _ := sim.RecordPath(name)

	fmt.Printf("✅ sim %q created — tenant %q, scoped identity %q\n\n", name, tenant, email)
	// Stated on every create, not only for a scenario that needs it: a certificate
	// check that was skipped is worth one line wherever it was decided, and dcctl
	// does not know which scenario the handshake will end up running.
	fmt.Printf("MQTT gateway (command far end): %s\n", endpoints.MqttBroker)
	// Only for a broker the client will actually establish TLS to. A plaintext
	// tcp:// broker presents no certificate, so "its certificate will NOT be
	// verified" is a security-relevant sentence that is untrue there.
	if mqttInsecure && sim.BrokerIsTLS(endpoints.MqttBroker) {
		fmt.Println("  ⚠️  its certificate will NOT be verified (--mqtt-insecure=false to require it)")
	}
	fmt.Println()
	fmt.Println("Run the sim actor (the Go reference runner):")
	fmt.Printf("    dc-simulator --handshake %s\n\n", path)
	fmt.Println("Then drive / inspect it:")
	for _, line := range simCreateNextSteps(name) {
		fmt.Printf("    %s\n", line)
	}
	return nil
}

// simCreateNextSteps are the dcctl commands create suggests once a sim exists.
//
// A function rather than three Printf calls so a test can resolve every command and
// flag in them against the real cobra tree. These lines are copy-paste bait, and they
// go stale the moment a command changes shape: this block printed
// `dcctl sim destroy <name> [--purge]` for as long as that flag existed, and nothing
// would have noticed when it was removed.
func simCreateNextSteps(name string) []string {
	return []string{
		fmt.Sprintf("dcctl sim status %s", name),
		fmt.Sprintf("dcctl sim stop %s   |   dcctl sim start %s", name, name),
		fmt.Sprintf("dcctl sim destroy %s", name),
	}
}
