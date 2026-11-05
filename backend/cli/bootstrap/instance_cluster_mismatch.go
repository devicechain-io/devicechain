// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RefuseInstanceOnAnotherCluster stops a bootstrap whose instance name already has local
// state recorded against a DIFFERENT cluster than the one this run is aimed at.
//
// 🔴 LOCAL STATE IS KEYED BY INSTANCE NAME, NOT BY CLUSTER. ~/.devicechain/instances/<name>
// holds the OpenTofu state and the broker credentials of whichever cluster the name was
// last built on. When that cluster is destroyed outside dcctl (a `tofu destroy`, a deleted
// kind cluster or cloud project) the directory outlives it, and a fresh bootstrap of the
// same name on a new cluster would plan against the dead cluster's applied outputs. The
// first symptom used to be an unrelated-looking refusal (a broker-teardown guard naming
// servers that do not exist on the target); this names the real cause up front.
//
// It fails closed on the one signal that is reliable: both UIDs known and unequal. A record
// from before clusterUid was recorded, or a run whose cluster could not be identified,
// answers nil, which keeps today's behaviour.
//
// It never deletes or reads the credential files. The directory holds broker credentials,
// so the remedy is to move it aside, not to remove it.
func RefuseInstanceOnAnotherCluster(instance string, target ClusterBinding, liveUID string) error {
	if liveUID == "" {
		return nil
	}
	rec, err := ReadInstanceRecord(instance)
	if err != nil || rec.ClusterUID == "" || rec.ClusterUID == liveUID {
		return nil
	}
	dir, derr := instanceRoot(instance)
	if derr != nil {
		dir = "~/.devicechain/instances/" + instance
	}
	// Outside instances/: a copy left inside is enumerated by `instances list` and
	// `destroy --all` as an instance whose record names another one.
	aside := filepath.Join(filepath.Dir(filepath.Dir(dir)), instance+".stale-state")
	return fmt.Errorf("instance %q already has local state on this machine, and it belongs to a "+
		"different cluster than the one this run targets.\n"+
		"  recorded cluster: %s (kube-system UID %s)\n"+
		"  target cluster:   %s (kube-system UID %s)\n"+
		"  That cluster may have been destroyed outside dcctl. Its OpenTofu state and broker "+
		"credentials are kept under %s, and building on top of them would plan against a cluster "+
		"that is not there.\n"+
		"  If that cluster is gone, move the directory aside (it holds credentials, so keep it "+
		"rather than deleting it) and run this command again:\n\n"+
		"    mv \"%s\" \"%s\"\n\n"+
		"  (PowerShell: Move-Item -LiteralPath '%s' -Destination '%s')\n\n"+
		"  If the recorded cluster is still running, choose a different instance name for this "+
		"one, or point --kube-context at it",
		instance, rec.describeForMismatch(), rec.ClusterUID, target.Describe(), liveUID, dir,
		dir, aside, psQuote(dir), psQuote(aside))
}

func (r InstanceRecord) describeForMismatch() string { return r.Binding().Describe() }

// psQuote escapes a path for a PowerShell single-quoted literal.
func psQuote(p string) string { return strings.ReplaceAll(p, "'", "''") }
