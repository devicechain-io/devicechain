// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"time"

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
type RestoreFlags struct {
	RdbFrom        string
	RdbTargetTime  string
	TsdbFrom       string
	TsdbTargetTime string
	// BackupsEnabled is what this run's OTHER flags settled about the backup
	// destination — see DatabaseBackupsEnabled. Passed in rather than recomputed so
	// there is one derivation of it in the CLI.
	BackupsEnabled bool
}

// RestorePlan is the settled database-restore intent for one bootstrap run: which
// archive each store recovers FROM, and how far it replays.
//
// 🔴 REBUILD-TIME ONLY. `spec.bootstrap` is read when CloudNativePG CREATES a
// Cluster, so a plan aimed at a store that already exists does nothing at all — no
// error, no restore, a green apply. stepRenderConfig says so out loud when it finds
// the Cluster already there, because "it ran and restored nothing" and "it declined
// to run" are indistinguishable from the outside.
type RestorePlan struct {
	RdbFrom        string
	RdbTargetTime  string
	TsdbFrom       string
	TsdbTargetTime string
}

// Active reports whether this run restores anything.
func (p RestorePlan) Active() bool { return p.RdbFrom != "" || p.TsdbFrom != "" }

// DatabaseBackupsEnabled reports whether this combination of flags leaves the
// instance with a backup destination — i.e. whether OpenTofu will be told
// enable_database_backups=true.
//
// 🔴 This is a SECOND statement of what infraVars emits, and the two are pinned
// against each other over the whole flag matrix by
// TestDatabaseBackupsEnabledMatchesWhatIsEmitted. It exists separately because the
// emitter builds a var LIST inside two unrelated conditional blocks, and the
// restore path needs the same answer as a boolean, in the first second of the run,
// before any cluster — not after parsing argv it has not built yet.
//
// Both halves are consequences, not choices: --no-cnpg skips the operator the
// plugin extends, and --compact --no-tls drops cert-manager, which the plugin needs
// for its own Issuer and Certificates. See tofu.go for the full reasoning.
func DatabaseBackupsEnabled(noCNPG, compact, noTLS bool) bool {
	return !noCNPG && !(compact && noTLS)
}

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

	// A target time with nothing to restore is silently ignored by the root, which
	// is the wrong outcome for a flag whose whole purpose is to stop replay before
	// a known-bad moment: the operator would get a full-archive restore and be told
	// nothing. (The root refuses this too — this refusal is here so it lands before
	// the rebuild rather than at plan time.)
	for _, c := range []struct{ target, source, flag, sourceFlag string }{
		{f.RdbTargetTime, f.RdbFrom, "--restore-rdb-at", "--restore-rdb-from"},
		{f.TsdbTargetTime, f.TsdbFrom, "--restore-tsdb-at", "--restore-tsdb-from"},
	} {
		if c.target != "" && c.source == "" {
			return RestorePlan{}, fmt.Errorf(
				"%s is set but %s is empty, so nothing is being restored and the recovery "+
					"target would be silently ignored", c.flag, c.sourceFlag)
		}
	}

	// Restoring needs the Barman Cloud plugin, and the flags that switch the backup
	// destination off switch the RESTORE path off with it — the same plugin reads
	// the archive that writes it. Without this the run proceeds, OpenTofu refuses at
	// plan time, and the operator is told to set a variable dcctl does not expose.
	if plan.Active() && !f.BackupsEnabled {
		return RestorePlan{}, fmt.Errorf(
			"--restore-rdb-from/--restore-tsdb-from need the database backup plugin, and this " +
				"run disables it: --no-cnpg skips the CloudNativePG operator the plugin extends, " +
				"and --compact --no-tls drops cert-manager, which the plugin needs for its own " +
				"Issuer and Certificates. Restore with neither (--compact WITH TLS keeps backups)")
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

// liveArchiveState is both database Clusters' current state, read from the cluster.
type liveArchiveState struct {
	Rdb  clusterArchiveState
	Tsdb clusterArchiveState
}

// readLiveArchiveState is the seam stepRenderConfig uses to ask what the two
// database Clusters are ALREADY archiving under. Indirected for the same reason as
// lookupDeployedInstance: the branch behind it decides between "keep the path this
// instance owns" and "retarget a live cluster's WAL archive", and that decision has
// to be testable without standing up CloudNativePG.
var readLiveArchiveState = func(ctx context.Context, kubeContext string) (liveArchiveState, error) {
	restCfg, err := RestConfig(kubeContext)
	if err != nil {
		return liveArchiveState{}, fmt.Errorf("building kube config to read the database archive state: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return liveArchiveState{}, err
	}
	var out liveArchiveState
	for _, s := range []struct {
		name string
		into *clusterArchiveState
	}{
		{RdbClusterName, &out.Rdb},
		{TsdbClusterName, &out.Tsdb},
	} {
		st, err := clusterArchivePath(ctx, dyn, infraNamespace, s.name)
		if err != nil {
			return liveArchiveState{}, err
		}
		*s.into = st
	}
	return out, nil
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
	plugins, _, err := unstructured.NestedSlice(cl.Object, "spec", "plugins")
	if err != nil || len(plugins) == 0 {
		return out, nil
	}
	for _, p := range plugins {
		plug, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if n, _, _ := unstructured.NestedString(plug, "name"); n != barmanPluginName {
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
		if isArchiver, _, _ := unstructured.NestedBool(plug, "isWALArchiver"); !isArchiver {
			continue
		}
		out.Path, _, _ = unstructured.NestedString(plug, "parameters", "serverName")
		return out, nil
	}
	return out, nil
}

// archivePaths is what resolveArchivePaths settles: the serverName each store
// should archive under for the rest of this instance's life. "" means "the
// Cluster's own name", which is the OpenTofu default and every ordinary install.
type archivePaths struct {
	Rdb  string
	Tsdb string
	// AlreadyLive names the Clusters that already exist while a restore was
	// requested for them. Those restores will NOT happen: `spec.bootstrap` is read
	// at CREATE only.
	AlreadyLive []string
}

// resolveArchivePaths decides what each store archives under, given what it is
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
// fresh path is derived instead.
func resolveArchivePaths(live liveArchiveState, plan RestorePlan, now time.Time) archivePaths {
	out := archivePaths{}
	for _, s := range []struct {
		live    clusterArchiveState
		from    string
		target  *string
		cluster string
	}{
		{live.Rdb, plan.RdbFrom, &out.Rdb, RdbClusterName},
		{live.Tsdb, plan.TsdbFrom, &out.Tsdb, TsdbClusterName},
	} {
		switch {
		case s.from == "":
			// No restore: whatever it is archiving under now, including "".
			*s.target = s.live.Path
		case s.live.Path != "" && s.live.Path != s.from:
			// A restored instance being re-run, or re-restored. It already owns a path
			// that is not the source; keep it.
			*s.target = s.live.Path
		default:
			*s.target = RestoredArchivePath(s.from, now)
		}
		if s.from != "" && s.live.Exists {
			out.AlreadyLive = append(out.AlreadyLive, s.cluster)
		}
	}
	return out
}
