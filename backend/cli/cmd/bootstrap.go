// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// Bootstrap command flags.
var (
	bootstrapKubeContext     string
	bootstrapCluster         string
	bootstrapProfile         string
	bootstrapDryRun          bool
	bootstrapAssumeYes       bool
	bootstrapSkipPreflight   bool
	bootstrapRegistry        string
	bootstrapVersion         string
	bootstrapBuild           bool
	bootstrapHost            string
	bootstrapNoTLS           bool
	bootstrapAllowLegacyDb   bool
	bootstrapDev             bool
	bootstrapEnableAreas     []string
	bootstrapLwm2mIdentities string
	bootstrapEscrowFile      string
	bootstrapEscrowPassFile  string
	bootstrapNoEscrow        bool
	bootstrapRestoreRootKey  string
	bootstrapRestoreTsdbFrom string
	bootstrapRestoreTsdbAt   string
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
//
// A larger profile is refused on a compact cluster for the FOOTPRINT CLAIM, not the
// storage budget: the JetStream reservation sums streams.Suffixes() and kv.All
// unconditionally, so the budget holds for every profile. What `full` breaks is the
// published compact number, measured on `default`, which would not describe an
// instance running three more services. The smaller two are accepted: asking for the
// smallest thing the platform ships is the one request a small-footprint preset must
// not refuse.
var (
	profilesLargerThanDefault  = []string{"full"}
	profilesSmallerThanDefault = []string{"telemetry", "ingest-only"}
)

// restoreFlagsFromArgv assembles the event-store restore inputs from the parsed
// flags.
//
// Extracted from RunE so the wiring itself is testable. It is two string copies
// and a derivation, which is exactly the kind of code that looks too trivial to
// test and then copies the source into the target — a mistake with no symptom at
// all until an operator's recovery stops at the wrong moment during an incident.
// The tests drive real argv through the real flag set, so a rename on either side
// is a failure rather than a silent no-op.
func restoreFlagsFromArgv(backupsEnabled bool) bootstrap.RestoreFlags {
	return bootstrap.RestoreFlags{
		TsdbFrom:       bootstrapRestoreTsdbFrom,
		TsdbTargetTime: bootstrapRestoreTsdbAt,
		BackupsEnabled: backupsEnabled,
	}
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

		// The restore's own shape is checkable from argv alone; whether the cluster
		// archives at all is the install's answer, checked once the record is read.
		if _, err := bootstrap.ResolveRestorePlan(restoreFlagsFromArgv(true)); err != nil {
			return err
		}

		opts := bootstrap.Options{
			Instance:             args[1],
			KubeContext:          bootstrapKubeContext,
			Cluster:              bootstrapCluster,
			Profile:              bootstrapProfile,
			DryRun:               bootstrapDryRun,
			AssumeYes:            bootstrapAssumeYes,
			ImageRegistry:        bootstrapRegistry,
			ImageVersion:         bootstrapVersion,
			BuildImages:          bootstrapBuild,
			IngressHost:          bootstrapHost,
			NoTLS:                bootstrapNoTLS,
			AllowLegacyDbRemoval: bootstrapAllowLegacyDb,
			EnableAreas:          enableAreas,
		}

		// 🔴 CHECKED WHERE A NEW NAME ENTERS, and only here. `destroy` deliberately does
		// NOT validate: whatever is already on disk must stay destroyable, including
		// anything created before this check existed.
		if err := bootstrap.ValidateInstanceName(opts.Instance); err != nil {
			return err
		}

		// Settle which images this run deploys, here and now — before EnsureCluster,
		// for the same reason the escrow and restore plans are settled above. A dcctl
		// with no pinned image version can never complete a published-image bootstrap,
		// and it used to learn that after spinning up a kind cluster to hold the run.
		// It also has to be settled before the pipeline, whose first two steps both
		// consume it now. See bootstrap.ResolveImageSource.
		img, err := bootstrap.ResolveImageSource(opts.ImageRegistry, opts.ImageVersion, opts.BuildImages)
		if err != nil {
			return err
		}
		opts.ImageRegistry, opts.ImageVersion = img.Registry, img.Version

		ctx := cmd.Context()
		// EnsureCluster resolves WHICH CLUSTER we should target, not merely how to reach
		// it — see bootstrap.ClusterBinding.
		binding, err := provider.EnsureCluster(ctx, opts)
		if err != nil {
			return err
		}

		// The cluster's identity, and then its install record — both BEFORE anything of
		// this instance is written, locally or in the cluster.
		//
		// 🔴 THE IDENTITY IS FATAL. It is what the install record is checked against: a
		// record carried in from another cluster describes prerequisites this one may not
		// have. A dry run reads both when it can and says so when it cannot, because it is
		// often aimed at a cluster not installed yet and the rehearsal is still worth having.
		installCommand := bootstrap.InstallCommand(provider.Name(), binding)
		// 🔴 A CLUSTER THAT HAS NOT BEEN INSTALLED IS REFUSED HERE, naming the command
		// that prepares it — never prepared on the way past.
		clusterUID, installRec, err := bootstrap.ReadInstall(ctx, binding.KubeContext, installCommand)
		switch {
		case err == nil:
		case !opts.DryRun && clusterUID == "":
			return fmt.Errorf("reading the identity of cluster %s: %w\n"+
				"  The identity is the kube-system namespace's UID; a context that cannot read it "+
				"is one dcctl cannot build an instance on", binding.Describe(), err)
		case !opts.DryRun:
			return err
		case clusterUID == "":
			fmt.Println(color.YellowString("[dry-run] could not identify cluster %s (%v); the plan below "+
				"assumes an installed cluster with default settings.", binding.Describe(), err))
		default:
			fmt.Println(color.YellowString("[dry-run] %v\n  The plan below assumes an installed "+
				"cluster with default settings.", err))
		}
		// Set on a dry run too: the rehearsal reads the cluster's Secrets back, and asks
		// whether each is THIS cluster's, which needs its identity. Nothing a dry run does
		// writes it anywhere.
		binding.ClusterUID = clusterUID

		st := &bootstrap.State{
			Instance:             opts.Instance,
			KubeContext:          binding.KubeContext,
			ClusterUID:           clusterUID,
			Binding:              binding,
			Provider:             provider.Name(),
			DcctlVersion:         Version,
			Profile:              opts.Profile,
			DryRun:               opts.DryRun,
			AssumeYes:            opts.AssumeYes,
			ImageRegistry:        opts.ImageRegistry,
			ImageVersion:         opts.ImageVersion,
			BuildImages:          opts.BuildImages,
			IngressHost:          opts.IngressHost,
			NoTLS:                opts.NoTLS,
			AllowLegacyDbRemoval: opts.AllowLegacyDbRemoval,
			EnableAreas:          opts.EnableAreas,
			EnabledAreas:         enabledAreas,
			Lwm2mIdentities:      lwm2mIdentities,
			Escrow:               escrowPlan,
			Values:               map[string]string{},
		}
		if installRec != nil {
			bootstrap.FollowInstall(st, installRec)
		}
		if err := followClusterShape(cmd.Flags().Changed, st); err != nil {
			return err
		}
		if st.Restore, err = bootstrap.ResolveRestorePlan(restoreFlagsFromArgv(bootstrap.BackupsEnabledFor(st))); err != nil {
			return err
		}
		// 🔴 RECORDED HERE, AND HERE IS THE ONLY PLACE IT CAN BE. This is the one moment
		// the instance name and the cluster it was resolved to are both in hand; every
		// later command used to re-derive the second from the first, and that derivation
		// is wrong for any instance bootstrapped with --kube-context. Writing it BEFORE
		// the pipeline is deliberate: a bootstrap that dies partway through has still
		// written into the cluster, and an instance that cannot be destroyed because its
		// record was never written would be the same orphan this record exists to prevent.
		//
		// A failure to record is a WARNING, not a stop: making a bookkeeping error abort a
		// bring-up would trade a recoverable annoyance (destroy falls back to the guess,
		// loudly) for a broken install.
		//
		// 🔴 What was here first is captured before it is replaced: on the one refusal that
		// fires before anything is written — a host another instance serves — the record
		// this run writes describes nothing. See PriorLocalState.
		prior := bootstrap.CapturePriorLocalState(opts.Instance)
		if !opts.DryRun {
			rec := bootstrap.InstanceRecord{
				Instance:     opts.Instance,
				Provider:     provider.Name(),
				Cluster:      binding.Cluster,
				KubeContext:  binding.KubeContext,
				Managed:      binding.Managed,
				ClusterUID:   clusterUID,
				CreatedAt:    time.Now().UTC(),
				DcctlVersion: Version,
			}
			if err := bootstrap.WriteInstanceRecord(rec); err != nil {
				fmt.Println(color.YellowString(
					"warning: could not record which cluster this instance lives in (%v).\n"+
						"  `dcctl instances list` will show it as unrecorded and `dcctl destroy` will guess.", err))
			}
		}

		runErr := bootstrap.NewDefaultPipeline().Run(ctx, st)
		finishClaim(ctx, st, runErr)
		unwindLocalRecordOnHostTaken(opts, prior, runErr)
		return runErr
	},
	SilenceUsage: true,
}

// followClusterShape settles what this instance takes from the cluster it is built on,
// and refuses what the cluster cannot give it.
//
// 🔴 THE CLUSTER'S SHAPE IS NOT RESTATED HERE, IT IS CHECKED AGAINST. HA, compact sizing,
// monitoring and backups came from the install record (FollowInstall); what is left for a
// bootstrap to decide is whether its own requests fit them.
func followClusterShape(changed func(string) bool, st *bootstrap.State) error {
	if st.Compact {
		// A compact cluster publishes a footprint measured on the default profile, and a
		// larger profile deploys more than that number describes.
		if slices.Contains(profilesLargerThanDefault, st.Profile) {
			return fmt.Errorf("this cluster was installed --compact, which publishes a footprint "+
				"measured on the `default` profile, and profile %q deploys more than that. Use --profile "+
				"default (or a smaller profile: %s), or build it on a cluster installed without --compact",
				st.Profile, strings.Join(profilesSmallerThanDefault, ", "))
		}
		fmt.Printf("compact cluster: %s\n", bootstrap.CompactSummary())
		if len(st.EnableAreas) > 0 {
			fmt.Printf("note: --enable-area adds %s beyond the compact-measured default; the printed footprint is a floor, not the total\n", strings.Join(st.EnableAreas, ", "))
		}
	}
	if st.HA {
		fmt.Printf("ha cluster: %s\n", bootstrap.HaSummary(true))
	}
	// 🔴 TLS NEEDS cert-manager, AND AN INSTALL WITHOUT IT HAS NONE TO GIVE. Serving
	// TLS there either fails the chart outright against a missing CRD or — with a
	// cluster issuer configured — succeeds and never issues the certificate.
	if st.Install != nil && !st.Install.Settings.CertManager {
		if changed("no-tls") && !st.NoTLS {
			return fmt.Errorf("--no-tls=false asks for TLS, but this cluster was installed without " +
				"cert-manager (--compact --no-tls), so nothing would issue the certificate. Re-install " +
				"the cluster with --compact --no-tls=false, or serve this instance over plain HTTP")
		}
		st.NoTLS = true
	}
	return nil
}

// unwindLocalRecordOnHostTaken puts the local record back after the one refusal
// that makes it describe nothing.
//
// 🔴 KEYED ON THE REFUSAL, NOT ON FAILURE. Every other way a bootstrap can fail leaves a
// cluster that may be half-built and MUST keep its record, which is the whole reason the
// record is written before the pipeline. This one cannot: it fires before anything is
// written, on a cluster already holding another instance — one EnsureCluster adopted,
// never one it created — so there is nothing for the record to name. Widening this to "any error" would restore the orphan
// the record exists to prevent.
//
// It reports and moves on. The refusal is what the operator is about to read, and
// failing differently because the cleanup failed would replace a message they can act on
// with one they cannot.
func unwindLocalRecordOnHostTaken(opts bootstrap.Options, prior bootstrap.PriorLocalState, runErr error) {
	var hostTaken *bootstrap.ErrHostTaken
	var noBudget *bootstrap.ErrConnectionBudget
	if opts.DryRun || !(errors.As(runErr, &hostTaken) || errors.As(runErr, &noBudget)) {
		return
	}
	removed, err := prior.Restore()
	if err != nil {
		fmt.Println(color.YellowString(
			"warning: could not undo the local record this run wrote for %q (%v).\n"+
				"  `dcctl instances list` will show it even though nothing was installed; "+
				"remove ~/.devicechain/instances/%s by hand.", opts.Instance, err, opts.Instance))
		return
	}
	if removed {
		fmt.Println(color.WhiteString(
			"Nothing was installed, so the local record for %q has been removed again.", opts.Instance))
	}
}

func init() {
	bootstrapCmd.Flags().StringVar(&bootstrapKubeContext, "kube-context", "", "build the instance on the cluster this context reaches, prepared with 'dcctl install --kube-context'")
	bootstrapCmd.Flags().StringVar(&bootstrapCluster, "cluster", bootstrap.DefaultClusterName, "local provider: the kind cluster 'dcctl install local' prepared")
	bootstrapCmd.Flags().StringVar(&bootstrapProfile, "profile", "", "configuration profile to apply")
	bootstrapCmd.Flags().BoolVar(&bootstrapDryRun, "dry-run", false, "print what would happen without applying changes")
	bootstrapCmd.Flags().BoolVarP(&bootstrapAssumeYes, "yes", "y", false, "assume yes for prompts")
	bootstrapCmd.Flags().BoolVar(&bootstrapSkipPreflight, "skip-preflight", false, "skip the local-system preflight checks")
	bootstrapCmd.Flags().StringVar(&bootstrapRegistry, "registry", "", "image registry to deploy from (default: published ghcr.io/devicechain-io, or localhost:5000 with --build)")
	bootstrapCmd.Flags().StringVar(&bootstrapVersion, "version", "", "image version/tag to deploy (default: the published release version, or 'dev' with --build)")
	bootstrapCmd.Flags().BoolVar(&bootstrapBuild, "build", false, "build images from source into a local registry (developer path; requires source + ko)")
	bootstrapCmd.Flags().StringVar(&bootstrapHost, "host", "", "ingress host to expose the instance on (default devicechain.local; use 'localhost' for a local cluster to skip the /etc/hosts edit)")
	bootstrapCmd.Flags().BoolVar(&bootstrapNoTLS, "no-tls", false, "serve plain HTTP instead of a self-signed cert (with --host localhost, a zero-config http://localhost/). On by default on a cluster installed without cert-manager, where --no-tls=false is refused")
	bootstrapCmd.Flags().BoolVar(&bootstrapAllowLegacyDb, "allow-legacy-db-removal", false,
		"proceed even though this cluster still runs the pre-CloudNativePG event-store "+
			"StatefulSet (dc-timescaledb-single). 🔴 This ASSERTS THAT YOU HAVE HANDLED THE DATA — "+
			"it is not a migration, nothing verifies it, and applying with it set destroys that "+
			"StatefulSet and brings up an empty database on the same hostname. Dump first, or use "+
			"it deliberately to discard a local instance")
	bootstrapCmd.Flags().BoolVar(&bootstrapDev, "dev", false, "local-developer preset: --build --host localhost --no-tls --yes (a zero-config http://localhost/ bring-up); rejects contradictory flags")

	bootstrapCmd.Flags().StringSliceVar(&bootstrapEnableAreas, "enable-area", nil, "additionally deploy a functional area on TOP of the profile (repeatable, e.g. --enable-area lwm2m-ingest --enable-area sparkplug-ingest). Composes with --compact; validated against the area catalog (unknown area or unmet hard dependency fails before any cluster spin-up)")
	bootstrapCmd.Flags().StringVar(&bootstrapLwm2mIdentities, "lwm2m-identities", "", "path to a JSON file of LwM2M DTLS-PSK credentials to provision: [{identity, psk(base64), tenant, externalId, deviceTypeToken, autoRegister}]. Renders the PSKs into a chart-owned Secret and binds each to lwm2m-ingest; implies --enable-area lwm2m-ingest. Validated up front (short PSK / missing tenancy fails before any cluster). Re-running bootstrap WITHOUT this flag removes the provisioned credentials")

	bootstrapCmd.Flags().StringVar(&bootstrapEscrowFile, "escrow-file", "", "where to write the encrypted root-key escrow artifact (default ~/.devicechain/escrow/<instance>-rootkey.escrow). Refuses a path inside ~/.devicechain/instances/<instance>, which 'dcctl destroy' deletes")
	bootstrapCmd.Flags().StringVar(&bootstrapEscrowPassFile, "escrow-passphrase-file", "", "read the escrow passphrase from this file instead of $"+bootstrap.EscrowPassphraseEnv+" or an interactive prompt (trailing newline stripped)")
	bootstrapCmd.Flags().BoolVar(&bootstrapNoEscrow, "no-escrow", false, "do NOT escrow the secret-store root key. The key then exists only inside the cluster: losing the cluster makes every stored secret permanently unreadable, even from a database backup, because no DeviceChain backup contains etcd. For throwaway instances only; implied by --dev")
	bootstrapCmd.Flags().StringVar(&bootstrapRestoreRootKey, "restore-root-key", "", "disaster recovery: seed the instance's secret-store root key FROM this escrow artifact instead of minting a fresh one, so a rebuilt cluster can read secrets restored from a database backup. Needs the artifact's passphrase")

	// Database restore (ADR-028 / ADR-020 A2.5). These are REBUILD-time levers, not
	// repair levers — see RestorePlan. dcctl picks the path the recovered cluster
	// archives INTO by itself, and it is deliberately not a flag: it must stay put
	// across every later re-run, so it is read back off the live cluster rather than
	// re-derived from argv.
	bootstrapCmd.Flags().StringVar(&bootstrapRestoreTsdbFrom, "restore-tsdb-from", "",
		"disaster recovery: recover the EVENT store from this archive path (the serverName inside "+
			"the backup bucket, e.g. dc-tsdb) instead of initialising an empty database. "+
			"🔴 Only takes effect when the cluster is CREATED — recover by destroying the instance "+
			"and rebuilding it with this set, not by re-running against a live one")
	bootstrapCmd.Flags().StringVar(&bootstrapRestoreTsdbAt, "restore-tsdb-at", "",
		"stop the event store's recovery at this RFC3339 timestamp instead of replaying the whole "+
			"archive. For the disaster where the data was destroyed correctly — a mistaken delete — so "+
			"pick a moment strictly before the damage. Needs --restore-tsdb-from")

	rootCmd.AddCommand(bootstrapCmd)
}

// finishClaim records how the run ended and gives the cluster lock back.
//
// 🔴 IT RUNS ON A CONTEXT THAT CANNOT BE CANCELLED, deliberately. The run's own
// context is what Ctrl+C cancels, and Ctrl+C is precisely the case where giving
// the lock back matters most — a stranded lock makes the next operator wait out a
// full lease duration and type a holder identity back to recover from a keystroke.
// Cleanup that only runs on the happy path is cleanup for the case that did not
// need it.
//
// 🔴 A FENCED RUN WRITES NOTHING. Once the claim is lost, the declaration belongs
// to whoever reclaimed it, and stamping Failed on it would overwrite the phase of
// a bootstrap that is running right now and doing fine. Release is still called,
// and it is a no-op by its own precondition check.
func finishClaim(ctx context.Context, st *bootstrap.State, runErr error) {
	if st.Claim == nil {
		return
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	// 🔴 Asked, not remembered. The cached flag is refreshed by a 10s ticker and the
	// last fenced step boundary is several minutes back — "Wait for readiness" runs
	// without one — so a reclaim landing in that gap would leave this run stamping
	// Ready or Failed on a declaration the reclaimer now owns.
	if st.Claim.CheckHeld(cleanup) == nil {
		phase := dcv1beta1.PhaseReady
		if runErr != nil {
			phase = dcv1beta1.PhaseFailed
		}
		if err := bootstrap.SetInstancePhase(cleanup, st.KubeContext, st.Instance, phase); err != nil {
			// Reporting, not correctness. A bootstrap that worked must not be
			// reported as failed because a courtesy annotation did not land.
			fmt.Println(color.YellowString("warning: could not record the instance phase (%v)", err))
		}
	}
	st.Claim.Release(cleanup)
}
