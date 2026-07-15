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

	rootCmd.AddCommand(bootstrapCmd)
}
