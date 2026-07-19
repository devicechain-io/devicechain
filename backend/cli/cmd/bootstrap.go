// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

// Bootstrap command flags.
var (
	bootstrapKubeContext   string
	bootstrapProfile       string
	bootstrapDryRun        bool
	bootstrapAssumeYes     bool
	bootstrapSkipPreflight bool
	bootstrapRegistry      string
	bootstrapVersion       string
	bootstrapBuild         bool
	bootstrapHost          string
	bootstrapNoTLS         bool
	bootstrapNoMonitoring  bool
	bootstrapGrafanaSSO    bool
	bootstrapDev           bool
	bootstrapCompact       bool
)

// devModeResolution is the set of flag values the --dev preset settles on.
type devModeResolution struct {
	Build bool
	Host  string
	NoTLS bool
	Yes   bool
}

// resolveDevMode expands the --dev local-developer preset — build images from
// source, host=localhost, plain http, assume-yes (a zero-config http://localhost/
// bring-up) — on top of the user's explicit flags. It REJECTS an explicit flag that
// contradicts the preset rather than silently overriding it, so --dev can never mask
// a mistake (e.g. a real --host that would otherwise be quietly discarded). `changed`
// reports whether the user set a given flag explicitly (cmd.Flags().Changed).
func resolveDevMode(changed func(string) bool, host string, noTLS, build bool) (devModeResolution, error) {
	if changed("host") && host != "localhost" {
		return devModeResolution{}, fmt.Errorf("--dev pins --host to localhost; remove the conflicting --host %q (or drop --dev)", host)
	}
	if changed("no-tls") && !noTLS {
		return devModeResolution{}, fmt.Errorf("--dev serves plain http on localhost; remove --no-tls=false (or drop --dev)")
	}
	if changed("build") && !build {
		return devModeResolution{}, fmt.Errorf("--dev builds images from source; remove --build=false (or drop --dev)")
	}
	return devModeResolution{Build: true, Host: "localhost", NoTLS: true, Yes: true}, nil
}

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
// The two contradictions are treated DIFFERENTLY on purpose:
//
//   - --profile full is REJECTED. Compact's storage budget is sized against the
//     areas `default` deploys; full adds three more that reach outside the instance.
//     There is no resolution that honours both, so it must not pick one silently.
//   - --no-tls=false is HONOURED. It is not a contradiction, it is a dependency:
//     TLS stays on, cert-manager stays installed to issue the cert, and every other
//     compact lever still applies. Erroring here would cost real functionality to
//     no benefit.
//
// `changed` reports whether the user set a given flag explicitly.
func resolveCompactMode(changed func(string) bool, profile string, noTLS, noMonitoring bool) (compactModeResolution, error) {
	if changed("profile") && profile != "" && profile != "default" {
		return compactModeResolution{}, fmt.Errorf(
			"--compact sizes storage for the %q profile's areas; profile %q deploys a "+
				"different set, so the budget would not hold. Drop --compact, or use "+
				"--profile default",
			"default", profile)
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
			res, err := resolveDevMode(cmd.Flags().Changed, bootstrapHost, bootstrapNoTLS, bootstrapBuild)
			if err != nil {
				return err
			}
			bootstrapBuild, bootstrapHost, bootstrapNoTLS, bootstrapAssumeYes = res.Build, res.Host, res.NoTLS, res.Yes
			fmt.Println("dev mode: --build --host localhost --no-tls --yes")
		}

		// --compact expands to the small-footprint preset. Resolved here, before
		// preflight, for the same reason as --dev: the pipeline should see settled
		// values rather than flags that still need interpreting.
		if bootstrapCompact {
			res, err := resolveCompactMode(cmd.Flags().Changed, bootstrapProfile, bootstrapNoTLS, bootstrapNoMonitoring)
			if err != nil {
				return err
			}
			bootstrapNoTLS, bootstrapNoMonitoring = res.NoTLS, res.NoMonitoring
			fmt.Printf("compact mode: %s\n", bootstrap.CompactSummary())
		}

		// Diagnose the local system up front so a run fails fast on a missing
		// tool / low limit / unreachable docker rather than midway through.
		if !bootstrapSkipPreflight {
			if d := runDoctor(args[0]); d.fails > 0 {
				return fmt.Errorf("%d preflight check(s) failed — fix the items above, or re-run with --skip-preflight", d.fails)
			}
		}

		opts := bootstrap.Options{
			Instance:      args[1],
			KubeContext:   bootstrapKubeContext,
			Profile:       bootstrapProfile,
			DryRun:        bootstrapDryRun,
			AssumeYes:     bootstrapAssumeYes,
			ImageRegistry: bootstrapRegistry,
			ImageVersion:  bootstrapVersion,
			BuildImages:   bootstrapBuild,
			IngressHost:   bootstrapHost,
			NoTLS:         bootstrapNoTLS,
			NoMonitoring:  bootstrapNoMonitoring,
			GrafanaSSO:    bootstrapGrafanaSSO,
			Compact:       bootstrapCompact,
		}

		ctx := cmd.Context()
		// EnsureCluster resolves the kube-context we should target.
		kubeContext, err := provider.EnsureCluster(ctx, opts)
		if err != nil {
			return err
		}

		st := &bootstrap.State{
			Instance:      opts.Instance,
			KubeContext:   kubeContext,
			Profile:       opts.Profile,
			DryRun:        opts.DryRun,
			AssumeYes:     opts.AssumeYes,
			ImageRegistry: opts.ImageRegistry,
			ImageVersion:  opts.ImageVersion,
			BuildImages:   opts.BuildImages,
			IngressHost:   opts.IngressHost,
			NoTLS:         opts.NoTLS,
			NoMonitoring:  opts.NoMonitoring,
			GrafanaSSO:    opts.GrafanaSSO,
			Compact:       opts.Compact,
			Values:        map[string]string{},
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
	bootstrapCmd.Flags().BoolVar(&bootstrapGrafanaSSO, "grafana-sso", false, "wire Grafana login to DeviceChain SSO (ADR-047), operator/superuser-tier only; enables the OAuth AS (needs https, or --host localhost --no-tls for local http)")
	bootstrapCmd.Flags().BoolVar(&bootstrapDev, "dev", false, "local-developer preset: --build --host localhost --no-tls --yes (a zero-config http://localhost/ bring-up); rejects contradictory flags. Compose with --grafana-sso for local SSO")

	bootstrapCmd.Flags().BoolVar(&bootstrapCompact, "compact", false, "small-footprint preset: lowered JetStream/KV ceilings with the smaller volumes they permit, lowered scheduling requests, and no monitoring stack. Keeps --profile default (it does not change which services run); rejects a conflicting --profile")

	rootCmd.AddCommand(bootstrapCmd)
}
