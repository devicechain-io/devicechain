// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/devicechain-io/dcctl/dcdir"
	"github.com/fatih/color"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// DefaultClusterName is the local cluster `dcctl install local` creates, and the one
// `dcctl bootstrap local` builds on, when neither is told another.
const DefaultClusterName = "devicechain"

// defaultMaxConnections is the relational store's connection budget on a first install
// that does not ask for one. See postgres_max_connections in the cluster root.
const defaultMaxConnections = 600

// InstallOptions drives `dcctl install`.
type InstallOptions struct {
	Options
	// NoMonitoring skips installing the kube-prometheus-stack observability stack
	// (default-on). Set it when the cluster already has the Prometheus Operator, or
	// to opt out of in-cluster metrics collection; instances on the cluster then render
	// no ServiceMonitors or alerts.
	NoMonitoring bool
	// NoCNPG skips installing the CloudNativePG operator and the Barman Cloud backup
	// plugin (default-on, ADR-020 A2). Set it when the cluster ALREADY runs CNPG —
	// which is not a corner case: the upstream `kubectl apply` manifest is the most
	// common way to install it, and Helm cannot adopt objects it did not create, so
	// without this flag such a cluster fails the cluster apply with an ownership error
	// and no way past it.
	NoCNPG bool
	// Compact applies the small-footprint preset to the cluster and every instance
	// built on it: lowered JetStream/KV ceilings, the smaller volumes those permit, and
	// lowered scheduling requests. It is a preset over levers that already exist and
	// does NOT change which services run; a bootstrap on a compact cluster keeps the
	// default profile or a smaller one. See compactSizing.
	Compact bool
	// HA provisions the ADR-020 topology: a replicated relational store (synchronous,
	// A2.3) for the cluster, and for every instance built on it a 3-node NATS RAFT
	// cluster spread one server per node, with every JetStream stream and KV bucket
	// replicated across it, and a replicated event store at `preferred` durability
	// (A2.4). It is ONE value driving both tools — see haTopology for why that
	// matters. It does not change how many DeviceChain services run: the stateful
	// areas are pinned to one writer by the ADR-070 lease fence.
	HA bool
	// BackupDestination is an off-site archive the operator already owns. Nil means
	// the in-cluster object store.
	BackupDestination *BackupDestination
	// BackupSnapshotClass takes each database's scheduled base backup as a CSI volume
	// snapshot with this VolumeSnapshotClass instead of a full copy in the backup
	// store; a weekly copy still goes to the store. Empty keeps object-store base
	// backups. Every instance on the cluster follows it.
	BackupSnapshotClass string
	// DatabasePlacement confines the relational store, and the event store of every
	// instance bootstrapped on the cluster, to the nodes it names. Settled from argv by
	// ParseDatabasePlacement; the zero value places nothing.
	DatabasePlacement DatabasePlacement
	// MaxConnections is the relational store's connection budget. Zero keeps what the
	// cluster was installed with, or the default on a first install.
	MaxConnections int
	// Restore recovers the CLUSTER's shared relational store from an archive instead
	// of initialising an empty one (ADR-028). The zero plan is an ordinary install.
	//
	// Settled in the command layer by ResolveRestorePlan, before this cluster exists,
	// because every way the flags can be wrong is knowable from argv alone. Only the
	// Rdb half is ever set here: the event store belongs to an INSTANCE, and `dcctl
	// bootstrap` carries that half.
	Restore      RestorePlan
	DcctlVersion string
}

// Install prepares a cluster for instances: it creates or names the cluster, applies
// the cluster prerequisites, makes the base database identity, and records the install.
//
// 🔴 THE RECORD IS WRITTEN LAST, AND ONLY ON SUCCESS. `dcctl bootstrap` refuses a
// cluster without an `installed` record and builds every instance from what it says,
// so a record that claimed an install which did not finish would put instances on
// prerequisites that are not there. markInstallApplying brackets the apply for exactly
// that reason.
func Install(ctx context.Context, provider Provider, opts InstallOptions) error {
	opts.CreateCluster = true
	binding, err := provider.EnsureCluster(ctx, opts.Options)
	if err != nil {
		return err
	}

	st := installState(binding, provider.Name(), opts)
	settings := installSettingsFor(st)

	// 🔴 CHECKED BEFORE THE FIRST LINE IS PRINTED, INCLUDING UNDER --dry-run. The
	// command layer settles this from argv, so a dcctl run cannot reach here
	// unresolved — but Install is also the engine, and a caller that built its
	// options by hand would otherwise have the heading below announce
	// "Operator: /operator:", a syntactically valid reference that pulls nothing,
	// and a rehearsal would print a plan the real run refuses. A rehearsal must
	// refuse what the run refuses.
	if err := requireResolvedImages(st, "installing the operator"); err != nil {
		return err
	}

	fmt.Println(GreenUnderline(fmt.Sprintf("\nInstall DeviceChain prerequisites on cluster %s", binding.Describe())))
	fmt.Printf("  %s %s\n", color.WhiteString("Settings:"), color.GreenString(describeInstallSettings(settings)))
	reportOperatorPlan(st)

	// 🔴 THE LOCK IS TAKEN BEFORE THE READS THAT DECIDE, not just before the writes.
	// Everything below — the previous install record, whether a re-install would
	// hurt, what the relational store's archive path already is — is read from the
	// cluster and then acted on, and a concurrent bootstrap is exactly what makes
	// such a read stale between the asking and the acting.
	//
	// 🔴 UNTIL THIS RELEASE `dcctl install` TOOK NO LOCK AT ALL. That was survivable
	// only while install wrote nothing cluster-scoped that bootstrap also wrote: it
	// now installs the operator, so a lockless install racing a locked bootstrap
	// would be the same hole this Lease exists to close, relocated into the verb
	// that was supposed to own the resource.
	//
	// ClaimCluster is shared with the bootstrap pipeline rather than reimplemented
	// here: it creates the operator's namespace (idempotently, reading the name from
	// the rendered overlay rather than a constant) and then acquires. Two
	// implementations of "where does the cluster lock live" is how a lock ends up
	// protecting nothing.
	//
	// A dry run takes nothing. It still resolves the namespace and reports the claim
	// it WOULD have met, because "another operator is already running" is part of
	// the answer to what this command would do.
	if err := claimForInstall(ctx, st); err != nil {
		return err
	}
	// Released on every exit, including a refusal below. A CLI that has exited holds
	// nothing, and leaving the Lease behind would send the operator's own retry down
	// the reclaim path for no reason. Release detaches from ctx itself, so a Ctrl+C
	// still hands the lock back.
	defer func() {
		if st.Claim != nil {
			st.Claim.Release(ctx)
		}
	}()

	if err := checkHaNodeCapacity(ctx, st); err != nil {
		return err
	}
	// Before the dry-run branch and before every write: a placement the nodes cannot
	// hold is refused with nothing taken but the claim. Counted against the default
	// instance count for --ha; the cnpg-cluster module counts against the real one.
	if err := checkDatabasePlacement(ctx, st); err != nil {
		return err
	}
	if st.DryRun {
		// Build, then operator, then the long OpenTofu apply — the order the real
		// run performs them in. A rehearsal whose steps are in a different order
		// from the run is a rehearsal of something else.
		//
		// (The relational-restore sentence below is the exception, and it is
		// deliberate: the real run prints it earlier, before the apply, because an
		// operator mid-incident has to be told which way it will go while they can
		// still stop. It is a warning, not a step.)
		//
		// The re-install refusals come first, as they do in the real run (identify,
		// refuse, then build): a plan printed above a refusal is a plan the run
		// will not carry out.
		rehearsed, err := rehearseInstallRefusals(ctx, st, settings, localClusterStateExists)
		if err != nil {
			return err
		}
		if err := buildOperatorImageForInstall(ctx, st); err != nil {
			return err
		}
		if class := backupSnapshotClass(st); class != "" && !rehearsed {
			wouldDo("refuse, before any write, a VolumeSnapshotClass " + class + " that does not exist, " +
				"does not use deletionPolicy Delete, or belongs to another driver than the database volumes' " +
				"-- NOT rehearsed, because the cluster could not be identified")
		}
		if p := databasePlacement(st); !p.IsZero() {
			wouldDo("place the relational store, and every instance's event store, on " + p.describe())
		}
		wouldDo("install the operator — CRDs, RBAC and the controller Deployment at " +
			operatorImageRef(st) + " — in namespace " + st.OperatorNamespace)
		wouldDo("tofu init+apply deploy/opentofu/cluster — once per cluster, shared by every instance " +
			"(CloudNativePG operator + backup plugin, ingress, cert-manager, monitoring, the relational " +
			"store, the backup object store)")
		wouldDo("create the base database identity instances' logins are made with")
		wouldDo(fmt.Sprintf("record the install in ConfigMap %s/%s", infraNamespace, installRecordName))
		// 🔴 A RESTORE IS THE ONE THING A REHEARSAL MOST NEEDS TO BE TOLD ABOUT, and
		// the reason it is rendered from the PLAN plus a READ rather than from the
		// apply's outputs is that a dry run has no outputs: this project has already
		// shipped a --dry-run that printed "Backups: NONE" for a cluster that
		// archives, because it read back a value an apply it never ran would have set.
		// Both inputs here exist before anything is applied.
		//
		// Printed the way the real run prints it, not through wouldDo: these are
		// sentences, and one of them says a restore will NOT happen, which "would" in
		// front of it would turn into its own opposite.
		printRelationalRestore(st.Restore, dryRunRdbArchiveState(ctx, st), backupSnapshotClass(st))
		return nil
	}

	uid, err := identifyCluster(ctx, binding.KubeContext)
	if err != nil {
		return fmt.Errorf("reading the identity of cluster %s: %w\n"+
			"  dcctl files this cluster's prerequisite state under that identity, so it cannot "+
			"install them without it", binding.Describe(), err)
	}
	st.ClusterUID, st.Binding.ClusterUID = uid, uid
	if err := WriteClusterRecord(ClusterRecord{
		UID: uid, Cluster: binding.Cluster, KubeContext: binding.KubeContext, Managed: binding.Managed,
		FirstSeenAt: time.Now().UTC(), DcctlVersion: opts.DcctlVersion,
	}); err != nil {
		fmt.Println(color.YellowString("warning: could not record what is known about cluster %s (%v).",
			binding.Describe(), err))
	}

	// The clients the first writes below go through come out of the refusals, so
	// those writes cannot be reached without them. See connectAndRefuse.
	dyn, typed, err := connectAndRefuse(ctx, st, settings, localClusterStateExists)
	if err != nil {
		return err
	}

	doing("settling the cluster's credentials")
	live, err := clusterArchivePath(ctx, dyn, infraNamespace, RdbClusterName)
	if err != nil {
		return fail("reading the relational store's archive state", err)
	}
	settleRdbArchivePath(st, live, time.Now().UTC())
	if st.Credentials, err = resolveCredentials(ctx, typed, st, liveArchiveState{Rdb: live}); err != nil {
		return fail("settling the cluster's credentials", err)
	}
	done()

	// 🔴 SAID BEFORE THE APPLY, not after. "It restored" and "it declined to restore"
	// are indistinguishable from the outside — the apply is green either way and no
	// data moves — so an operator mid-incident has to be told which one is about to
	// happen while they can still stop.
	printRelationalRestore(st.Restore, live, backupSnapshotClass(st))

	clusterVars, _, err := splitVars(infraVars(st))
	if err != nil {
		return err
	}
	if err := checkRelationalStoreOwner(ctx, st.KubeContext); err != nil {
		return err
	}
	// 🔴 BUILT BEFORE THE INSTALL RECORD IS OPENED, not inside it. On --build this
	// is a ko build measured in minutes, and the preflight treats `ko` as OPTIONAL
	// — so a developer without it fails here. Inside the applying→installed bracket
	// that failure would leave the cluster recorded `applying`, which every later
	// bootstrap refuses, over a step whose only cluster write is an idempotent
	// ConfigMap. It is a no-op on every published path.
	if err := buildOperatorImageForInstall(ctx, st); err != nil {
		return err
	}
	if err := ensureInfraNamespace(ctx, typed, infraNamespace); err != nil {
		return err
	}
	// Fenced here and before each irreversible write below: everything above this
	// point is a read or a refusal, and everything below it changes a cluster that
	// may no longer be ours. See stillHoldsTheCluster.
	if err := stillHoldsTheCluster(ctx, st, "writing this cluster's credentials"); err != nil {
		return err
	}
	if err := writeClusterSecrets(ctx, typed, st); err != nil {
		return err
	}
	if err := markInstallApplying(ctx, typed, st.ClusterUID, st.DcctlVersion, time.Now); err != nil {
		return err
	}
	// 🔴 AFTER markInstallApplying, BECAUSE THAT CALL IS THE LAST REFUSAL AND A
	// REFUSAL MUST COME BEFORE A WRITE THAT CANNOT BE TAKEN BACK. It rejects a
	// cluster whose install record carries a newer SCHEMA than this build writes,
	// and the overlay carries CRDs: applying first would let such a binary prune
	// fields out of a structural schema on its way to being told it may not touch
	// this cluster at all.
	//
	// 🔑 THAT IS A NARROWER GUARD THAN IT LOOKS, AND THE GAP IS THE WHOLE REASON
	// SLICE A EXISTS. The check is `old.Schema > installRecordSchema`, so a NEWER
	// dcctl at the SAME record schema passes it — and an older install then
	// server-side-applies its older CRDs with the same field manager and Force,
	// removing whatever the newer release added. Ordering cannot fix that; only
	// comparing what is on the cluster against what is about to be applied can,
	// which is what the operator identity is for.
	//
	// 🔴 THOSE GUARDS EXIST NOW — in `dcctl bootstrap` and `dcctl upgrade`, which
	// refuse a cluster whose operator is not the one they need. INSTALL ITSELF STILL
	// HAS NONE, and that is deliberate rather than overlooked: install is the verb
	// whose job is to MOVE the operator, so a guard refusing a difference would
	// refuse the only command that can resolve one. The consequence is that an older
	// dcctl's `install` still downgrades a newer cluster's CRDs silently, and closing
	// that needs a comparison that can tell "older" from "different" — which the
	// identity, being equality-only, cannot. Written down rather than left implied.
	if err := installOperator(ctx, st); err != nil {
		return err
	}
	if err := stillHoldsTheCluster(ctx, st, "applying the cluster prerequisites"); err != nil {
		return err
	}
	var outputs InstallOutputs
	if err := runStreamed("applying cluster prerequisites (OpenTofu)", "cluster prerequisites", func() error {
		outputs, err = applyClusterPrereqs(ctx, st, st.ClusterUID, clusterVars, infraNamespace)
		return err
	}); err != nil {
		return err
	}

	// 🔴 THE BASE IDENTITY BEFORE THE RECORD. Every instance's login is created as it, so
	// a cluster recorded as installed without it would refuse the first bootstrap.
	//
	// 🔴 AND THE BUDGET THE STORE RUNS WITH, NOT THE ONE IT WAS ASKED FOR. A changed
	// max_connections is applied by restarting the store's instances, after the apply has
	// returned; recording the new budget before then would admit instances against
	// connections the store does not have yet. Measured: the apply finished with the
	// store still on the old value.
	doing("creating the base database identity")
	if err := withProvisionerSession(ctx, st.KubeContext, outputs.Rdb, func(q instanceDBQuerier) error {
		return storeRunsWithBudget(ctx, q, outputs.Rdb.MaxConnections)
	}); err != nil {
		return fail("creating the base database identity", err)
	}
	done()

	// 🔴 THE LAST ONE MATTERS MOST. This record is what every later bootstrap reads
	// to decide the shape of the instances it builds; writing it over a reclaimer's
	// would hand them a cluster described by a run they stopped.
	if err := stillHoldsTheCluster(ctx, st, "recording the install"); err != nil {
		return err
	}
	if err := writeInstalled(ctx, typed, InstallRecord{
		ClusterUID:   st.ClusterUID,
		DcctlVersion: st.DcctlVersion,
		Settings:     settings,
		Outputs:      outputs,
	}, time.Now); err != nil {
		return err
	}
	reportInstall(st, provider.Name())
	return nil
}

// installState is the State an install runs from: the options, turned into the shape
// every step downstream reads.
//
// 🔴 SEPARATED FROM Install SO IT CAN BE EXERCISED AT ALL. Install needs a provider
// and a live cluster, so no test in this package reaches the struct literal below —
// and a literal is exactly where a settled option is dropped. The restore is the one
// that costs most: a dropped Restore leaves infraVars emitting no restore variables,
// which is an ordinary install of an EMPTY relational store, reported green, during
// the recovery it was run for. This was measured, not assumed — deleting the line was
// invisible to the whole suite before this function existed.
func installState(binding ClusterBinding, provider string, opts InstallOptions) *State {
	return &State{
		KubeContext:          binding.KubeContext,
		Binding:              binding,
		Provider:             provider,
		DcctlVersion:         opts.DcctlVersion,
		DryRun:               opts.DryRun,
		AssumeYes:            opts.AssumeYes,
		NoTLS:                opts.NoTLS,
		NoMonitoring:         opts.NoMonitoring,
		NoCNPG:               opts.NoCNPG,
		AllowLegacyDbRemoval: opts.AllowLegacyDbRemoval,
		// The image source the operator is installed at. Settled in the command
		// layer by ResolveImageSource before this cluster exists, for the same reason
		// the restore plan is — a dcctl build with no pinned image version is
		// knowable from argv, and finding out after EnsureCluster has spun up a kind
		// cluster is the expensive time to find out.
		ImageRegistry:     opts.ImageRegistry,
		ImageVersion:      opts.ImageVersion,
		BuildImages:       opts.BuildImages,
		Compact:           opts.Compact,
		HA:                opts.HA,
		BackupDestination: opts.BackupDestination,
		// Read through backupSnapshotClass, which drops it when backups are off.
		BackupSnapshotClass: opts.BackupSnapshotClass,
		DatabasePlacement:   opts.DatabasePlacement,
		MaxConnections:      opts.MaxConnections,
		Restore:             opts.Restore,
		Values:              map[string]string{},
	}
}

// settleRdbArchivePath records the serverName the CLUSTER's relational store owns for
// the rest of its life, which infraVars emits as backup_server_name_rdb.
//
// The path is permanent once the store is archiving, so a live store's own answer wins
// — see resolveArchivePaths for what deriving it from a one-shot flag costs. A restore
// into a store that is NOT there mints a fresh stamped path instead.
//
// 🔴 THAT SECOND BRANCH IS WHAT MAKES terraform_data.restore_guard UNREACHABLE FROM
// dcctl rather than merely survivable. The guard refuses backup_server_name_rdb equal
// to — or unset alongside — restore_rdb_from, because a recovered store pointed back
// at the archive it read comes up and then hangs in `Setting up primary` on `Expected
// empty archive`. Without the minting here, every dcctl recovery would hit that
// precondition: loudly, but as a refusal from a layer the operator is not driving,
// naming a variable dcctl does not expose, mid-incident.
//
// Empty is the OpenTofu default — the Cluster's own name — and is what every ordinary
// install leaves, so the var is omitted rather than passed empty.
//
// Split out from Install so it can be exercised: the branch that is wrong is the one
// that never runs on a developer's cluster, because a developer's cluster already has
// a relational store.
func settleRdbArchivePath(st *State, live clusterArchiveState, now time.Time) {
	if path, _ := resolveRdbArchivePath(live, st.Restore, now); path != "" {
		st.Values["backupServerNameRdb"] = path
	}
}

// printRelationalRestore tells the operator what this run will do to the relational
// store. One printer for the rehearsal and the real run, so the two cannot describe the
// same plan in different words — which is the whole claim a rehearsal makes.
func printRelationalRestore(plan RestorePlan, live clusterArchiveState, snapshotClass string) {
	lines := describeRelationalRestore(plan, live, time.Now().UTC())
	if len(lines) > 0 && snapshotClass != "" {
		lines = append(lines, snapshotRestoreNote("relational store"))
	}
	for _, line := range lines {
		fmt.Println(color.YellowString("  %s", line))
	}
}

// previousInstall reads whatever install record the cluster already has, parsed but not
// validated: a re-install is asking what was there, including a half-finished one.
func previousInstall(ctx context.Context, typed kubernetes.Interface) (*InstallRecord, error) {
	cm, err := getInstallRecordMap(ctx, typed)
	if err != nil || cm == nil {
		return nil, err
	}
	var rec InstallRecord
	if json.Unmarshal([]byte(cm.Data[installRecordKey]), &rec) != nil {
		// A record that does not parse describes nothing; markInstallApplying replaces it.
		return nil, nil
	}
	return &rec, nil
}

// installClients is the seam Install reaches the cluster through, so the refusals it
// runs before the first write can be exercised, not only the checks they call.
var installClients = func(kubeContext string) (dynamic.Interface, kubernetes.Interface, error) {
	dyn, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return nil, nil, err
	}
	return dyn, typed, nil
}

// identifyCluster is the seam Install and its rehearsal read the cluster's identity
// through. One variable for both, so the dry run identifies the cluster exactly as the
// run does; indirected so a test can reach the refusals behind it without a cluster.
var identifyCluster = IdentifyCluster

// rehearseInstallRefusals makes, for a dry run, the re-install refusals Install makes
// on what the cluster already holds: the same identity read and the same
// connectAndRefuse, so a rehearsal fails with the real run's own message. Those are
// an install from a machine that holds no state for the cluster, changed settings
// under running instances, and an unusable VolumeSnapshotClass.
//
// NOT rehearsed, and still refused by the real run: the relational-store owner check
// (checkRelationalStoreOwner) and the refusal of a record written by a newer dcctl
// (markInstallApplying).
//
// Failing to REACH the cluster is softened, as every dry-run read is: a dry run is
// often aimed at a cluster that does not exist yet. It is SAID, and the caller keeps
// the "NOT rehearsed" plan lines. What the cluster ANSWERS is fatal, as on the real
// run: an API status error (Forbidden, Unauthorized...), a cancelled context (Ctrl+C), and
// every refusal connectAndRefuse makes.
//
// 🔴 A FUNCTION OF ITS OWN, NOT A SECOND connectAndRefuse CALL INSIDE Install.
// TestInstallAppliesThePrerequisitesInOrder pins Install's call order by the FIRST
// occurrence of each name; a dry-run call above the real one would become that first
// occurrence and blind the guard to the real call moving.
//
// Nothing is written: no ClusterRecord, no claim. The refusal is returned unwrapped,
// byte-identical to the real run's.
func rehearseInstallRefusals(ctx context.Context, st *State, settings InstallSettings,
	stateExists func(uid string) (bool, error)) (rehearsed bool, err error) {
	uid, err := identifyCluster(ctx, st.KubeContext)
	if err != nil {
		var status apierrors.APIStatus
		if errors.Is(ctx.Err(), context.Canceled) || errors.As(err, &status) {
			return false, fmt.Errorf("reading the identity of cluster %s: %w", st.Binding.Describe(), err)
		}
		fmt.Println(color.YellowString(
			"  could not identify cluster %s (%v); the refusals a re-install makes — an install from "+
				"another machine, changed settings under running instances, an unusable "+
				"VolumeSnapshotClass — were NOT rehearsed; the real run identifies the cluster "+
				"first and stops if it cannot",
			st.Binding.Describe(), err))
		return false, nil
	}
	st.ClusterUID, st.Binding.ClusterUID = uid, uid
	if _, _, err := connectAndRefuse(ctx, st, settings, stateExists); err != nil {
		return true, err
	}
	return true, nil
}

// connectAndRefuse connects to the cluster and runs every refusal Install makes on
// what the cluster already holds, handing back the clients only when none refuses.
//
// 🔑 THE CLIENTS ARE THE RETURN VALUE ON PURPOSE. Install's first writes -- the
// infrastructure namespace, the cluster's credentials, the record marked applying --
// go through them, so this call cannot be dropped from Install without those losing
// the clients they need: it would not compile. A refusal called beside the writes
// instead would be one line nothing notices missing. Everything in here reads;
// nothing writes.
func connectAndRefuse(ctx context.Context, st *State, settings InstallSettings,
	stateExists func(uid string) (bool, error)) (dynamic.Interface, kubernetes.Interface, error) {
	dyn, typed, err := installClients(st.KubeContext)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to cluster %s: %w", st.Binding.Describe(), err)
	}
	prev, err := previousInstall(ctx, typed)
	if err != nil {
		return nil, nil, err
	}
	if st.MaxConnections == 0 {
		st.MaxConnections = defaultMaxConnections
		if last := prev.lastCompleted(); last != nil && last.Outputs.Rdb.MaxConnections > 0 {
			st.MaxConnections = last.Outputs.Rdb.MaxConnections
		}
	}
	if err := refuseAReinstallThatWouldHurt(ctx, st, prev, settings, stateExists); err != nil {
		return nil, nil, err
	}
	// 🔴 BEFORE THE FIRST WRITE. A class that is missing, keeps the provider's
	// snapshot when its VolumeSnapshot is deleted, or belongs to another driver is
	// accepted by every API it passes through, and then fails every snapshot or keeps
	// every one for ever. Found here, it costs a re-run; found after the apply, it
	// costs a cluster recorded as installed with backups that do not work.
	if class := backupSnapshotClass(st); class != "" {
		doing("checking VolumeSnapshotClass " + class)
		if err := checkVolumeSnapshotClass(ctx, dyn, typed, class, infraNamespace, RdbClusterName); err != nil {
			return nil, nil, fail("checking VolumeSnapshotClass "+class, err)
		}
		done()
	}
	return dyn, typed, nil
}

// localClusterStateExists reports whether this machine holds the cluster root's state
// for the cluster.
func localClusterStateExists(uid string) (bool, error) {
	dir, err := dcdir.Cluster(uid)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(filepath.Join(dir, prereqStateSubdir, assets.ClusterRootDir, "terraform.tfstate"))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// refuseAReinstallThatWouldHurt stops a re-install that would damage what the cluster
// already runs.
//
// Two ways, and both are green runs without this:
//
//   - 🔴 AN INSTALL FROM A MACHINE THAT DID NOT DO IT. The cluster root's state lives
//     on the machine that applied it. Another machine starts from empty state, plans
//     every prerequisite as new, and dies on "cannot re-use a name that is still in
//     use" part-way through — after marking the record as applying, which is what
//     every bootstrap then refuses.
//   - 🔴 NEW SETTINGS UNDER RUNNING INSTANCES. Every instance was built to the settings
//     it found — its HA, its sizing, whether it archives — and none of them is
//     re-applied when the cluster's change. Turning backups off or HA down underneath
//     them leaves instances that believe in a cluster that no longer exists. Raising
//     the connection budget is the one change that cannot hurt them, so it is allowed.
func refuseAReinstallThatWouldHurt(ctx context.Context, st *State, prev *InstallRecord, settings InstallSettings,
	stateExists func(uid string) (bool, error)) error {
	last := prev.lastCompleted()
	if last == nil {
		return nil
	}
	exists, err := stateExists(st.ClusterUID)
	if err != nil {
		return fmt.Errorf("checking this machine for the cluster's prerequisite state: %w", err)
	}
	if !exists {
		return fmt.Errorf("cluster %s is already installed (by dcctl %s, %s), but this machine holds no "+
			"state for it under ~/.devicechain/clusters/%s. It was installed from another machine, and "+
			"re-applying from empty state would try to create every prerequisite again. Run "+
			"`dcctl install` from the machine that installed it",
			st.Binding.Describe(), last.DcctlVersion, last.UpdatedAt.Format(time.RFC3339), st.ClusterUID)
	}

	var changed []string
	if last.Settings != settings {
		changed = append(changed, fmt.Sprintf("settings %s → %s",
			describeInstallSettings(last.Settings), describeInstallSettings(settings)))
	}
	if st.MaxConnections < last.Outputs.Rdb.MaxConnections {
		changed = append(changed, fmt.Sprintf("connection budget %d → %d",
			last.Outputs.Rdb.MaxConnections, st.MaxConnections))
	}
	// An off-site destination is more than a boolean: every instance's event store
	// archives to the endpoint and bucket it was built with, and none is re-pointed.
	if d, a := st.BackupDestination, last.Outputs.Archive; last.Settings.BackupsExternal && d.Configured() &&
		(d.EndpointURL != a.EndpointURL || d.BucketTsdb != a.BucketTsdb) {
		changed = append(changed, fmt.Sprintf("off-site archive %s/%s → %s/%s",
			a.EndpointURL, a.BucketTsdb, d.EndpointURL, d.BucketTsdb))
	}
	if len(changed) == 0 {
		return nil
	}
	held, err := readClusterInstances(ctx, st.KubeContext)
	if err != nil {
		return fmt.Errorf("this install changes the cluster (%s), and dcctl cannot tell whether any "+
			"instance runs on it to be hurt by that: %w", strings.Join(changed, "; "), err)
	}
	if len(held.IDs) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to change cluster %s (%s) while instance(s) %s run on it: each was "+
		"built to the settings it found, and none is rebuilt when they change. Re-run `dcctl install` "+
		"with the settings it was installed with, or destroy those instances first",
		st.Binding.Describe(), strings.Join(changed, "; "), strings.Join(held.IDs, ", "))
}

// describeInstallSettings renders settings as the flags that produce them.
func describeInstallSettings(s InstallSettings) string {
	parts := []string{}
	if s.HA {
		parts = append(parts, "ha")
	}
	if s.Compact {
		parts = append(parts, "compact")
	}
	for _, f := range []struct {
		on   bool
		name string
	}{
		{s.Monitoring, "monitoring"},
		{s.CNPG, "cloudnative-pg"},
		{s.CertManager, "cert-manager"},
		{s.DatabaseBackups, "backups"},
	} {
		if f.on {
			parts = append(parts, f.name)
		} else {
			parts = append(parts, "no "+f.name)
		}
	}
	if s.BackupsExternal {
		parts = append(parts, "off-site archive")
	}
	if s.BackupSnapshotClass != "" {
		parts = append(parts, fmt.Sprintf("volume-snapshot base backups (%s)", s.BackupSnapshotClass))
	}
	if !s.DatabasePlacement.IsZero() {
		parts = append(parts, "databases on "+s.DatabasePlacement.describe())
	}
	return strings.Join(parts, ", ")
}

// FollowInstall shapes a bootstrap from the cluster's install record: the half of an
// instance the cluster decides, and what the cluster apply built.
//
// 🔴 EVERY FIELD IS OVERWRITTEN, NONE MERGED. A bootstrap has no flags for these any
// more, so anything already in them is a zero value, and a zero value here is a
// decision — "no HA", "no monitoring" — about a cluster that may have both.
func FollowInstall(st *State, rec *InstallRecord) {
	st.Install = rec
	st.HA = rec.Settings.HA
	st.Compact = rec.Settings.Compact
	st.NoMonitoring = !rec.Settings.Monitoring
	st.NoCNPG = !rec.Settings.CNPG
	st.Values[databaseNamespaceKey] = rec.Outputs.Rdb.Namespace
	st.Values[cnpgNamespaceKey] = rec.Outputs.CNPGNamespace
	st.Values["grafanaService"] = rec.Outputs.GrafanaService
	st.Values["grafanaNamespace"] = rec.Outputs.GrafanaNamespace
	st.Values[databaseBackupOffsiteKey] = fmt.Sprint(rec.Outputs.BackupSurvivesClusterLoss)
}

// ReadInstall identifies the cluster a context points at and reads the install record a
// bootstrap follows, refusing — with the command that fixes it — a cluster that has not
// been installed.
//
// An empty uid with an error means the cluster could not be identified; a uid with an
// error means it was, and its install record is unusable.
func ReadInstall(ctx context.Context, kubeContext, installCommand string) (uid string, rec *InstallRecord, err error) {
	_, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return "", nil, fmt.Errorf("building a client to identify the cluster: %w", err)
	}
	if uid, err = ClusterUID(ctx, typed); err != nil {
		return "", nil, err
	}
	if rec, err = readInstallRecord(ctx, typed, uid); err != nil {
		return uid, nil, refuseUninstalled(err, installCommand)
	}
	return uid, rec, nil
}

// refuseUninstalled turns an unusable install record into the refusal an operator can
// act on.
func refuseUninstalled(err error, installCommand string) error {
	if errors.Is(err, ErrNotInstalled) {
		return fmt.Errorf("this cluster is not ready for instances — %w.\n"+
			"  Prepare it once with:\n\n    %s\n\n"+
			"  then build any number of instances on it with `dcctl bootstrap`", err, installCommand)
	}
	return err
}

// InstallCommand is the `dcctl install` invocation that prepares the cluster a command
// was aimed at, for refusals to print.
func InstallCommand(provider string, b ClusterBinding) string {
	return "dcctl install " + provider + clusterTargetFlag(b)
}

// reportInstall prints what the install left in place.
func reportInstall(st *State, provider string) {
	fmt.Println(color.HiGreenString("\nDeviceChain prerequisites installed"))
	fmt.Printf("  %s %s\n", color.WhiteString("Cluster:"), color.GreenString(st.Binding.Describe()))
	fmt.Printf("  %s %s\n", color.WhiteString("Kube context:"), color.GreenString(st.KubeContext))
	fmt.Printf("  %s %s\n", color.WhiteString("Connection budget:"),
		color.GreenString("%d (each instance reserves its own share when it is bootstrapped)", st.MaxConnections))
	printBackups("instances on this cluster", databaseBackupsEnabled(st), backupsAreExternal(st))
	if class := backupSnapshotClass(st); class != "" {
		fmt.Printf("  %s %s\n", color.WhiteString("Base backups:"), color.GreenString(
			"daily volume snapshots (VolumeSnapshotClass %s), pruned by the operator to each store's window; "+
				"a weekly one in the backup store, which is what a restore reads", class))
	}
	if p := databasePlacement(st); !p.IsZero() {
		fmt.Printf("  %s %s\n", color.WhiteString("Databases on:"), color.GreenString(
			"%s (the relational store, and every instance's event store)", p.describe()))
	}
	if p := st.Values["backupServerNameRdb"]; p != "" && databaseBackupsEnabled(st) {
		fmt.Printf("  %s %s\n", color.WhiteString("Relational archive:"), color.GreenString(p))
	}
	// 🔴 INTENT, AND LABELLED AS INTENT. The apply returning says the recovery
	// bootstrap was rendered, not that a single row came back — nothing in an apply
	// can say that. An operator who reads this as a verdict stops checking, which is
	// how a restore that recovered nothing gets believed.
	if st.Restore.RestoresRelationalStore() {
		fmt.Printf("  %s %s\n", color.WhiteString("Relational recovery:"),
			color.GreenString("requested from %q — check `kubectl -n %s get clusters.postgresql.cnpg.io %s` "+
				"reads `Cluster in healthy state` before believing it", st.Restore.RdbFrom,
				infraNamespace, RdbClusterName))
	}
	fmt.Println(color.HiGreenString("\nNext: build an instance on it:\n\n    dcctl bootstrap %s <instance>%s\n",
		provider, clusterTargetFlag(st.Binding)))
}

// clusterTargetFlag is what a command needs to be told to land on this cluster: a
// context named by hand, or a kind cluster dcctl names that is not the default.
func clusterTargetFlag(b ClusterBinding) string {
	switch {
	case !b.Managed:
		return " --kube-context " + b.KubeContext
	case b.Cluster != "" && b.Cluster != DefaultClusterName:
		return " --cluster " + b.Cluster
	}
	return ""
}

// storeRunsWithBudget answers not-ready until the store is running with the budget the
// cluster root asked for, so the session that asks is retried through the restart.
func storeRunsWithBudget(ctx context.Context, q instanceDBQuerier, want int) error {
	var running int
	var pending bool
	if err := q.QueryRow(ctx, `select setting::int, pending_restart from pg_settings
		where name = 'max_connections'`).Scan(&running, &pending); err != nil {
		return notReady("reading the relational store's max_connections: %v", err)
	}
	if running != want || pending {
		return notReady("the relational store runs with max_connections %d (restart pending: %t), "+
			"and is being restarted onto %d", running, pending, want)
	}
	return nil
}
