// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/hashicorp/terraform-exec/tfexec"
)

const (
	// stateDirMode and stateFileMode keep ~/.devicechain/<instance> readable only
	// by its owner. Same values, and the same reasoning, as escrowDirMode /
	// escrowFileMode next door: the directory holds cleartext secrets, so the
	// question is not whether anyone WOULD read it but whether they COULD.
	stateDirMode  = 0o700
	stateFileMode = 0o600
)

// applyInfra extracts the embedded OpenTofu config into a stable per-instance
// working directory and runs init+apply through terraform-exec. The config (.tf
// + modules) is refreshed from the binary on every run, but terraform.tfstate
// lives in that directory and persists across runs so the apply is idempotent.
func applyInfra(ctx context.Context, st *State) (err error) {
	tofuBin, err := findTofu()
	if err != nil {
		return err
	}

	workdir, err := instanceStateDir(st.Instance, "infra")
	if err != nil {
		return err
	}
	// Deferred, and registered the moment the directory exists, because a FAILED
	// apply writes state too — a partial apply is exactly the run that leaves
	// resource attributes on disk, and the path that returns early is the one a
	// call placed after the apply would skip. A chmod error only surfaces when
	// nothing else went wrong; the apply's own error is always the better one to
	// hand back.
	defer func() {
		if herr := hardenStateFiles(workdir); herr != nil && err == nil {
			err = herr
		}
	}()
	if err := extractFS(assets.OpenTofu(), workdir); err != nil {
		return fmt.Errorf("extracting infrastructure config: %w", err)
	}

	tf, err := tfexec.NewTerraform(workdir, tofuBin)
	if err != nil {
		return err
	}
	// Stream tofu's own progress so a long apply is not a silent wait.
	tf.SetStdout(os.Stdout)
	tf.SetStderr(os.Stderr)

	if err := tf.Init(ctx); err != nil {
		return fmt.Errorf("tofu init: %w", err)
	}

	// Refuse to shrink a broker cluster that is already carrying replicated data.
	// Reads the CURRENT state, so it must run after Init and before Apply — this is
	// the only point where both the applied topology and the requested one are known.
	if err := checkHaNotTornDown(ctx, st, tf); err != nil {
		return err
	}

	opts := make([]tfexec.ApplyOption, 0, 16)
	for _, v := range infraVars(st) {
		opts = append(opts, tfexec.Var(v))
	}
	if err := tf.Apply(ctx, opts...); err != nil {
		// The ONE failure this retries: the API server could not call the CNPG
		// admission webhook, so the database Cluster releases were refused (and,
		// thanks to atomic, rolled back leaving nothing behind). See
		// cnpgadmission.go for why the gate lives here rather than in the tofu
		// root, and why the probe is a server-side dry-run create.
		//
		// Everything else is returned untouched. A retry that hides a real error
		// is worse than the race it was written for.
		if !isCNPGWebhookUnavailable(err.Error()) {
			return fmt.Errorf("tofu apply: %w", err)
		}
		reportCNPGAdmissionWait()
		if werr := waitForCNPGAdmission(ctx, st.KubeContext, cnpgAdmissionTimeout); werr != nil {
			return fmt.Errorf("tofu apply failed because the CloudNativePG admission webhook "+
				"was unreachable, and it did not recover: %w (original apply error: %v)", werr, err)
		}
		// Once, not in a loop. The probe has proved the API server can admit a
		// Cluster, so a second failure of the same kind is not a race and must
		// surface rather than be retried around.
		if retryErr := tf.Apply(ctx, opts...); retryErr != nil {
			// Both errors, deliberately. The retry can fail for a reason the first
			// attempt caused rather than shared — a refused UPDATE leaves Helm
			// rolling back through the same dead webhook, and the release can land
			// in pending-rollback, whose "another operation is in progress" says
			// nothing about the webhook. Dropping the original would leave the
			// operator holding the second error and none of the story.
			return fmt.Errorf("tofu apply, retried after waiting on the CloudNativePG "+
				"admission webhook: %w (the apply that triggered the wait failed with: %v)", retryErr, err)
		}
	}

	// Read the NATS TLS material back out (ADR-025): the broker terminates TLS and
	// emits its CA, which the Helm step threads into the instance config so
	// services dial over TLS. The broker flag and the client flag come from the
	// same outputs so they cannot drift apart.
	outputs, err := tf.Output(ctx)
	if err != nil {
		return fmt.Errorf("reading tofu outputs: %w", err)
	}
	// Decode errors are propagated, not swallowed: a broker that terminates TLS
	// paired with a client that (silently) fell back to plaintext is the one
	// failure the two-flags-one-source design exists to prevent, and NATS'
	// retry-forever masks it as a healthy-but-mute service. Fail the bootstrap
	// loudly instead.
	if meta, ok := outputs["nats_tls_enabled"]; ok {
		var enabled bool
		if err := json.Unmarshal(meta.Value, &enabled); err != nil {
			return fmt.Errorf("decoding nats_tls_enabled output: %w", err)
		}
		if enabled {
			st.Values["natsTlsEnabled"] = "true"
		}
	}
	if meta, ok := outputs["nats_ca"]; ok {
		var ca string
		if err := json.Unmarshal(meta.Value, &ca); err != nil {
			return fmt.Errorf("decoding nats_ca output: %w", err)
		}
		st.Values["natsCA"] = ca
	}
	// The NATS server count the infrastructure ACTUALLY provisioned (ADR-020 A0).
	// Read back rather than assumed so the Helm step can refuse a replica factor the
	// broker cannot host — see checkBrokerHostsReplication for why reading reality
	// rather than our own request is the entire value of this. A decode failure is
	// non-fatal: this feeds a guard, and an unreadable output should not fail a
	// bring-up that is otherwise fine (the guard treats absence as "cannot tell").
	if meta, ok := outputs["nats_cluster_replicas"]; ok {
		var servers int
		if err := json.Unmarshal(meta.Value, &servers); err == nil && servers > 0 {
			st.Values[natsClusterReplicasKey] = strconv.Itoa(servers)
		}
	}
	// Whether the infrastructure ACTUALLY archives WAL and takes base backups
	// (ADR-028, ADR-020 A2.5) — read back rather than derived from our own flags,
	// for the same reason as nats_cluster_replicas above.
	//
	// It gates the backup alerting rules the Helm step renders. Getting it wrong in
	// the permissive direction is not dangerous, only useless: with archive_mode off
	// the cnpg_pg_stat_archiver_* series do not exist, so the rules would load,
	// evaluate nothing and never fire — which is precisely the silent shape those
	// alerts exist to remove, so it is worth reading the truth.
	//
	// 🔑 Absence is treated as OFF, and that asymmetry is deliberate. The two ways
	// to be wrong are not equal: rules that cannot fire look like a monitored
	// instance and are not, whereas no rules at all is visibly nothing. `dcctl ha
	// verify` reports the backup state either way, so the quieter failure is the
	// one to prefer here.
	st.Values[databaseBackupsKey] = "false"
	if meta, ok := outputs["database_backups_enabled"]; ok {
		var enabled bool
		if err := json.Unmarshal(meta.Value, &enabled); err == nil && enabled {
			st.Values[databaseBackupsKey] = "true"
		}
	}
	// Whether those backups survive losing the cluster, which is a different
	// question from whether they exist and the one an operator is most likely to
	// get wrong. Null when backups are off; "false" for the default in-cluster
	// destination.
	if meta, ok := outputs["database_backup_survives_cluster_loss"]; ok {
		var offsite bool
		if err := json.Unmarshal(meta.Value, &offsite); err == nil {
			st.Values[databaseBackupOffsiteKey] = strconv.FormatBool(offsite)
		}
	}
	// The namespace the database Clusters run in, which is where their metrics are
	// exported from. NOT the instance namespace — an alert scoped to the instance's
	// own namespace selects no series at all.
	if meta, ok := outputs["namespace"]; ok {
		var ns string
		if err := json.Unmarshal(meta.Value, &ns); err == nil && ns != "" {
			st.Values[databaseNamespaceKey] = ns
		}
	}
	// Grafana access (when monitoring was installed): stash the namespace/service so
	// the report step can print a port-forward hint. Null when --no-monitoring.
	if meta, ok := outputs["grafana_service"]; ok {
		var svc string
		if err := json.Unmarshal(meta.Value, &svc); err == nil && svc != "" {
			st.Values["grafanaService"] = svc
		}
	}
	if meta, ok := outputs["grafana_namespace"]; ok {
		var ns string
		if err := json.Unmarshal(meta.Value, &ns); err == nil && ns != "" {
			st.Values["grafanaNamespace"] = ns
		}
	}
	return nil
}

// Where applyInfra stashes what the infrastructure actually provisioned for
// backups (ADR-028, ADR-020 A2.5), for the Helm step to render the alerting rules
// against. Read back from the OpenTofu outputs rather than derived from dcctl's
// own flags — the flags are what was asked for, and these are what exists.
const (
	databaseBackupsKey       = "databaseBackups"
	databaseNamespaceKey     = "databaseNamespace"
	databaseBackupOffsiteKey = "databaseBackupOffsite"
)

// databaseNamespaceFor is where the database Clusters export their metrics from.
//
// It falls back to infraNamespace rather than to the empty string, and the
// difference matters more than a default usually does: an empty namespace label
// in a PromQL selector does not mean "any namespace", it matches only series whose
// namespace label is literally empty — of which there are none. So the fallback is
// the difference between alerts that work on an install whose outputs could not be
// read and alerts that silently select nothing.
func databaseNamespaceFor(st *State) string {
	if ns := st.Values[databaseNamespaceKey]; ns != "" {
		return ns
	}
	return infraNamespace
}

// infraVars builds the `-var` settings the infrastructure apply runs with, as
// "name=value" strings.
//
// Split out of applyInfra so it can be asserted on without a cluster or a tofu
// binary. The compact preset's volume sizing is checked against the ceilings the
// Helm step states (TestCompactReservationFitsItsSmallerVolume), and that check
// only means something if it reads the vars the apply actually passes.
func infraVars(st *State) []string {
	vars := []string{"kubeconfig_context=" + st.KubeContext}
	// The OpenTofu half of the HA topology. Emitted UNCONDITIONALLY, including for
	// the single-node case, so the two halves are rendered from one value on every
	// path rather than only when the flag is set — a conditional here would leave
	// the disagreement reachable again by the narrow route of turning HA off on an
	// instance that had it on. See haTopology.
	vars = append(vars, haFor(st.HA).infraVars()...)
	// On a kind/minikube node, ingress-nginx must bind the node's 80/443 via
	// hostPort; a LoadBalancer stays <pending> and times out the apply. The
	// monitoring stack likewise runs in its slim profile (emptyDir TSDB, smaller
	// requests) so it fits a local single-node cluster.
	if looksLocal(st.KubeContext) {
		vars = append(vars,
			"ingress_use_host_port=true",
			"monitoring_slim=true",
			// Expose MQTT as a NodePort on the port the embedded kind config maps
			// host 1883 to (deploy/local/kind-cluster.yaml: host 1883 -> node 31883),
			// so a device/tool on the host reaches the broker at ssl://127.0.0.1:1883
			// out of the box — the same host-port treatment :80/:443 already get.
			// Cloud leaves this 0 (ClusterIP only); a NodePort there would publish
			// MQTT on every node IP. The gate is looksLocal — the same context-NAME
			// heuristic that already sets ingress_use_host_port above, so a
			// false-positive here also visibly breaks ingress (a louder signal); and
			// the broker still terminates TLS + runs the auth callout, so an exposed
			// listener is not an open relay. A provider-based gate would be a stronger
			// signal than the name if this heuristic is ever tightened.
			"nats_mqtt_node_port=31883",
		)
	}
	// The observability stack is default-on (like Postgres/Timescale); --no-monitoring
	// skips it for a cluster that already has the Prometheus Operator.
	if st.NoMonitoring {
		vars = append(vars, "enable_monitoring=false")
	}
	// The CloudNativePG operator is likewise default-on (ADR-020 A2, decision D4:
	// one storage shape, HA or not). --no-cnpg is the escape hatch for a cluster that
	// already runs it — Helm refuses to adopt objects another installer created, so
	// on such a cluster the apply fails with an ownership error that no other flag
	// gets past. Skipping the operator necessarily skips the backup plugin: the
	// plugin is an extension of an operator that would not be there.
	// The cutover-guard hatch, emitted for BOTH stores from one flag.
	//
	// One flag rather than two because the two guards fire together: any instance
	// predating A2.3 carries both legacy StatefulSets, and an operator who has
	// dealt with one has dealt with the other in the same maintenance window.
	// Splitting them would mean discovering the second refusal after the first
	// apply, halfway through the cutover.
	if st.AllowLegacyDbRemoval {
		vars = append(vars,
			"allow_legacy_rdb_removal=true",
			"allow_legacy_tsdb_removal=true",
		)
	}
	if st.NoCNPG {
		vars = append(vars, "enable_cnpg=false", "enable_database_backups=false")
	}
	// The compact preset's volumes (compactSizing). The JetStream PV is DERIVED from
	// the stream ceilings helmInstall states, not chosen alongside them: every
	// per-stream ceiling is reserved UP FRONT at stream creation, so a volume smaller
	// than their sum crashloops the last services to start with "insufficient storage
	// resources available". Both halves come from the same `compact` value and
	// TestCompactReservationFitsItsSmallerVolume checks the sum against it — never
	// shrink one of these without the other.
	if st.Compact {
		// All THREE volumes, not two. The module stands up two Postgres StatefulSets
		// — the relational store and TimescaleDB — and telemetry lands in the second
		// one. Setting only postgres_storage shrinks the database that does not grow
		// and leaves the one that does at its full-size default, which makes the
		// preset's disk claim describe a fraction of the disk it uses.
		vars = append(vars,
			"nats_jetstream_storage="+compact.JetStreamStorage,
			"postgres_storage="+compact.PostgresStorage,
			"timescale_storage="+compact.TimescaleStorage,
			// The backup destination's volume. It joined this list when A2.5 made
			// enable_database_backups provision a destination rather than merely
			// installing the plugin — before that, compact's footprint genuinely
			// had no object store in it. Left out, a compact install would take the
			// 20Gi default for a preset whose whole point is small nodes, which is
			// the same bug TimescaleStorage was added to fix: shrink some volumes
			// and the preset's disk claim describes a fraction of the disk it uses.
			"backup_object_store_storage="+compact.ObjectStoreStorage,
			// Drop the prometheus-nats-exporter sidecar. It is a whole extra
			// container per NATS pod, and what it publishes is BROKER-side cluster
			// health — route state, RAFT peer health — which is information about a
			// topology compact does not have: compact runs one NATS server, so there
			// are no routes and no RAFT. The platform's own replication gauges are
			// exported by the services either way, so nothing that speaks about
			// stream replication is lost here.
			"nats_prom_exporter=false",
		)
		// cert-manager exists to issue the ingress certificate. Dropping it is only
		// safe because compact serves plain HTTP; an instance that still terminates
		// TLS needs it. With the chart's default self-signed issuer the chart renders
		// a cert-manager Issuer, so the install fails outright against a CRD that is
		// not installed; with selfSigned=false and a clusterIssuer it renders only an
		// annotation, so the install SUCCEEDS and the certificate is simply never
		// issued — quieter, and worse. Keyed on NoTLS rather than on Compact so
		// `--compact --no-tls=false` keeps a working cert either way.
		if st.NoTLS {
			vars = append(vars,
				"enable_cert_manager=false",
				// 🔴 SAME APPEND, DELIBERATELY. The Barman Cloud plugin (ADR-020 A2 /
				// ADR-028) renders a cert-manager Issuer and two Certificates, so it
				// inherits the failure above exactly: without the CRDs the release
				// fails outright and takes the whole bootstrap with it. These two vars
				// are emitted from one statement rather than two so that a later edit
				// cannot re-enable one without seeing the other — the same
				// by-construction shape the A8 credential fix landed on, and the
				// alternative is a coupling that exists only in a comment.
				//
				// The consequence is real and is the accepted trade, not an oversight:
				// `--compact --no-tls` gets the CNPG operator and NO point-in-time
				// recovery. Compact is the footprint preset, cert-manager is three more
				// workloads, and PITR additionally needs object storage; an install
				// that wants backups should not be asking for the smallest possible
				// one. TestCompactDropsCertManagerOnlyWhenTLSIsOff pins BOTH halves —
				// including that `--compact` WITH TLS keeps backups, which is the one
				// configuration hack/dr-rig.sh can run and still have something to
				// restore. (An earlier version of this comment cited a test by a name
				// nothing in the tree carried, so the coupling it promised rested
				// entirely on these two lines staying adjacent.)
				"enable_database_backups=false",
			)
		}
	}
	// The archive path each store OWNS (ADR-020 A2.5 / ADR-028), settled in
	// stepRenderConfig. Empty is the OpenTofu default — the Cluster's own name — and
	// is what every ordinary install emits, so the var is omitted rather than passed
	// empty.
	//
	// 🔴 This is read from the LIVE Cluster, not derived from this run's flags. See
	// resolveArchivePaths: passing a value derived from a one-shot restore flag would
	// mean a later flagless re-run silently retargets a live cluster's archiver.
	if v := st.Values["backupServerNameRdb"]; v != "" {
		vars = append(vars, "backup_server_name_rdb="+v)
	}
	if v := st.Values["backupServerNameTsdb"]; v != "" {
		vars = append(vars, "backup_server_name_tsdb="+v)
	}
	// The restore itself. Rebuild-time only: CloudNativePG reads `spec.bootstrap`
	// when it CREATES a Cluster, so these do nothing to a store that already exists
	// — stepRenderConfig says so out loud when it finds one.
	for _, v := range []struct{ name, value string }{
		{"restore_rdb_from", st.Restore.RdbFrom},
		{"restore_rdb_target_time", st.Restore.RdbTargetTime},
		{"restore_tsdb_from", st.Restore.TsdbFrom},
		{"restore_tsdb_target_time", st.Restore.TsdbTargetTime},
	} {
		if v.value != "" {
			vars = append(vars, v.name+"="+v.value)
		}
	}
	// Broker authentication (ADR-025): enable auth callout on NATS and pass the
	// minted public issuer + the bcrypt hash of the service password. The plaintext
	// password + seed go into the instance config in helmInstall; nats-server
	// bcrypt-compares the plaintext, so the broker and clients agree. (tfexec passes
	// vars as argv, no shell — the hash's `$` is literal.)
	if pub := st.Values["natsCalloutIssuerPublic"]; pub != "" {
		vars = append(vars,
			"nats_enable_auth=true",
			"nats_callout_issuer_public="+pub,
			"nats_service_password_bcrypt="+st.Values["natsServicePasswordBcrypt"],
		)
	}
	// Grafana SSO (ADR-047): configure Grafana's generic_oauth + the /grafana ingress
	// against the minted client secret and the computed URLs. The browser-facing
	// authorize URL uses the public host; token/userinfo are in-cluster (Grafana's pod
	// can't reach the public ingress). user-management gets the matching issuer + the
	// bcrypt hash of this same secret in helmInstall.
	if grafanaSSOEnabled(st) {
		u := grafanaSSOURLsFor(st)
		vars = append(vars,
			"monitoring_grafana_oauth_enabled=true",
			"monitoring_grafana_oauth_client_secret="+st.Values["grafanaOAuthSecret"],
			"monitoring_grafana_oauth_auth_url="+u.AuthURL,
			"monitoring_grafana_oauth_token_url="+u.TokenURL,
			"monitoring_grafana_oauth_api_url="+u.APIURL,
			"monitoring_grafana_root_url="+u.RootURL,
			"monitoring_grafana_ingress_host="+u.Host,
		)
	}
	return vars
}

// findTofu locates the OpenTofu (preferred) or Terraform CLI on PATH. Acquiring
// the binary automatically when absent is a follow-up; the preflight checks
// guide the user to install it for now.
func findTofu() (string, error) {
	for _, bin := range []string{"tofu", "terraform"} {
		if p, err := exec.LookPath(bin); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("neither 'tofu' nor 'terraform' found on PATH; install OpenTofu (https://opentofu.org) and re-run")
}

// instanceRoot returns the per-instance state root (~/.devicechain/<instance>)
// without creating it — used by destroy to remove all persisted state for an
// instance (tofu tfstate and friends).
func instanceRoot(instance string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".devicechain", instance), nil
}

// instanceStateDir returns a stable, per-instance directory under the user's
// home for persistent bootstrap state (e.g. ~/.devicechain/<instance>/<sub>),
// creating it if necessary.
//
// 🔴 THE MODE IS THE PROTECTION, and it protects a file this code does not write.
// OpenTofu's local backend puts terraform.tfstate in here, and tfstate is not a
// summary of the infrastructure — it is the infrastructure's values, in cleartext,
// including the database superuser password and the NATS server's TLS PRIVATE KEY.
// It was shipping at 0644 inside 0755 directories, so on any machine with a second
// account those were readable by everyone on the box.
//
// Tightening the constant alone would have fixed only fresh installs: MkdirAll
// applies its mode to directories it CREATES and silently leaves an existing one
// as it found it, so every instance bootstrapped before this change would have
// stayed world-readable while the code claimed otherwise. Hence the explicit walk
// back down over each level.
func instanceStateDir(instance, sub string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".devicechain", instance, sub)
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		return "", err
	}
	// Every level, not just the leaf: ~/.devicechain itself is the one an older
	// dcctl created at 0755, and a private leaf under a traversable parent is
	// still private — but the parent also holds the escrow directory and every
	// other instance, so it is worth owning.
	for _, p := range []string{
		filepath.Join(home, ".devicechain"),
		filepath.Join(home, ".devicechain", instance),
		dir,
	} {
		if err := os.Chmod(p, stateDirMode); err != nil {
			return "", fmt.Errorf("restricting permissions on %s: %w", p, err)
		}
	}
	return dir, nil
}

// hardenStateFiles tightens anything OpenTofu wrote into the working directory
// that this process did not create itself.
//
// The directory mode above is the real protection; this is the second layer, and
// it exists because the file is written by a tool whose permission choices are
// not ours to assume. It runs AFTER the apply, deliberately: a chmod before the
// apply would be undone by the write it was meant to protect.
//
// Missing files are not an error. A dry run never produces a state file, and
// there is no backup until the second apply.
func hardenStateFiles(workdir string) error {
	for _, name := range []string{"terraform.tfstate", "terraform.tfstate.backup"} {
		p := filepath.Join(workdir, name)
		if err := os.Chmod(p, stateFileMode); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("restricting permissions on %s: %w", p, err)
		}
	}
	return nil
}

// extractFS writes every file in an embedded fs.FS into dir, recreating the
// directory structure. Existing files are overwritten; files already in dir but
// not in the FS (e.g. terraform.tfstate) are left untouched.
func extractFS(src fs.FS, dir string) error {
	return fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, path)
		if d.IsDir() {
			if err := os.MkdirAll(target, stateDirMode); err != nil {
				return err
			}
			// The same reason as the file chmod below, and it was missed on the
			// first pass: MkdirAll leaves an EXISTING directory exactly as it
			// found it. modules/ and its children are created once and then
			// re-extracted over on every run, so on an instance bootstrapped by
			// an older dcctl every one of them stays 0755 forever. A `find -perm
			// /0077` over a real instance directory is what surfaced it — the
			// files were all 0600 and the directories holding them were not.
			return os.Chmod(target, stateDirMode)
		}
		b, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}
		// The .tf files themselves are not secret — they ship in the binary and
		// in the public repo. They get the restrictive mode anyway so there is
		// ONE rule for everything under this directory, rather than a rule with
		// an exception that the next file added here has to know about.
		//
		// WriteFile's mode applies only when it CREATES the file, so a re-run
		// over an older install's 0644 copies would leave them as they were.
		if err := os.WriteFile(target, b, stateFileMode); err != nil {
			return err
		}
		return os.Chmod(target, stateFileMode)
	})
}
