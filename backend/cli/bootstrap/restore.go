// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/fatih/color"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// The two CloudNativePG Clusters the OpenTofu root stands up, and the namespace
// they live in. Named here rather than derived because the restore path has to
// ask the cluster what it is archiving BEFORE OpenTofu runs, and the root's
// module blocks are not readable from Go.
const (
	RdbClusterName  = "dc-rdb"
	TsdbClusterName = "dc-tsdb"
)

// barmanPluginName is the CNPG-I plugin whose parameters carry the archive path.
const barmanPluginName = "barman-cloud.cloudnative-pg.io"

// RestoreFlags is the raw --restore-* input, before validation.
//
// 🔴 THE TWO STORES ARE REACHED BY DIFFERENT COMMANDS, and one struct carries both
// on purpose. The relational store is the CLUSTER's — `dcctl install` builds it and
// every instance shares it — while the event store is one INSTANCE's, built by
// `dcctl bootstrap`. So `install` fills the Rdb half and `bootstrap` fills the Tsdb
// half, and neither ever fills the other's.
//
// They share a struct because every rule that makes a restore wrong is the same rule
// for both — a recovery target with nothing to recover, a timestamp with no offset, a
// cluster with no backup plugin to read the archive — and the one that was written
// twice is the one that gets fixed once.
type RestoreFlags struct {
	RdbFrom        string
	RdbTargetTime  string
	TsdbFrom       string
	TsdbTargetTime string
	// BackupsEnabled is whether the cluster archives at all — the install's answer.
	// Passed in rather than recomputed so there is one derivation of it in the CLI.
	BackupsEnabled bool
}

// RestorePlan is the settled database-restore intent for one run: which archive each
// store recovers FROM, and how far it replays.
//
// 🔴 REBUILD-TIME ONLY. `spec.bootstrap` is read when CloudNativePG CREATES a
// Cluster, so a plan aimed at a store that already exists does nothing at all — no
// error, no restore, a green apply. stepRenderConfig and Install both say so out loud
// when they find the Cluster already there, because "it ran and restored nothing" and
// "it declined to run" are indistinguishable from the outside.
type RestorePlan struct {
	RdbFrom        string
	RdbTargetTime  string
	TsdbFrom       string
	TsdbTargetTime string
}

// Active reports whether this run restores anything.
func (p RestorePlan) Active() bool { return p.RdbFrom != "" || p.TsdbFrom != "" }

// RestoresEventStore reports whether this run recovers the INSTANCE's event store.
//
// Separate from Active because the two are asked by different code for different
// reasons, and one plan can now carry a restore that is not this question's: an
// instance's declaration records whether that INSTANCE was restored, and a cluster's
// relational recovery is not an answer to it.
func (p RestorePlan) RestoresEventStore() bool { return p.TsdbFrom != "" }

// RestoresRelationalStore reports whether this run recovers the CLUSTER's shared
// relational store.
func (p RestorePlan) RestoresRelationalStore() bool { return p.RdbFrom != "" }

// DatabaseBackupsEnabled reports whether this combination of install flags leaves the
// cluster with a backup destination — i.e. whether OpenTofu will be told
// enable_database_backups=true.
//
// It is the flag half of databaseBackupsEnabled, which infraVars emits from, exported
// because `dcctl install` needs the answer from argv alone, before any State exists.
// A bootstrap does not ask it: it follows the install record, through
// BackupsEnabledFor.
//
// Both halves are consequences, not choices: --no-cnpg skips the operator the
// plugin extends, and --compact --no-tls drops cert-manager, which the plugin needs
// for its own Issuer and Certificates. See tofu.go for the full reasoning.
func DatabaseBackupsEnabled(noCNPG, compact, noTLS bool) bool {
	return !noCNPG && !(compact && noTLS)
}

// BackupsEnabledFor is DatabaseBackupsEnabled for a run whose shape is already settled,
// including one following an install record.
func BackupsEnabledFor(st *State) bool { return databaseBackupsEnabled(st) }

// ResolveRestorePlan validates the --restore-* flags and settles them into a plan.
//
// It runs in the command layer, BEFORE any cluster exists, for the same reason the
// escrow plan does: every way this can be wrong is knowable from argv alone, and
// finding out ten minutes into a rebuild — during an incident — is the expensive
// time to find out.
func ResolveRestorePlan(f RestoreFlags) (RestorePlan, error) {
	plan := RestorePlan{
		RdbFrom:        f.RdbFrom,
		RdbTargetTime:  f.RdbTargetTime,
		TsdbFrom:       f.TsdbFrom,
		TsdbTargetTime: f.TsdbTargetTime,
	}

	// 🔴 ONE SET OF RULES, WALKED ONCE PER STORE. The alternative — the same three
	// checks written out again with `rdb` swapped for `tsdb` — is the shape that lets
	// one copy stop covering what the other does, and the copy that stops covering is
	// always the newer one, whose failures nobody has seen yet.
	for _, s := range []struct {
		fromFlag, atFlag string
		from, at         string
	}{
		{"--restore-rdb-from", "--restore-rdb-at", f.RdbFrom, f.RdbTargetTime},
		{"--restore-tsdb-from", "--restore-tsdb-at", f.TsdbFrom, f.TsdbTargetTime},
	} {
		// A target time with nothing to restore is silently ignored by the root, which
		// is the wrong outcome for a flag whose whole purpose is to stop replay before
		// a known-bad moment: the operator would get a full-archive restore and be told
		// nothing. (The root refuses this too — this refusal is here so it lands before
		// the rebuild rather than at plan time.)
		if s.at != "" && s.from == "" {
			return RestorePlan{}, fmt.Errorf(
				"%s is set but %s is empty, so nothing is being restored and the recovery "+
					"target would be silently ignored", s.atFlag, s.fromFlag)
		}

		// A recovery target is a moment, and a moment with no timezone is a different
		// moment on a different server.
		//
		// 🔴 THIS IS THE ONE THAT SUCCEEDS AND IS WRONG. PostgreSQL accepts
		// "2026-07-27 13:59:00" and interprets it in the RECOVERING server's TimeZone,
		// so a target meant as UTC silently becomes a target hours away — and recovery
		// stops there, reports success, and hands back a database rewound to the wrong
		// instant. Everything else on this path fails loudly; this one does not, and
		// the whole reason an operator reaches for a target is that they know exactly
		// which moment they need to stop before.
		//
		// RFC3339 is narrower than the OpenTofu variable accepts, deliberately: the var
		// is a general lever for someone driving OpenTofu directly, and this is the
		// operator surface, where an unambiguous instant is worth more than a permissive
		// grammar.
		if s.at != "" {
			if _, err := time.Parse(time.RFC3339, s.at); err != nil {
				return RestorePlan{}, fmt.Errorf(
					"%s %q is not an RFC3339 timestamp. It needs an explicit offset — "+
						"2026-07-27T13:59:00Z, or 2026-07-27T09:59:00-04:00. Without one PostgreSQL "+
						"reads it in the recovering server's own timezone, and recovery stops at a "+
						"different moment than you named and reports success",
					s.atFlag, s.at)
			}
		}

		// Restoring needs the Barman Cloud plugin, and the flags that switch the backup
		// destination off switch the RESTORE path off with it — the same plugin reads
		// the archive that writes it. Without this the run proceeds, OpenTofu refuses at
		// plan time, and the operator is told to set a variable dcctl does not expose.
		//
		// The message names the flag the operator actually typed. A refusal that named
		// the OTHER store's flag would read as a bug in dcctl rather than as an answer.
		if s.from != "" && !f.BackupsEnabled {
			return RestorePlan{}, fmt.Errorf(
				"%s needs the database backup plugin, and this cluster was "+
					"installed without it: `dcctl install --no-cnpg` skips the CloudNativePG operator "+
					"the plugin extends, and `dcctl install --compact --no-tls` drops cert-manager, which "+
					"the plugin needs for its own Issuer and Certificates. Restore onto a cluster "+
					"installed with neither (--compact --no-tls=false keeps backups)", s.fromFlag)
		}
	}

	return plan, nil
}

// RestoredArchivePath is the archive path a cluster recovered from source should
// OWN — i.e. what it archives into afterwards.
//
// 🔴 It must differ from the source AND from every earlier restore of that same
// source. CloudNativePG refuses to archive into a non-empty archive, and the
// refusal is not a clean error: the cluster stays in `Setting up primary`
// indefinitely, logging `Expected empty archive`. A plain "<source>-restored"
// would collide on the SECOND restore from the same archive — which is precisely
// the case where an operator is retrying a recovery that did not take — so the
// stamp is not decoration.
//
// Stability across re-runs is NOT provided here. It comes from
// resolveArchivePaths, which prefers what the live Cluster is already archiving to
// over anything derived; this function is only ever called when there is nothing
// live to read.
func RestoredArchivePath(source string, now time.Time) string {
	return fmt.Sprintf("%s-restored-%s", source, now.UTC().Format("20060102T150405Z"))
}

// clusterArchiveState is what one Cluster is currently doing: whether it is there
// at all, and the serverName it archives under. An ordinary install archives under
// its own name and renders no explicit parameter, so Exists with an empty Path is
// both possible and common.
type clusterArchiveState struct {
	Exists bool
	Path   string
}

// liveArchiveState is what resolveCredentials is told about the database Clusters a
// run's credentials were built into. Each half is filled by the run that owns that
// store: the install reads the relational store, a bootstrap the event store.
type liveArchiveState struct {
	Rdb  clusterArchiveState
	Tsdb clusterArchiveState
}

// readLiveArchiveState is the seam stepRenderConfig uses to ask what this instance's
// event store is ALREADY archiving under. Indirected for the same reason as
// lookupDeployedInstance: the branch behind it decides between "keep the path this
// instance owns" and "retarget a live cluster's WAL archive", and that decision has
// to be testable without standing up CloudNativePG.
var readLiveArchiveState = func(ctx context.Context, kubeContext, instance string) (clusterArchiveState, error) {
	restCfg, err := RestConfig(kubeContext)
	if err != nil {
		return clusterArchiveState{}, fmt.Errorf("building kube config to read the database archive state: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return clusterArchiveState{}, err
	}
	return readArchiveState(ctx, dyn, instance)
}

// readArchiveState is the half of readLiveArchiveState that has no cluster in it:
// which Cluster, in which namespace, is this instance's event store.
//
// 🔴 SPLIT OUT SO THAT WIRING IS TESTABLE AT ALL. readLiveArchiveState is the seam
// every stepRenderConfig test stubs, so the lookup below is the one part of the
// restore path that no test in this package could otherwise reach — and reading the
// wrong Cluster is invisible in exactly the way that costs the most: the instance is
// handed another store's archive path, and an ordinary re-run retargets its archiver
// at a prefix holding no base backup of it.
func readArchiveState(ctx context.Context, dyn dynamic.Interface, instance string) (clusterArchiveState, error) {
	// The event store is the instance's, in the instance's namespace.
	return clusterArchivePath(ctx, dyn, InstanceNamespace(instance), TsdbClusterName)
}

// clusterArchivePath reads one Cluster's archiver serverName.
//
// A missing Cluster and a missing CNPG CRD both mean "not there" — a fresh cluster
// and a --no-cnpg cluster are both simply not archiving yet. Every other error
// FAILS, because "we could not tell" must not be read as "there is nothing there":
// see resolveArchivePaths for what acting on a wrong answer costs.
func clusterArchivePath(ctx context.Context, dyn dynamic.Interface, namespace, name string) (clusterArchiveState, error) {
	cl, err := dyn.Resource(clusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return clusterArchiveState{}, nil
		}
		return clusterArchiveState{}, fmt.Errorf("reading Cluster %s/%s: %w. Refusing to "+
			"continue: if the cluster IS there, guessing that it archives nowhere would "+
			"retarget its WAL archive at a path with no base backup in it", namespace, name, err)
	}
	out := clusterArchiveState{Exists: true}

	// 🔴 MALFORMED IS NOT ABSENT — the rule for every read below, without
	// exception, and it is the whole reason this function has error returns at all.
	//
	// An unreadable archiver spec reports Exists=true with an empty Path, and empty
	// does not mean "unknown" here: it means "archiving under its own name", the
	// default every ordinary install has. So the next re-run emits no serverName
	// and retargets this cluster's LIVE WAL archiver at a prefix holding no base
	// backup. That is the same direction the Get above refuses to guess in.
	//
	// The rule was first applied to two of the five reads in this function and not
	// to the other three, which is worse than not having applied it: the comments
	// said the spec was checked while `name`, `isWALArchiver` and the entry type
	// were still being read with `_, _` and skipped on error — landing on the same
	// destructive default by a route the guards did not cover. One helper now, so
	// there is one statement of the rule rather than five chances to forget it.
	unreadable := func(field string, err error) (clusterArchiveState, error) {
		return clusterArchiveState{}, fmt.Errorf("reading %s of Cluster %s/%s: %w. Refusing "+
			"to continue: an unreadable archiver spec is not an absent one, and reading it "+
			"as 'archiving under its own name' would retarget this cluster's WAL archive at "+
			"a path with no base backup in it", field, namespace, name, err)
	}

	// This read in particular used to fold its error in with the empty case —
	// `if err != nil || len(plugins) == 0` — which looks fail-closed and is not:
	// NestedSlice hands back a nil slice on error, so the error branch was
	// unreachable and both cases returned the same answer.
	plugins, _, err := unstructured.NestedSlice(cl.Object, "spec", "plugins")
	if err != nil {
		return unreadable("spec.plugins", err)
	}
	if len(plugins) == 0 {
		return out, nil
	}
	for i, p := range plugins {
		plug, ok := p.(map[string]any)
		if !ok {
			return unreadable(fmt.Sprintf("spec.plugins[%d]", i),
				fmt.Errorf("the entry is %T, not an object", p))
		}
		n, _, err := unstructured.NestedString(plug, "name")
		if err != nil {
			return unreadable(fmt.Sprintf("spec.plugins[%d].name", i), err)
		}
		if n != barmanPluginName {
			continue
		}
		// The ARCHIVER, specifically — the entry naming the path this cluster OWNS.
		//
		// This is DEFENSIVE, not a fix for what the chart renders today: a restored
		// Cluster's recovery source lives under spec.externalClusters[].plugin, a
		// different field this loop never reads, so spec.plugins currently holds
		// exactly one barman entry. CNPG permits several, only one of which may be
		// the WAL archiver, and the day a second lands here (a replica source, a
		// second destination) matching on the plugin NAME would return whichever
		// came first — handing back an archive this cluster does not own and
		// retargeting a live archiver at it. isWALArchiver is the field that
		// actually carries the distinction, so match on it rather than on position.
		isArchiver, _, err := unstructured.NestedBool(plug, "isWALArchiver")
		if err != nil {
			return unreadable(fmt.Sprintf("spec.plugins[%d].isWALArchiver", i), err)
		}
		if !isArchiver {
			continue
		}
		// The field that actually carries the answer.
		path, _, err := unstructured.NestedString(plug, "parameters", "serverName")
		if err != nil {
			return unreadable(fmt.Sprintf("spec.plugins[%d].parameters.serverName", i), err)
		}
		out.Path = path
		return out, nil
	}
	return out, nil
}

// freshTsdbArchivePath is where a NEW event store archives: under a path of its own
// instance, and of this generation of it.
//
// 🔴 BOTH HALVES ARE LOAD-BEARING. Every instance's event store archives into one
// shared bucket, and CloudNativePG refuses to start a new Cluster over a path that
// already holds an archive — it waits in "Setting up primary" on "Expected empty
// archive", with nothing failing. The instance id keeps two instances apart; the
// declaration's UID keeps a rebuilt instance off the archive its previous generation
// left behind, which outlives a destroy on purpose.
//
// A dry run has no declaration to read a UID from, and deploys nothing, so it shows
// the id half alone.
func freshTsdbArchivePath(instance, instanceUID string) string {
	p := TsdbClusterName + "-" + instance
	if len(instanceUID) >= 8 {
		p += "-" + instanceUID[:8]
	}
	return p
}

// archivePaths is what resolveArchivePaths settles: the serverName the event store
// should archive under for the rest of this instance's life.
type archivePaths struct {
	Tsdb string
	// AlreadyLive names the Clusters that already exist while a restore was
	// requested for them. Those restores will NOT happen: `spec.bootstrap` is read
	// at CREATE only.
	AlreadyLive []string
}

// resolveArchivePaths decides what the event store archives under, given what it is
// already archiving under and what this run is restoring.
//
// 🔴 THE POINT OF THIS FUNCTION IS THAT THE ANSWER MUST NOT MOVE. The archive path
// is permanent configuration; a restore is a one-shot flag. Deriving the path from
// the flag alone means an ordinary `dcctl bootstrap` re-run — weeks later, with no
// restore flag — reverts it, and the helm upgrade retargets a LIVE cluster's
// archiver at a different path. The instance then has no base backup at the path it
// is writing to until the next scheduled one, and nothing anywhere says so. That is
// the same shape as the credential rotation A8 closed, so it is closed the same way:
// the live value wins by construction, and the derived one is only ever reached
// when there is no live value to read.
//
// The one exception is a restore aimed at a store with no explicit path (an
// ordinary install, archiving under its own name). Keeping "" there would send the
// restored cluster back over the archive it just recovered from — the wedge — so a
// fresh path is derived instead. The relational store is the cluster's, and
// `dcctl install` keeps its path from the live store the same way.
//
// fresh is what an event store that neither exists nor is being restored archives
// under: freshTsdbArchivePath, because every instance's event store archives into the
// SAME bucket, so its own name would be every instance's path.
func resolveArchivePaths(live clusterArchiveState, plan RestorePlan, fresh string, now time.Time) archivePaths {
	var out archivePaths
	path, ineffective := resolveArchivePath(live, plan.TsdbFrom, fresh, now)
	out.Tsdb = path
	if ineffective {
		out.AlreadyLive = append(out.AlreadyLive, TsdbClusterName)
	}
	return out
}

// resolveRdbArchivePath is resolveArchivePaths for the CLUSTER's relational store —
// `dcctl install`'s half, settled the same way and for the same reason.
//
// 🔴 ITS FRESH PATH IS EMPTY, AND THAT IS NOT AN OVERSIGHT. Empty is the OpenTofu
// default, which means "archive under the Cluster's own name" — unambiguous here
// because a cluster has exactly ONE relational store, in one bucket of its own. The
// event store cannot do that: every instance's shares one bucket, so its own name
// would be every instance's path, which is why freshTsdbArchivePath exists and why
// nothing like it is needed here.
//
// ineffective reports that the relational store is already there, so the restore will
// NOT run — CloudNativePG reads `spec.bootstrap` only when it CREATES a Cluster.
func resolveRdbArchivePath(live clusterArchiveState, plan RestorePlan, now time.Time) (path string, ineffective bool) {
	return resolveArchivePath(live, plan.RdbFrom, "", now)
}

// resolveArchivePath is the rule above applied to ONE store: what it archives under,
// given what it is already archiving under and what this run recovers it from.
//
// Written once and called per store rather than once per store, because the branch
// that was wrong the first time — Path where it meant Exists — is exactly the kind
// that gets fixed in one copy.
func resolveArchivePath(live clusterArchiveState, restoreFrom, fresh string, now time.Time) (path string, ineffective bool) {
	switch {
	case live.Exists:
		// 🔴 A CLUSTER THAT EXISTS KEEPS ITS PATH, unconditionally — including the
		// empty one, which means "archiving under its own name". There is no case
		// in which moving it is right: a restore cannot run against an existing
		// Cluster at all (`spec.bootstrap` is CREATE-only), so the only thing a
		// new path could do here is retarget a LIVE archiver.
		//
		// This branch keyed on Path rather than Exists in its first version, and
		// the difference is the whole defect: a store archiving under its own name
		// renders no serverName, so Path is "" while the cluster is alive and
		// archiving. A restore naming that very archive — the obvious wrong guess,
		// and what a half-followed runbook produces — fell through to the derived
		// branch and emitted a fresh path for a running instance. The archive with
		// the base backup would stop receiving WAL, and the prefix now receiving
		// WAL would have no base backup until the next scheduled one: a window,
		// up to a day wide, in which the database is restorable to no point at
		// all. Green apply, no error, on the run that was trying to recover.
		return live.Path, restoreFrom != ""
	case restoreFrom != "":
		// The real restore: nothing is there, so this Cluster is about to be
		// CREATED and needs a path of its own to archive into.
		//
		// 🔴 THIS IS WHAT MAKES terraform_data.restore_guard UNREACHABLE FROM dcctl
		// rather than merely survivable. That precondition refuses a recovered store
		// pointed back at the archive it read; deriving a stamped path here means
		// dcctl never asks for that combination in the first place, so the guard is
		// a net under someone driving OpenTofu by hand, not a step in this path.
		return RestoredArchivePath(restoreFrom, now), false
	default:
		// A fresh ordinary install.
		return fresh, false
	}
}

// describeRelationalRestore is what `dcctl install` tells the operator about the
// relational restore it is about to perform — in one place, so the rehearsal and the
// real run cannot describe the same plan differently.
//
// 🔴 IT IS COMPUTED FROM THE PLAN AND THE LIVE READ, NEVER FROM AN APPLY'S OUTPUTS.
// A --dry-run reads back nothing from an apply it does not run, so a describer built
// on outputs prints the zero value and calls it the answer — which this project has
// already shipped once, as a dry run reporting "Backups: NONE" for a cluster that
// archives. Both inputs here are available before anything is applied, so the
// rehearsal's sentence is the real run's sentence.
//
// Empty when nothing is being restored: an ordinary install should say nothing about
// recovery at all.
func describeRelationalRestore(plan RestorePlan, live clusterArchiveState, now time.Time) []string {
	if !plan.RestoresRelationalStore() {
		return nil
	}
	path, ineffective := resolveRdbArchivePath(live, plan, now)
	if ineffective {
		// 🔴 SAID OUT LOUD, because from the outside "it restored" and "it declined to
		// restore" look identical: the apply is green either way and no data moves.
		return []string{fmt.Sprintf(
			"Cluster %s/%s already exists, so its restore from %q will NOT run: CloudNativePG "+
				"reads spec.bootstrap only when it CREATES a cluster. A relational restore only "+
				"takes effect on a cluster whose relational store is not there — which is the "+
				"disaster it is for.", infraNamespace, RdbClusterName, plan.RdbFrom)}
	}
	line := fmt.Sprintf("relational store recovering from archive %q, and will archive under %q",
		plan.RdbFrom, path)
	if plan.RdbTargetTime != "" {
		line += fmt.Sprintf(", replaying no further than %s", plan.RdbTargetTime)
	}
	// 🔴 THE HALF THIS COMMAND CANNOT ENFORCE, SAID WHERE THE OPERATOR IS.
	//
	// Before the install/bootstrap split, dcctl REFUSED a relational restore that did
	// not also carry --restore-root-key: the recovered rows hold secrets sealed by an
	// instance's root key, and a key that existed only in the destroyed cluster's etcd
	// cannot be minted back. That refusal cannot live here any more — an install has no
	// instance and no escrow artifact to check against, and `dcctl bootstrap` runs
	// later, possibly much later, possibly from another machine.
	//
	// What is left is that this is the last moment anyone is thinking about the
	// recovery, so it is the moment to say it. A warning an operator can still act on
	// is worth more than a guard this command is structurally unable to make.
	return []string{line,
		"🔴 the recovered rows hold secrets sealed by each instance's root key, and no " +
			"database backup contains those keys. Rebuild every instance with `dcctl bootstrap " +
			"--restore-root-key <artifact>`: one bootstrapped without it mints a fresh key, " +
			"comes up clean, and leaves every one of those secrets permanently unreadable"}
}

// dryRunRdbArchiveState is the relational store's archive state as a REHEARSAL is
// allowed to read it: best effort, because a dry run is often aimed at a cluster that
// does not exist yet and failing on an unreachable API server would break the
// rehearsal for the case it serves best.
//
// 🔴 THE ASYMMETRY WITH THE REAL RUN IS DELIBERATE AND ONE-DIRECTIONAL. Install
// FAILS on this read, because acting on a wrong answer there moves a live archiver.
// Here nothing is applied, so a wrong answer costs nothing — but it is still SAID,
// because a rehearsal that quietly assumed the store was absent would predict a
// restore the real run declines to perform. Same shape, same reason, as
// stepRenderConfig's read.
func dryRunRdbArchiveState(ctx context.Context, st *State) clusterArchiveState {
	// 🔴 NOTHING IS READ FOR A RUN THAT RESTORES NOTHING. An ordinary rehearsal must
	// not start contacting an API server for an answer it has no use for — a dry run
	// is routinely aimed at a cluster that does not exist yet, and the reachable
	// failure would be a scary line about the relational store in a plan that never
	// mentions one.
	if !st.Restore.RestoresRelationalStore() {
		return clusterArchiveState{}
	}
	state, err := readRdbArchiveState(ctx, st.KubeContext)
	if err == nil {
		return state
	}
	fmt.Println(color.YellowString(
		"  could not read the relational store's archive state (%v); the rehearsal below "+
			"assumes it is not there, which the real run will refuse to assume", err))
	return clusterArchiveState{}
}

// readRdbArchiveState is the seam the rehearsal reads the CLUSTER's relational store
// through. Indirected for the same reason readLiveArchiveState is: the branch behind it
// decides between "this recovery will run" and "this recovery is a no-op", and that
// decision has to be testable without standing up CloudNativePG — and, just as much,
// without a test quietly reaching the developer's own cluster and passing because of
// what happens to be in it.
var readRdbArchiveState = func(ctx context.Context, kubeContext string) (clusterArchiveState, error) {
	dyn, _, _, err := kubeClients(kubeContext)
	if err != nil {
		return clusterArchiveState{}, err
	}
	return clusterArchivePath(ctx, dyn, infraNamespace, RdbClusterName)
}
