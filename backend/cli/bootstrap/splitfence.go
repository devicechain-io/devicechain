// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"strings"
)

// preSplitStateAddresses are the cluster PREREQUISITES an instance built before the
// root split still has in its per-instance state, and which the instance root no
// longer declares because they moved to the cluster root.
//
// 🔴 WHY THIS IS THE MOST DANGEROUS LIST IN THIS PACKAGE. OpenTofu destroys what
// leaves the configuration, and "leaves the configuration" is exactly what the split
// does to every address below. An apply over pre-split state would not fail — it
// would succeed, having destroyed the CNPG operator, cert-manager, ingress-nginx,
// monitoring, the object store holding every backup archive, the shared
// infrastructure namespace, and `module.cnpg_rdb.helm_release.cluster`: the
// relational database holding every tenant, user, device and relationship for every
// instance on the cluster.
//
// 🔴 AND prevent_destroy CANNOT HELP, which is the whole reason this is a Go fence
// and not a lifecycle block. `modules/cnpg-cluster/main.tf` puts
// `prevent_destroy = true` on the database release precisely to stop a naive destroy
// — but a resource removed from the configuration is ORPHANED, and orphans are
// destroyed without consulting a lifecycle block, because there is no longer a
// lifecycle block to consult. `instance/main.tf`'s cutover guard records that same
// behaviour measured against a copy of a real state file: the plan succeeds, exit 0,
// and the resource is marked for destruction.
//
// 🔑 THE ADDRESSES ARE LITERALS, AND THEY HAVE TO BE. They name resources this root
// no longer declares, so there is nothing to derive them from and nothing that will
// fail to compile if one is wrong. A missing entry is a hole that fails SILENTLY in
// the one direction that matters: the run proceeds and the apply destroys the
// database.
//
// ✅ DERIVED BY ENUMERATION FROM THE PRE-SPLIT TREE, NOT FROM THE SPLIT'S OWN DIFF.
// That distinction is the point. This fence ships in the SAME change as the split, so
// an author listing "what I moved" would be writing a control that cannot detect its
// own subject — it would agree with the diff by construction and catch nothing the
// diff got wrong. Instead the pre-split root was planned against an EMPTY state and
// its addresses enumerated; this list is that set of 17 minus the four the instance
// root still declares (`module.nats.helm_release.nats`,
// `module.nats.kubernetes_config_map_v1.nats_ca[0]`,
// `module.cnpg_tsdb.helm_release.cluster`, `terraform_data.cutover_guard["tsdb"]`)
// and the three in deliberatelyNotFenced below. 10 + 4 + 3 = 17, and
// TestEveryPreSplitAddressIsAccountedFor is what holds that arithmetic to the tree.
//
// ✅ MEASURED 2026-09-14 against the pre-split tree on an empty kind cluster, over
// six variable combinations, because count-indexed addresses are what a single plan
// misses: defaults (17 addresses), ha=true (17), nats_enable_tls=false (16),
// enable_cnpg=false (11), backup_destination=external (14),
// enable_database_backups=false (12). The union is 17 and `defaults` is the superset
// — no variant produced an address it lacks.
//
//	kind create cluster --name dc-split-probe
//	git archive <commit-before-the-split> deploy/opentofu | tar -x -C /tmp/pre
//	cd /tmp/pre/opentofu/instance && terraform init -backend=false
//	terraform plan -refresh=false -var kubeconfig_context=kind-dc-split-probe -out=p.bin
//	terraform show -json p.bin | jq -r '.resource_changes[].address'
//
// 🔴 THE RECIPE NEEDS A REACHABLE CLUSTER, AND fence.go USED TO CLAIM OTHERWISE.
// Its version of this comment said the enumeration runs "without standing anything
// up, so it can be re-run by anyone". It never could: `main.tf` has carried
// `data.kubernetes_resources.legacy_db_statefulsets` since #561 and
// `data.kubernetes_resources.object_store_pvc` since #563, both of which a plan must
// resolve against a live API, and no variable combination removes either. An EMPTY
// kind cluster is enough — nothing needs to be installed on it — but something has
// to answer. That correction is why the line above says `kind create cluster`.
var preSplitStateAddresses = []string{
	// The shared infrastructure namespace. Destroying it CASCADES: the broker, both
	// databases and the object store all live in it, so this single address is a
	// whole-instance loss on its own — and a whole-CLUSTER loss once `rdb` is shared.
	"module.namespace.kubernetes_namespace_v1.this[0]",

	// The shared relational database. The one address on this list whose destruction
	// takes data belonging to instances OTHER than the one being applied.
	"module.cnpg_rdb.helm_release.cluster",

	// The CloudNativePG operator and the backup plugin that extends it. Destroying
	// the operator leaves every Cluster object in the cluster unreconciled.
	"module.cnpg[0].helm_release.cnpg",
	"module.cnpg[0].helm_release.barman_plugin[0]",

	// The cluster prerequisites proper.
	"module.cert_manager[0].helm_release.cert_manager",
	"module.ingress_nginx[0].helm_release.ingress_nginx",
	"module.monitoring[0].helm_release.kube_prometheus_stack",

	// The backup object store, and the PVC is the sharp edge: on a default
	// StorageClass with reclaimPolicy Delete, destroying it takes the retention
	// window's worth of archives for BOTH stores with it.
	"module.object_store[0].kubernetes_deployment_v1.this",
	"module.object_store[0].kubernetes_service_v1.this",
	"module.object_store[0].kubernetes_persistent_volume_claim_v1.data",
}

// deliberatelyNotFenced are addresses a pre-split instance ALSO holds that this fence
// does not refuse, each with the reason — because an unexplained absence from a list
// like this one is indistinguishable from an oversight.
//
// 🔑 All three are `terraform_data`, which is a STATE-ONLY resource: it has no object
// in the cluster, so orphaning one destroys nothing. They are plan-time guards, and
// the protection they give is given by being EVALUATED, not by being retained. An
// instance that reaches this fence is refused by the real resources below regardless,
// so listing these would add no refusal — it would only make the fence look like it
// was protecting something it is not.
//
// 🔴 AND cutover_guard COULD NOT BE LISTED HONESTLY EVEN IF IT MATTERED. It is one
// `for_each` block over `local.legacy_db_statefulsets`; the split moves the `rdb` key
// to the cluster root and keeps the `tsdb` key here, so the BLOCK is still declared by
// the instance root while one of its keys is not. "Does this root still declare it?"
// has no block-level answer for a for_each'd resource, and a fence that cannot state
// its own criterion is a fence nobody can check.
var deliberatelyNotFenced = map[string]string{
	"terraform_data.cutover_guard[\"rdb\"]":      "state-only, and the block stays declared for its tsdb key",
	"terraform_data.backup_destination_guard[0]": "state-only; the guard protects by being evaluated, not by being kept",
	"terraform_data.backup_removal_guard":        "state-only; the PVC it guards is fenced above in its own right",
}

// checkNoPreSplitInfrastructure refuses to apply the instance root over state written
// before the cluster prerequisites moved to their own root.
//
// 🔴 IT IS KEYED ON EVIDENCE THE INSTANCE WAS BUILT, NOT ON A VERSION NUMBER. A
// version recorded in the declaration says what built the instance; the STATE says
// what that build left behind, and it is the state the apply is about to act on. The
// two come apart exactly where it matters — an instance rebuilt halfway, a
// declaration restored from elsewhere, a state file older than the binary that wrote
// it. fence.go states the same rule for the same reason.
//
// 🔑 AND IT FAILS CLOSED ON "CANNOT TELL". stateAddressesPresent returns an error
// rather than an empty set when the state cannot be read, and that error stops the
// run: reading an unreadable state as "nothing pre-split here" is precisely the
// reading that lets the apply through to destroy the database.
//
// 🔴 NO CARVE-OUTS, AND THE RESTORE PATH IS THE REASON TO SAY SO EXPLICITLY. There is
// no `dcctl restore` — restore is a set of flags on `bootstrap`, so it runs the full
// pipeline including this apply, and `createonly.go` deliberately exempts it from the
// live-instance refusal because a recovery is the run most likely to need repeating.
// That makes restore-as-retry the ONE supported way to point a new binary at old
// state, and it happens during an incident. A fence that inherited that exemption
// would be absent from the only path that can actually reach the hazard.
//
// 🔴 WHY REFUSE RATHER THAN MIGRATE. `tofu state mv` could in principle move these
// addresses into the cluster root's state, and it is the wrong answer twice over.
// Pre-GA convention rejects migration scaffolding for old shapes; and a state move
// that fails partway leaves two state files disagreeing about which one owns the
// database, which is worse than either state alone. Recreating is honest: destroy
// takes the instance's data with it, the operator is told that, and the shared
// prerequisites are rebuilt by the cluster root on the next bootstrap.
func checkNoPreSplitInfrastructure(ctx context.Context, tf stateLister, instance string) error {
	found, err := stateAddressesPresent(ctx, tf, preSplitStateAddresses)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return nil
	}
	return fmt.Errorf(
		"instance %q was built by a dcctl that kept the cluster prerequisites in this "+
			"instance's own infrastructure state, and this build keeps them in a separate "+
			"cluster root. Applying over it would DESTROY them, because OpenTofu destroys "+
			"what leaves the configuration — %d such resource(s) are still in this "+
			"instance's state:\n  %s\n"+
			"That includes the shared relational database, which holds data for every "+
			"instance on this cluster, and the object store holding every backup archive. "+
			"OpenTofu's prevent_destroy does NOT stop this: removing a module block orphans "+
			"its resources, and orphans are destroyed without consulting their lifecycle "+
			"rules.\n"+
			"There is no in-place upgrade for this: destroy the instance and bootstrap it "+
			"again (`dcctl destroy %s` then `dcctl bootstrap %s`). Back up anything you need "+
			"first — a destroy takes the databases with it",
		instance, len(found), strings.Join(found, "\n  "), instance, instance)
}

// stateAddressesPresent reports which of the given addresses the state holds.
//
// It reads the state ONCE and answers for every address, rather than asking per
// address the way checkNoRetiredInfrastructure does. Each read shells out to the
// tofu binary and re-parses the whole state document, so a per-address read makes the
// cost of a fence proportional to the length of its list — and this list is the
// longer of the two, on the path an operator runs during an incident.
func stateAddressesPresent(ctx context.Context, tf stateLister, addresses []string) ([]string, error) {
	state, err := tf.Show(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the infrastructure state: %w", err)
	}
	if state == nil || state.Values == nil || state.Values.RootModule == nil {
		return nil, nil
	}
	var found []string
	for _, address := range addresses {
		if moduleHasAddress(state.Values.RootModule, address) {
			found = append(found, address)
		}
	}
	return found, nil
}
