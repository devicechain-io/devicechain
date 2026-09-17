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
	"strings"
	"time"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/devicechain-io/dcctl/dcdir"
	"github.com/hashicorp/terraform-exec/tfexec"
)

const (
	// stateDirMode and stateFileMode keep ~/.devicechain/instances/<instance> readable only
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
// tofuGracefulStopBudget is how long a cancelled tofu is given to finish its
// current operation and write state before it is killed. It must exceed the
// longest single resource timeout in the infrastructure root (900s today) or the
// kill lands in the middle of exactly the slow operation it was sized for.
const tofuGracefulStopBudget = 20 * time.Minute

// applyInfra brings the cluster's shared prerequisites and this instance's own
// infrastructure up, in that order.
//
// 🔴 THE ORDER IS THE ONLY THING ENFORCING A DEPENDENCY OPENTOFU USED TO ENFORCE
// FOR US. One root and one graph used to order "install the operator" before
// "create a database Cluster", and "install cert-manager" before "install the backup
// plugin that renders an Issuer". Two roots are two graphs, so the edge between them
// is this function's sequence and nothing else. That is why the two applies live
// behind one call rather than being two pipeline steps a future edit could reorder
// or run selectively.
//
// 🔑 AND IT IS WHAT MAKES A SECOND INSTANCE CHEAP RATHER THAN DANGEROUS. The
// prerequisite root is keyed on the CLUSTER, so the second bootstrap against one
// cluster re-applies the same state convergently — a no-op — instead of building a
// second ingress controller and a second relational database.
func applyInfra(ctx context.Context, st *State) (err error) {
	if st.ClusterUID == "" {
		return fmt.Errorf("the cluster's identity is not known, so dcctl cannot tell which " +
			"cluster's shared prerequisite state to use. Refusing rather than falling back to " +
			"the kube-context name: a cluster deleted and recreated wears the same context name, " +
			"and state filed under it would be inherited by a cluster holding none of those " +
			"resources")
	}

	// Every value dcctl decides, computed ONCE and then routed by which root declares
	// it. See splitVars for why this is computed rather than two hand-kept lists.
	clusterVars, instanceVars, err := splitVars(infraVars(st))
	if err != nil {
		return err
	}

	// 🔴 THE INSTANCE ROOT IS OPENED, AND ITS FENCES RUN, BEFORE ANYTHING IS WRITTEN.
	// The fences refuse an instance whose state this build would damage — and the
	// most important of them, the pre-split fence, guards against exactly the state
	// in which the CLUSTER apply below cannot succeed: an instance built before the
	// split already runs the operator, ingress and shared database as Helm releases
	// its own state owns, so a cluster root applied first dies on "cannot re-use a
	// name that is still in use" and the operator is handed a Helm error instead of
	// the explanation. A refusal has to come before the first thing it refuses.
	inst, err := openInstanceRoot(ctx, st)
	if err != nil {
		return err
	}
	defer func() {
		if herr := hardenStateFiles(inst.rootdir); herr != nil && err == nil {
			err = herr
		}
	}()

	// The shared infrastructure namespace and every minted credential, BEFORE either
	// apply.
	//
	// 🔴 IT HAS TO EXIST BEFORE THE APPLIES BECAUSE THE CREDENTIALS DO. CloudNativePG
	// builds a database role from a Secret when it CREATES the Cluster, so a Secret
	// written afterwards leaves the role on one password and every service on another
	// — and a Secret cannot be written into a namespace that is not there.
	//
	// 🔑 IT MOVED UP HERE RATHER THAN INTO ONE OF THE APPLIES, because after the
	// split BOTH roots need it: the shared relational store is created by the cluster
	// root and the event store by the instance root, and each reads its credentials
	// Secret out of this one namespace.
	_, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster to prepare the infrastructure namespace: %w", err)
	}
	if err := checkRelationalStoreOwner(ctx, st.KubeContext); err != nil {
		return err
	}
	if err := ensureInfraNamespace(ctx, typed, infraNamespace); err != nil {
		return err
	}
	// 🔴 AND THE INSTANCE'S OWN NAMESPACE, for the same reason: its credentials, and the
	// broker and event store built from them, live there. Created carrying the metadata
	// the instance's Helm release adopts it with, exactly as the Helm step would have.
	if err := ensureNamespaceForRelease(ctx, typed, st.Instance, helmReleaseNameFor(st.Instance), helmReleaseNamespace); err != nil {
		return err
	}
	if err := writeMintedSecrets(ctx, typed, st); err != nil {
		return err
	}

	// 🔴 THE RECORD BRACKETS THE APPLY: "applying" before it, "installed" only after it
	// succeeds. See installrecord.go for why a record written once, at the end, reads a
	// failed re-install as a finished one.
	if err := markInstallApplying(ctx, typed, st.ClusterUID, st.DcctlVersion, time.Now); err != nil {
		return err
	}
	archive, rdb, err := applyClusterPrereqs(ctx, st, st.ClusterUID, clusterVars, infraNamespace)
	if err != nil {
		return err
	}
	if err := writeInstalled(ctx, typed, InstallRecord{
		ClusterUID:   st.ClusterUID,
		DcctlVersion: st.DcctlVersion,
		Settings:     installSettingsFor(st),
		Outputs:      installOutputsFrom(st, archive, rdb),
	}, time.Now); err != nil {
		return err
	}

	// 🔴 THIS INSTANCE'S OWN LOGIN AND DATABASE, BEFORE ITS OWN INFRASTRUCTURE. After the
	// shared store exists, because it is created on it; before the instance root, because
	// the refusal it can raise — a database by this name that some other identity owns —
	// has to come before anything of this instance's is built on top of it.
	if err := provisionInstanceDatabase(ctx, st, rdb); err != nil {
		return err
	}

	// The archive contract, READ BACK from the root that owns the object store rather
	// than recomputed here. Appended after the instance's own variables so that what
	// the cluster actually built wins over anything derived from this run's flags.
	return applyInstanceInfra(ctx, st, inst.tf, append(instanceVars, archive.archiveVars()...))
}

// instanceRoot is the instance root, extracted, initialised and fenced — ready to apply.
type openedInstanceRoot struct {
	tf      *tfexec.Terraform
	rootdir string
}

// openInstanceRoot extracts the instance root, initialises it, and runs every refusal
// that reads its state. It changes nothing in the cluster, which is what lets
// applyInfra run it before anything else.
//
// The caller owns hardening the state files under rootdir, and must register that
// the moment this returns successfully; on an error return this hardens them itself.
func openInstanceRoot(ctx context.Context, st *State) (_ openedInstanceRoot, err error) {
	tofuBin, err := findTofu()
	if err != nil {
		return openedInstanceRoot{}, err
	}

	workdir, err := instanceStateDir(st.Instance, "infra")
	if err != nil {
		return openedInstanceRoot{}, err
	}
	// The tree extracted below holds this root plus the shared modules, and a root
	// reaches those as "../modules/<x>". So tofu runs one level down, in the root's
	// own directory, and the state it keeps lives there with it.
	rootdir := filepath.Join(workdir, assets.InstanceRootDir)
	// Deferred, and registered the moment the directory exists, because a FAILED
	// apply writes state too — a partial apply is exactly the run that leaves
	// resource attributes on disk, and the path that returns early is the one a
	// call placed after the apply would skip. A chmod error only surfaces when
	// nothing else went wrong; the apply's own error is always the better one to
	// hand back.
	defer func() {
		if err == nil {
			return // the caller hardens from here on, after the apply writes state
		}
		_ = hardenStateFiles(rootdir)
	}()
	if err := extractRoot(assets.OpenTofu(), assets.InstanceRootDir, workdir); err != nil {
		return openedInstanceRoot{}, fmt.Errorf("extracting infrastructure config: %w", err)
	}
	// 🔴 BEFORE ANY tofu CALL, AND THE ORDER IS NOT COSMETIC. Init does not read
	// state, but the retired-infrastructure fence immediately after it does — and a
	// fence reading an empty state concludes there is nothing to fence.
	if err := relocateRootState(workdir, rootdir); err != nil {
		return openedInstanceRoot{}, err
	}
	if err := removeSupersededRootConfig(workdir); err != nil {
		return openedInstanceRoot{}, err
	}

	tf, err := tfexec.NewTerraform(rootdir, tofuBin)
	if err != nil {
		return openedInstanceRoot{}, err
	}
	// Stream tofu's own progress so a long apply is not a silent wait.
	tf.SetStdout(os.Stdout)
	tf.SetStderr(os.Stderr)

	// 🔴 GIVE A CANCELLED APPLY LONG ENOUGH TO STOP THE WAY IT WANTS TO.
	// terraform-exec cancels by sending SIGINT — tofu's graceful stop, which
	// finishes the operation in flight and writes state — and then escalates to
	// SIGKILL once WaitDelay expires. The default is 60 seconds, and the operation
	// in flight here is routinely a helm_release whose own timeout is 600 or 900
	// seconds, so the default turns every interrupt during a slow release back
	// into the SIGKILL that loses the state file.
	//
	// The cost of the larger value is that an interrupt during a genuinely stuck
	// apply does not return the terminal promptly. That is the right trade only
	// BECAUSE a second interrupt exits immediately — and that is a property
	// cmd.Execute has to arrange deliberately, not one signal.NotifyContext
	// provides. An earlier version of this comment cited it as a given; it was
	// wrong, and this number is exactly what made that mistake expensive rather
	// than cosmetic. If that escape hatch is ever removed, this budget must shrink
	// with it.
	//
	// 🔴 A SECOND CONSEQUENCE, WITH NO INTERRUPT INVOLVED. WaitDelay also bounds
	// how long Wait blocks for the child's stdout/stderr pipes to close after it
	// exits, and terraform-exec reads through pipes. A provider plugin that
	// outlives tofu holding the inherited pipe therefore hangs dcctl for this
	// budget rather than the default minute. The same escape hatch applies, and
	// the trade is the same one: a rare long hang is preferable to routinely
	// killing an apply that was about to write its state.
	tf.SetWaitDelay(tofuGracefulStopBudget)

	if err := tf.Init(ctx); err != nil {
		return openedInstanceRoot{}, fmt.Errorf("tofu init: %w", err)
	}

	// 🔴 THE FENCE COMES FIRST, BEFORE ANY OTHER READ OR WRITE. An instance built
	// before the credentials moved into Secrets dcctl owns still has the resources
	// that used to hold them in its state, and this configuration no longer declares
	// them — so the apply below would DELETE them. Everything after this point
	// assumes it did not fire. Needs state, so it runs after Init.
	if err := checkNoRetiredInfrastructure(ctx, tf, st.Instance); err != nil {
		return openedInstanceRoot{}, err
	}

	// 🔴 AND THE SECOND FENCE, FOR THE SECOND TIME THIS ROOT STOPPED DECLARING
	// THINGS ITS STATE STILL HOLDS. An instance built before the cluster
	// prerequisites moved to their own root has them in THIS state, and this
	// configuration no longer declares them — so the apply below would destroy the
	// CNPG operator, ingress, cert-manager, monitoring, the object store and the
	// shared relational database. Same position and same reason as the fence above:
	// after Init because it reads state, before everything else because everything
	// else assumes it did not fire.
	if err := checkNoPreSplitInfrastructure(ctx, tf, st.Instance); err != nil {
		return openedInstanceRoot{}, err
	}

	// 🔴 AND THE THIRD: an instance whose broker and event store were built in the
	// shared namespace, before each instance had its own. Moving them is a replacement.
	if err := checkInstanceInItsOwnNamespace(ctx, tf, st.Instance); err != nil {
		return openedInstanceRoot{}, err
	}

	// Refuse to shrink a broker cluster that is already carrying replicated data.
	// Reads the CURRENT state, so it must run after Init and before Apply — this is
	// the only point where both the applied topology and the requested one are known.
	if err := checkHaNotTornDown(ctx, st, tf); err != nil {
		return openedInstanceRoot{}, err
	}
	return openedInstanceRoot{tf: tf, rootdir: rootdir}, nil
}

// applyInstanceInfra applies the instance root openInstanceRoot prepared, and records
// what it built.
func applyInstanceInfra(ctx context.Context, st *State, tf *tfexec.Terraform, vars []string) error {

	opts := make([]tfexec.ApplyOption, 0, len(vars))
	for _, v := range vars {
		opts = append(opts, tfexec.Var(v))
	}
	if err := applyWithCNPGAdmissionRetry(ctx, tf, opts, "tofu apply", func(ctx context.Context) error {
		return waitForCNPGAdmission(ctx, st.KubeContext, cnpgAdmissionTimeout)
	}); err != nil {
		return err
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
	cnpgNamespaceKey         = "cnpgNamespace"
	// mqttNodePortHolderKey is the namespace of another instance already holding the
	// local MQTT node port, set by the render step. Empty means this instance may take it.
	mqttNodePortHolderKey = "mqttNodePortHolder"
)

// databaseNamespaceFor is where the SHARED relational store exports its metrics from.
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
	vars := []string{
		"kubeconfig_context=" + st.KubeContext,
		// The event store's one database is named after the instance, and initdb
		// creates it: every service connects to the database named after the instance
		// and none of them creates it.
		"timescale_database=" + st.Instance,
		// The broker and the event store run in the instance's own namespace.
		"instance_namespace=" + instanceNamespace(st.Instance),
	}
	// The broker's certificate authority, PUBLIC HALF ONLY.
	//
	// 🔴 THE DIRECTION OF THIS ARROW IS THE POINT. The authority used to be created
	// BY the apply — `tls_private_key.ca` — which put its private key in the
	// infrastructure state in cleartext, and made the CA an OUTPUT the Helm step had
	// to wait for. dcctl mints it now, so what crosses this boundary is a
	// certificate anyone may hold, and the key that signs with it never leaves this
	// process. The module still renders the CA-only ConfigMap its chart references;
	// it just receives the material instead of generating it.
	if st.NATSTLS != nil {
		vars = append(vars, "nats_ca_cert_pem="+st.NATSTLS.CACertPEM)
	}
	// The off-site archive, when one was supplied. Everything here is an ADDRESS
	// rather than a credential — the two keys travel in a Secret dcctl writes, and
	// the variables that used to carry them are gone.
	//
	// 🔴 EMITTED ONLY WHEN A DESTINATION WAS SUPPLIED, because the default is
	// in-cluster and stating it here on every run would make `backup_destination`
	// something dcctl decides rather than something the operator does.
	if d := st.BackupDestination; d.Configured() {
		vars = append(vars,
			"backup_destination=external",
			"backup_endpoint_url="+d.EndpointURL,
			"backup_bucket_rdb="+d.BucketRdb,
			"backup_bucket_tsdb="+d.BucketTsdb,
		)
	}
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
		)
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
		//
		// 🔴 ONLY ONE INSTANCE PER CLUSTER CAN HAVE IT: a node port is cluster-wide, and
		// an apply asking for one another Service holds fails. The first instance keeps
		// it; the others get none, and the render step says so.
		if st.Values[mqttNodePortHolderKey] == "" {
			vars = append(vars, fmt.Sprintf("nats_mqtt_node_port=%d", localMQTTNodePort))
		}
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
		// The system-account login's hash. Passed separately from the service one
		// because the broker places them in different accounts, and omitted entirely
		// when no system password was minted — the module then renders SYS with no
		// users, which is what every instance before this change ran.
		if h := st.Values["natsSysPasswordBcrypt"]; h != "" {
			vars = append(vars, "nats_sys_password_bcrypt="+h)
		}
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

// instanceRoot returns the per-instance state root (~/.devicechain/instances/<instance>)
// without creating it — used by destroy to remove all persisted state for an
// instance (tofu tfstate and friends).
func instanceRoot(instance string) (string, error) {
	// 🔴 NOT VALIDATED HERE, AND THAT IS A CORRECTION. Putting ValidateInstanceName in
	// this funnel looked right and quietly DISARMED a guard: resolveEscrowPath treats an
	// error from this function as "no home directory — not this check's problem" and
	// returns the path as acceptable, so a rejected name skipped the containment check
	// that stops an escrow artifact being written where destroy will delete it. Two
	// callers encode that same assumption about what an error here means.
	//
	// So the name is validated where a NEW one enters (cmd/bootstrap.go) and again in
	// WriteInstanceRecord, and never on the cleanup paths — because whatever is already
	// on disk has to remain destroyable, including anything created before this existed.
	return dcdir.Instance(instance)
}

// instanceStateDir returns a stable, per-instance directory under the user's
// home for persistent bootstrap state (e.g. ~/.devicechain/instances/<instance>/<sub>),
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
	root, err := dcdir.Root()
	if err != nil {
		return "", err
	}
	instances, err := dcdir.Sibling(dcdir.Instances)
	if err != nil {
		return "", err
	}
	instanceDir, err := dcdir.Instance(instance)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(instanceDir, sub)
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		return "", err
	}
	// Every level, not just the leaf: ~/.devicechain itself is the one an older
	// dcctl created at 0755, and a private leaf under a traversable parent is
	// still private — but the parent also holds the escrow directory and every
	// other instance, so it is worth owning.
	for _, p := range []string{
		root,
		instances,
		instanceDir,
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
func hardenStateFiles(rootdir string) error {
	for _, name := range stateFileNames {
		p := filepath.Join(rootdir, name)
		if err := os.Chmod(p, stateFileMode); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("restricting permissions on %s: %w", p, err)
		}
	}
	return nil
}

// stateFileNames are the files OpenTofu's local backend keeps beside a root. The
// backup appears only from the second apply onward.
var stateFileNames = []string{"terraform.tfstate", "terraform.tfstate.backup"}

// relocateRootState moves an instance's existing state down into the root
// directory tofu now runs in.
//
// # WHY THIS EXISTS, AND WHY IT IS A MOVE RATHER THAN A REFUSAL
//
// The configuration used to be a single root of .tf files at the top of the
// extracted tree, so tofu ran in the working directory and the local backend kept
// terraform.tfstate there. Roots are now peers under a shared modules tree, so
// tofu runs one level down — and state beside the old location would simply not be
// found. An empty state is not an error to OpenTofu: it is a fresh install, and
// the apply would set about CREATING an instance's entire infrastructure on top of
// the infrastructure already running.
//
// 🔑 THE DISTINCTION THAT DECIDES MOVE-VS-REFUSE IS WHETHER THE STATE'S CONTENT IS
// STILL TRUE. Here nothing about what the state DESCRIBES has changed — the same
// resources, the same addresses, the same root, one directory further down. That is
// a path change, and a path change is repairable without an operator. When the roots
// are actually SPLIT, the content stops being true — addresses leave this root's
// configuration for another's — and no rename repairs that; per the tier-1 spec
// that case is refused outright, the way the retired-infrastructure fence refuses.
// Relocating here does not soften that; it removes a failure that has nothing to do
// with it.
//
// 🔴 REFUSE WHEN BOTH EXIST rather than choosing. Two state files for one root is
// not a situation this code can reason about — it means an interrupted move, or two
// binaries disagreeing about where state lives — and picking either one risks an
// apply against a state that does not describe the running instance. The operator
// can see both files and decide; this function cannot.
//
// Nothing is moved for a fresh instance, and the second run finds nothing left to
// move, so this is a one-time repair that then costs a stat.
//
// The provider cache and lock file are deliberately NOT moved: `tofu init` rebuilds
// both in the new root, and a stale .terraform beside the old path is inert.
func relocateRootState(workdir, rootdir string) error {
	for _, name := range stateFileNames {
		from := filepath.Join(workdir, name)
		to := filepath.Join(rootdir, name)

		if _, err := os.Stat(from); err != nil {
			if os.IsNotExist(err) {
				continue // fresh instance, or already moved
			}
			return fmt.Errorf("reading %s: %w", from, err)
		}
		if _, err := os.Stat(to); err == nil {
			return fmt.Errorf(
				"instance %q has two %s files and this build cannot tell which describes the "+
					"running infrastructure: one at %s, where earlier builds kept it, and one at %s, "+
					"where this build keeps it. Applying against the wrong one would rebuild "+
					"infrastructure that already exists. Compare them and delete the stale one — the "+
					"live one is whichever a `tofu show` describes as the instance you are running",
				filepath.Base(workdir), name, from, to)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("reading %s: %w", to, err)
		}

		if err := os.MkdirAll(rootdir, stateDirMode); err != nil {
			return fmt.Errorf("creating %s: %w", rootdir, err)
		}
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("moving %s to %s: %w", from, to, err)
		}
	}
	return nil
}

// removeSupersededRootConfig deletes the .tf files an earlier dcctl left at the
// top of an instance's working directory.
//
// 🔴 EXTRACTION ONLY EVER WRITES. extractFS recreates the embedded tree over
// whatever is already in the directory and deletes nothing, which is right for a
// refresh and wrong across a LAYOUT change: the roots moved into their own
// directories, so the .tf files an older binary wrote at the top are no longer
// overwritten by anything. They just stay.
//
// 🔴 AND WHAT THEY LEAVE BEHIND IS A LOADED GUN, not clutter. relocateRootState
// has just moved the state down into the root directory, so that top-level
// directory now holds a COMPLETE, STALE configuration describing an entire
// DeviceChain infrastructure — with NO state beside it. A `tofu plan` run there
// reads an empty state and an intact configuration, and answers that it will
// CREATE all of it: a second broker, two more database Clusters, another object
// store, against the cluster the instance is already running on. Nothing about
// that output looks like a mistake.
//
// It is not a hypothetical invocation either. That directory is where hand-runs
// were documented, and an operator debugging an instance goes to the directory
// they know. MEASURED against a real instance's ~/.devicechain/instances/<instance>/infra
// during the layout change: after extract + relocate, five stale .tf files
// remained at the top with the state gone from under them.
//
// Removing only *.tf, and only at the top level, is deliberate: the new tree puts
// nothing there, so anything matching is by definition superseded. A stale
// .terraform cache and lock file may remain and are inert — with no configuration
// to load, tofu in that directory fails cleanly instead of planning something.
func removeSupersededRootConfig(workdir string) error {
	entries, err := os.ReadDir(workdir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", workdir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tf") {
			continue
		}
		if err := os.Remove(filepath.Join(workdir, e.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing superseded %s: %w", e.Name(), err)
		}
	}
	return nil
}

// extractRoot extracts ONE root and the shared modules it reaches as ../modules/<x>.
//
// 🔴 NEVER THE WHOLE TREE, BECAUSE A ROOT'S CONFIGURATION WITH NO STATE IS A LOADED
// GUN. Each root is applied from its own state directory, so extracting every root
// there leaves the OTHER root sitting beside it: a complete, initialisable
// configuration with an empty state, in a directory an operator debugging an install
// is told to run tofu in. A `tofu plan` in the wrong one plans to CREATE a second
// operator, ingress and relational database — or a second broker and event store —
// against the live cluster, and nothing in that output looks like a mistake.
// removeSupersededRootConfig documents the same shape for the pre-split layout.
func extractRoot(src fs.FS, root, dir string) error {
	for _, keep := range []string{root, "modules"} {
		// fs.Sub does not check the directory exists, and a root extracted as an
		// empty directory would fail at init with an error about providers rather
		// than about a missing root.
		if _, err := fs.Stat(src, keep); err != nil {
			return fmt.Errorf("the embedded OpenTofu tree has no %q: %w", keep, err)
		}
		part, err := fs.Sub(src, keep)
		if err != nil {
			return err
		}
		if err := extractFS(part, filepath.Join(dir, keep)); err != nil {
			return err
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
