// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"slices"
	"strings"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

// Bootstrap command flags.
var (
	bootstrapKubeContext     string
	bootstrapProfile         string
	bootstrapDryRun          bool
	bootstrapAssumeYes       bool
	bootstrapSkipPreflight   bool
	bootstrapRegistry        string
	bootstrapVersion         string
	bootstrapBuild           bool
	bootstrapHost            string
	bootstrapNoTLS           bool
	bootstrapNoMonitoring    bool
	bootstrapNoCNPG          bool
	bootstrapAllowLegacyDb   bool
	bootstrapGrafanaSSO      bool
	bootstrapDev             bool
	bootstrapCompact         bool
	bootstrapHA              bool
	bootstrapEnableAreas     []string
	bootstrapLwm2mIdentities string
	bootstrapEscrowFile      string
	bootstrapEscrowPassFile  string
	bootstrapNoEscrow        bool
	bootstrapRestoreRootKey  string
)

// devModeResolution is the set of flag values the --dev preset settles on.
type devModeResolution struct {
	Build bool
	Host  string
	NoTLS bool
	Yes   bool
	// NoEscrow skips the root-key escrow. A --dev instance is disposable by
	// construction — built from source, on localhost, over plain http — so there is
	// nothing in it worth a passphrase, and demanding one would put an interactive
	// prompt in the middle of the platform's zero-config onboarding path.
	//
	// This is the ONE preset that turns escrow off, and it is honoured rather than
	// forced: an explicit --no-escrow=false keeps it, the same way --no-tls=false
	// keeps TLS under --compact. Wanting a real escrow on a dev instance is not a
	// contradiction, it is a dependency, and refusing it would cost function for
	// nothing.
	NoEscrow bool
}

// resolveDevMode expands the --dev local-developer preset — build images from
// source, host=localhost, plain http, assume-yes (a zero-config http://localhost/
// bring-up) — on top of the user's explicit flags. It REJECTS an explicit flag that
// contradicts the preset rather than silently overriding it, so --dev can never mask
// a mistake (e.g. a real --host that would otherwise be quietly discarded). `changed`
// reports whether the user set a given flag explicitly (cmd.Flags().Changed).
func resolveDevMode(changed func(string) bool, host string, noTLS, build, noEscrow, restoring bool) (devModeResolution, error) {
	if changed("host") && host != "localhost" {
		return devModeResolution{}, fmt.Errorf("--dev pins --host to localhost; remove the conflicting --host %q (or drop --dev)", host)
	}
	if changed("no-tls") && !noTLS {
		return devModeResolution{}, fmt.Errorf("--dev serves plain http on localhost; remove --no-tls=false (or drop --dev)")
	}
	if changed("build") && !build {
		return devModeResolution{}, fmt.Errorf("--dev builds images from source; remove --build=false (or drop --dev)")
	}
	res := devModeResolution{Build: true, Host: "localhost", NoTLS: true, Yes: true, NoEscrow: true}
	if changed("no-escrow") {
		res.NoEscrow = noEscrow
	}
	// A restore writes no artifact, so there is nothing for --no-escrow to suppress
	// and setting it would only manufacture a contradiction the operator never typed
	// — they asked for --dev and --restore-root-key, and would be told those two
	// flags they did not use are incompatible.
	if restoring {
		res.NoEscrow = false
	}
	return res, nil
}

// The profile catalog, split by size relative to `default`, mirroring
// devicechain.enabledAreas in the chart's _helpers.tpl (itself a mirror of
// backend/k8s/functionalarea). `full` is `default` plus the three areas that reach
// outside the instance; the other two are strict subsets.
//
// Split this way rather than compared by name so that adding a profile forces a
// decision about which side it falls on — a new one that silently defaulted to
// "acceptable" would quietly widen what the published compact number claims to
// cover. TestEveryShippedProfileIsClassifiedForCompact reads the chart's own
// catalog and fails on a profile in neither list.
var (
	profilesLargerThanDefault  = []string{"full"}
	profilesSmallerThanDefault = []string{"telemetry", "ingest-only"}
)

// compactModeResolution is the set of flag values the --compact preset settles on.
type compactModeResolution struct {
	NoTLS        bool
	NoMonitoring bool
}

// resolveCompactMode expands the --compact small-footprint preset on top of the
// user's explicit flags.
//
// Two of its levers live on flags that already exist, so they are resolved here
// rather than buried in the pipeline: the monitoring stack (~5 pods, the single
// largest consumer) is skipped, and TLS is off — which is what makes dropping
// cert-manager safe, since cert-manager is what issues the ingress certificate.
//
// The interactions are treated DIFFERENTLY on purpose, according to whether the
// two requests can both be honoured:
//
//   - A profile LARGER than `default` is REJECTED — which today means only `full`.
//     `telemetry` and `ingest-only` are strict subsets of `default` and are
//     accepted: asking for the smallest thing the platform ships is the one request
//     a small-footprint preset must not refuse.
//
//     The reason is NOT the storage budget, though that is the reason the first
//     draft gave. The JetStream reservation sums streams.Suffixes() and kv.All
//     unconditionally — the whole inventory, including the streams `full`'s extra
//     areas create — so the budget holds for every profile and the stated
//     justification was simply false. What `full` actually breaks is the FOOTPRINT
//     CLAIM: it adds three more services, and the published compact numbers are
//     measured on `default`, so the figure would not describe the instance.
//
//   - --grafana-sso is REJECTED, but with its escape hatch named. It is not
//     contradictory in principle — it just needs the monitoring stack compact
//     removes — and the failure mode without this check is silence, not breakage.
//
//   - --no-tls=false is HONOURED. It is not a contradiction, it is a dependency:
//     TLS stays on, cert-manager stays installed to issue the cert, and every other
//     compact lever still applies. Erroring here would cost real functionality to
//     no benefit.
//
// `changed` reports whether the user set a given flag explicitly.
func resolveCompactMode(changed func(string) bool, profile string, noTLS, noMonitoring, grafanaSSO bool) (compactModeResolution, error) {
	if changed("profile") && slices.Contains(profilesLargerThanDefault, profile) {
		return compactModeResolution{}, fmt.Errorf(
			"--compact publishes a footprint measured on the `default` profile, and "+
				"profile %q deploys more than that, so the number would not describe the "+
				"instance. Use --profile default (or a smaller profile: %s), or drop "+
				"--compact",
			profile, strings.Join(profilesSmallerThanDefault, ", "))
	}
	res := compactModeResolution{NoTLS: true, NoMonitoring: true}
	// An explicit --no-tls=false keeps TLS (and therefore cert-manager); an explicit
	// --no-monitoring=false keeps the observability stack. Both cost footprint, and
	// both are the operator's call to make.
	if changed("no-tls") {
		res.NoTLS = noTLS
	}
	if changed("no-monitoring") {
		res.NoMonitoring = noMonitoring
	}
	// Grafana lives IN the monitoring stack compact removes, and the SSO wiring is
	// silently skipped when that stack is absent — including the warning, which is
	// itself gated on monitoring being on. Before compact you could only reach that
	// by typing --no-monitoring --grafana-sso together, which reads as a
	// contradiction; compact turns monitoring off on the user's behalf, so the
	// request would be swallowed with nothing printed at all.
	if grafanaSSO && res.NoMonitoring {
		return compactModeResolution{}, fmt.Errorf(
			"--grafana-sso wires login for the Grafana in the monitoring stack, which " +
				"--compact removes. Add --no-monitoring=false to keep the stack (and pay " +
				"its footprint), or drop --grafana-sso")
	}
	return res, nil
}

// bootstrapCmd provisions a usable DeviceChain instance on a target provider.
// It is a thin wrapper over the bootstrap engine package (ADR-032).
var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap <provider> <instance>",
	Short: "Bootstrap a DeviceChain instance",
	Long:  `Provisions a usable DeviceChain instance on the given provider (e.g. "local")`,
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, err := bootstrap.Get(args[0])
		if err != nil {
			return err
		}

		// --dev expands to the local-developer preset (and rejects contradictory
		// flags) before anything else runs, so preflight and the pipeline see the
		// resolved values.
		if bootstrapDev {
			res, err := resolveDevMode(cmd.Flags().Changed, bootstrapHost, bootstrapNoTLS,
				bootstrapBuild, bootstrapNoEscrow, bootstrapRestoreRootKey != "")
			if err != nil {
				return err
			}
			bootstrapBuild, bootstrapHost, bootstrapNoTLS, bootstrapAssumeYes = res.Build, res.Host, res.NoTLS, res.Yes
			bootstrapNoEscrow = res.NoEscrow
			// Echo what was actually settled, not what the preset usually settles.
			// --dev --no-escrow=false keeps the escrow and --dev --restore-root-key
			// never suppressed one, and in both cases the banner used to announce
			// --no-escrow anyway — the one line an operator would trust over the truth.
			escrowNote := "--no-escrow"
			switch {
			case bootstrapRestoreRootKey != "":
				escrowNote = "(restoring the root key)"
			case !res.NoEscrow:
				escrowNote = "(escrow kept)"
			}
			fmt.Println("dev mode: --build --host localhost --no-tls --yes " + escrowNote)
		}

		// --compact expands to the small-footprint preset. Resolved here, before
		// preflight, for the same reason as --dev: the pipeline should see settled
		// values rather than flags that still need interpreting.
		if bootstrapCompact {
			res, err := resolveCompactMode(cmd.Flags().Changed, bootstrapProfile, bootstrapNoTLS, bootstrapNoMonitoring, bootstrapGrafanaSSO)
			if err != nil {
				return err
			}
			bootstrapNoTLS, bootstrapNoMonitoring = res.NoTLS, res.NoMonitoring
			fmt.Printf("compact mode: %s\n", bootstrap.CompactSummary())
		}

		// --ha names a TOPOLOGY, and unlike --dev/--compact there is nothing to
		// expand: the two levers it sets live in the pipeline, in one struct, for
		// exactly the reason that they must not be settable apart. What is echoed
		// here is what an operator would otherwise have to infer from two files in
		// two tools.
		if bootstrapHA {
			fmt.Printf("ha mode: %s\n", bootstrap.HaSummary(true))
			// --compact --ha is allowed: compact is a SIZING preset and HA is a
			// topology, so they are orthogonal — a 3-node cluster of small nodes is a
			// real deployment. But the published compact footprint is measured on a
			// single-node default, and HA adds two more NATS servers each with their
			// own volume, so the number stops describing the instance. Noted rather
			// than refused, matching how --enable-area is handled: the same reasoning
			// that REJECTS --compact --profile full applies with less force here,
			// because unlike an extra profile this adds no DeviceChain services.
			if bootstrapCompact {
				fmt.Println("note: --ha adds two more NATS servers and their volumes; the printed compact footprint is measured single-node and is a floor, not the total")
			}
		}

		// Parse + validate --lwm2m-identities up front (a short PSK or a missing tenancy
		// field must fail here, not as a ten-minute helm-timeout when lwm2m-ingest
		// crash-loops on a bad credential). An empty flag yields no identities.
		lwm2mIdentities, err := bootstrap.ParseLwm2mIdentities(bootstrapLwm2mIdentities)
		if err != nil {
			return fmt.Errorf("--lwm2m-identities: %w", err)
		}

		// Normalize --enable-area (trim, drop blanks) ONCE, so the deployment selection
		// and the report label see the same clean set. A stray `--enable-area " "` then
		// correctly takes the untouched-profile path instead of silently switching to an
		// explicit set with a garbage "default (+  )" label.
		enableAreas := make([]string, 0, len(bootstrapEnableAreas))
		for _, a := range bootstrapEnableAreas {
			if a = strings.TrimSpace(a); a != "" {
				enableAreas = append(enableAreas, a)
			}
		}
		// Provisioning LwM2M identities is meaningless unless lwm2m-ingest is deployed,
		// so the flag implies --enable-area lwm2m-ingest. Guard the append so
		// `--enable-area lwm2m-ingest --lwm2m-identities f` doesn't list the area twice
		// in the report label (ResolveEnabledAreas dedups the deployment either way).
		if len(lwm2mIdentities) > 0 && !slices.Contains(enableAreas, "lwm2m-ingest") {
			enableAreas = append(enableAreas, "lwm2m-ingest")
		}

		// Resolve --enable-area (profile ∪ extras) and validate it against the area
		// catalog up front — BEFORE the preflight doctor and any cluster — so a typo'd
		// or dependency-broken area fails immediately rather than after a doctor probe
		// or a ten-minute chart-render timeout.
		enabledAreas, err := bootstrap.ResolveEnabledAreas(bootstrapProfile, enableAreas)
		if err != nil {
			return fmt.Errorf("resolving deployment areas (--profile/--enable-area): %w", err)
		}
		// --compact publishes a footprint measured on the default profile; extra areas
		// add workloads beyond that, so the printed compact figure understates the real
		// instance. Flag it rather than silently contradict the number.
		if bootstrapCompact && len(enableAreas) > 0 {
			fmt.Printf("note: --enable-area adds %s beyond the compact-measured default; the printed footprint is a floor, not the total\n", strings.Join(enableAreas, ", "))
		}

		// Diagnose the local system up front so a run fails fast on a missing
		// tool / low limit / unreachable docker rather than midway through.
		if !bootstrapSkipPreflight {
			if d := runDoctor(args[0]); d.fails > 0 {
				return fmt.Errorf("%d preflight check(s) failed — fix the items above, or re-run with --skip-preflight", d.fails)
			}
		}

		// Settle the root-key escrow LAST among the up-front checks but still before
		// any cluster exists (ADR-059 / ADR-028). Last, because this is the only step
		// that may stop and ask a human something, and a passphrase typed twice only
		// to hit a typo'd --enable-area is a bad trade. Before the cluster, because
		// both of its failure modes — no passphrase in a non-interactive run, an
		// unopenable restore artifact — must land in the first second rather than as
		// a prompt nobody sees behind ten minutes of spin-up.
		escrowPlan, err := bootstrap.ResolveEscrowPlan(args[1], bootstrap.EscrowFlags{
			File:           bootstrapEscrowFile,
			PassphraseFile: bootstrapEscrowPassFile,
			NoEscrow:       bootstrapNoEscrow,
			RestoreFile:    bootstrapRestoreRootKey,
			DryRun:         bootstrapDryRun,
		})
		if err != nil {
			return err
		}

		opts := bootstrap.Options{
			Instance:             args[1],
			KubeContext:          bootstrapKubeContext,
			Profile:              bootstrapProfile,
			DryRun:               bootstrapDryRun,
			AssumeYes:            bootstrapAssumeYes,
			ImageRegistry:        bootstrapRegistry,
			ImageVersion:         bootstrapVersion,
			BuildImages:          bootstrapBuild,
			IngressHost:          bootstrapHost,
			NoTLS:                bootstrapNoTLS,
			NoMonitoring:         bootstrapNoMonitoring,
			NoCNPG:               bootstrapNoCNPG,
			AllowLegacyDbRemoval: bootstrapAllowLegacyDb,
			GrafanaSSO:           bootstrapGrafanaSSO,
			Compact:              bootstrapCompact,
			HA:                   bootstrapHA,
			EnableAreas:          enableAreas,
		}

		ctx := cmd.Context()
		// EnsureCluster resolves the kube-context we should target.
		kubeContext, err := provider.EnsureCluster(ctx, opts)
		if err != nil {
			return err
		}

		st := &bootstrap.State{
			Instance:             opts.Instance,
			KubeContext:          kubeContext,
			Profile:              opts.Profile,
			DryRun:               opts.DryRun,
			AssumeYes:            opts.AssumeYes,
			ImageRegistry:        opts.ImageRegistry,
			ImageVersion:         opts.ImageVersion,
			BuildImages:          opts.BuildImages,
			IngressHost:          opts.IngressHost,
			NoTLS:                opts.NoTLS,
			NoMonitoring:         opts.NoMonitoring,
			NoCNPG:               opts.NoCNPG,
			AllowLegacyDbRemoval: opts.AllowLegacyDbRemoval,
			GrafanaSSO:           opts.GrafanaSSO,
			Compact:              opts.Compact,
			HA:                   opts.HA,
			EnableAreas:          opts.EnableAreas,
			EnabledAreas:         enabledAreas,
			Lwm2mIdentities:      lwm2mIdentities,
			Escrow:               escrowPlan,
			Values:               map[string]string{},
		}
		return bootstrap.NewDefaultPipeline().Run(ctx, st)
	},
	SilenceUsage: true,
}

func init() {
	bootstrapCmd.Flags().StringVar(&bootstrapKubeContext, "kube-context", "", "kube-context to target (default: auto-detect)")
	bootstrapCmd.Flags().StringVar(&bootstrapProfile, "profile", "", "configuration profile to apply")
	bootstrapCmd.Flags().BoolVar(&bootstrapDryRun, "dry-run", false, "print what would happen without applying changes")
	bootstrapCmd.Flags().BoolVarP(&bootstrapAssumeYes, "yes", "y", false, "assume yes for prompts")
	bootstrapCmd.Flags().BoolVar(&bootstrapSkipPreflight, "skip-preflight", false, "skip the local-system preflight checks")
	bootstrapCmd.Flags().StringVar(&bootstrapRegistry, "registry", "", "image registry to deploy from (default: published ghcr.io/devicechain-io, or localhost:5000 with --build)")
	bootstrapCmd.Flags().StringVar(&bootstrapVersion, "version", "", "image version/tag to deploy (default: the published release version, or 'dev' with --build)")
	bootstrapCmd.Flags().BoolVar(&bootstrapBuild, "build", false, "build images from source into a local registry (developer path; requires source + ko)")
	bootstrapCmd.Flags().StringVar(&bootstrapHost, "host", "", "ingress host to expose the instance on (default devicechain.local; use 'localhost' for a local cluster to skip the /etc/hosts edit)")
	bootstrapCmd.Flags().BoolVar(&bootstrapNoTLS, "no-tls", false, "serve plain HTTP instead of a self-signed cert (with --host localhost, a zero-config http://localhost/)")
	bootstrapCmd.Flags().BoolVar(&bootstrapNoMonitoring, "no-monitoring", false, "skip the monitoring stack (Prometheus/Grafana) AND the chart's ServiceMonitors/alerts — for a minimal install or a cluster where you wire metrics separately")
	bootstrapCmd.Flags().BoolVar(&bootstrapAllowLegacyDb, "allow-legacy-db-removal", false,
		"proceed even though this cluster still runs the pre-CloudNativePG database "+
			"StatefulSets (dc-postgresql / dc-timescaledb-single). 🔴 This ASSERTS THAT YOU "+
			"HAVE HANDLED THE DATA — it is not a migration, nothing verifies it, and applying "+
			"with it set destroys those StatefulSets and brings up empty databases on the same "+
			"hostnames. Dump first, or use it deliberately to discard a local instance")
	bootstrapCmd.Flags().BoolVar(&bootstrapNoCNPG, "no-cnpg", false, "skip the CloudNativePG operator and the database backup plugin — for a cluster that ALREADY runs CNPG, since Helm cannot adopt objects another installer created")
	bootstrapCmd.Flags().BoolVar(&bootstrapGrafanaSSO, "grafana-sso", false, "wire Grafana login to DeviceChain SSO (ADR-047), operator/superuser-tier only; enables the OAuth AS (needs https, or --host localhost --no-tls for local http)")
	bootstrapCmd.Flags().BoolVar(&bootstrapDev, "dev", false, "local-developer preset: --build --host localhost --no-tls --yes (a zero-config http://localhost/ bring-up); rejects contradictory flags. Compose with --grafana-sso for local SSO")

	bootstrapCmd.Flags().BoolVar(&bootstrapCompact, "compact", false, "small-footprint preset: lowered JetStream/KV ceilings with the smaller volumes they permit, lowered scheduling requests, and no monitoring stack. Keeps --profile default (it does not change which services run); rejects a conflicting --profile")
	bootstrapCmd.Flags().BoolVar(&bootstrapHA, "ha", false, "ADR-020 messaging HA: a 3-node NATS RAFT cluster spread one server per node, with every JetStream stream and KV bucket replicated across it. Sets BOTH halves (the OpenTofu server count and the chart's streamReplicas) from one value, and refuses to install if the cluster cannot host the topology. Needs at least 3 schedulable nodes. Does NOT replicate Postgres/TimescaleDB (see ADR-028) and does not change how many services run")
	bootstrapCmd.Flags().StringSliceVar(&bootstrapEnableAreas, "enable-area", nil, "additionally deploy a functional area on TOP of the profile (repeatable, e.g. --enable-area lwm2m-ingest --enable-area sparkplug-ingest). Composes with --compact; validated against the area catalog (unknown area or unmet hard dependency fails before any cluster spin-up)")
	bootstrapCmd.Flags().StringVar(&bootstrapLwm2mIdentities, "lwm2m-identities", "", "path to a JSON file of LwM2M DTLS-PSK credentials to provision: [{identity, psk(base64), tenant, externalId, deviceTypeToken, autoRegister}]. Renders the PSKs into a chart-owned Secret and binds each to lwm2m-ingest; implies --enable-area lwm2m-ingest. Validated up front (short PSK / missing tenancy fails before any cluster). Re-running bootstrap WITHOUT this flag removes the provisioned credentials")

	bootstrapCmd.Flags().StringVar(&bootstrapEscrowFile, "escrow-file", "", "where to write the encrypted root-key escrow artifact (default ~/.devicechain/escrow/<instance>-rootkey.escrow). Refuses a path inside ~/.devicechain/<instance>, which `dcctl destroy` deletes")
	bootstrapCmd.Flags().StringVar(&bootstrapEscrowPassFile, "escrow-passphrase-file", "", "read the escrow passphrase from this file instead of $"+bootstrap.EscrowPassphraseEnv+" or an interactive prompt (trailing newline stripped)")
	bootstrapCmd.Flags().BoolVar(&bootstrapNoEscrow, "no-escrow", false, "do NOT escrow the secret-store root key. The key then exists only inside the cluster: losing the cluster makes every stored secret permanently unreadable, even from a database backup, because no DeviceChain backup contains etcd. For throwaway instances only; implied by --dev")
	bootstrapCmd.Flags().StringVar(&bootstrapRestoreRootKey, "restore-root-key", "", "disaster recovery: seed the instance's secret-store root key FROM this escrow artifact instead of minting a fresh one, so a rebuilt cluster can read secrets restored from a database backup. Needs the artifact's passphrase")

	rootCmd.AddCommand(bootstrapCmd)
}
