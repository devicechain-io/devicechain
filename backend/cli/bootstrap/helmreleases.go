// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"sort"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
)

// liveReleaseStates are the release states that mean resources are still in the cluster.
//
// 🔴 EVERYTHING EXCEPT UNINSTALLED, AND THE DEFAULT WOULD HAVE BEEN WRONG. action.List
// defaults to ListDeployed, which answers "which releases are healthy" — a different
// question from the one asked here, which is "is anything of ours still in this
// cluster". A release whose install FAILED part-way has objects in the cluster and a
// record in the store; read as absent, it would let a second instance be built on top of
// the wreckage of the first. Pending and uninstalling states are live for the same
// reason. Uninstalled records are the one genuine absence: they survive only with
// --keep-history, and what they describe is gone.
const liveReleaseStates = action.ListAll &^ action.ListUninstalled

// deviceChainReleases names every DeviceChain release this cluster holds, with the
// instance each belongs to.
//
// 🔴 A LIST, NOT A LOOKUP, AND THE RENAME IS WHY. While every instance installed under
// one constant name, "does this cluster hold a DeviceChain release" was answerable by
// asking for that one name. It is not any more: an instance-derived name cannot be
// guessed without already knowing the instance, which is the thing being asked. Keying
// the reader on helmReleaseNameFor(thisRun) would make every cluster look empty to every
// instance but its own — a second bootstrap would sail past the boundary and install a
// parallel set of workloads beside the running ones.
//
// 🔑 IT ALSO STILL FINDS THE LEGACY RELEASE, AND THAT IS NOT A SIDE EFFECT. Releases
// before v0.17.0 wrote no declaration and stamped no Secrets, so the release is the ONLY
// artifact naming those instances. A reader that recognised releases by name would have
// stopped seeing them at the rename, and a pre-v0.17.0 cluster would have read as empty
// — the one cluster state where reading empty is most expensive, because nothing else
// left behind would have contradicted it.
//
// Recognition is by CHART rather than by name for the same reason: the name is a label
// this binary chose and an operator may choose differently, while the chart metadata is
// written by the chart itself. A cluster's other Helm releases are not ours to reason
// about, and inferring ownership from a "dc-" prefix would claim some of them.
func deviceChainReleases(cfg *action.Configuration) ([]string, error) {
	names, err := deviceChainReleaseNames(cfg)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var ids []string
	for _, name := range names {
		// 🔴 THE SAME ATTRIBUTION READER THE UNINSTALL USES, DELIBERATELY. Two ways of
		// deciding which instance a release belongs to could disagree, and the pair that
		// would disagree here is the worst one available: this reader decides whether a
		// bootstrap is refused, and that one decides whether an uninstall proceeds. Its
		// three answers carry over unchanged, including the one that matters — a release
		// that cannot be attributed is an ERROR, never an absence.
		id, present, err := releaseInstanceNamed(cfg, name)
		if err != nil {
			return nil, err
		}
		if !present {
			// Listed a moment ago and gone now: a concurrent uninstall. Nothing to
			// attribute, and nothing to refuse over.
			continue
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// deviceChainReleaseNames lists the releases in helmReleaseNamespace that were installed
// from the DeviceChain chart.
//
// Split from the attribution above so the filter can be exercised against a fabricated
// release list — the cluster half of this is one Helm call, and the half that decides
// which releases are OURS is the half that can be wrong in a way no test with a real
// cluster would reliably produce.
func deviceChainReleaseNames(cfg *action.Configuration) ([]string, error) {
	list := action.NewList(cfg)
	list.All = true
	list.StateMask = liveReleaseStates
	rels, err := list.Run()
	if err != nil {
		return nil, fmt.Errorf("listing the Helm releases in namespace %s, to ask which "+
			"instances this cluster holds: %w. Refusing to continue rather than read a cluster "+
			"that will not answer as an empty one", helmReleaseNamespace, err)
	}
	return releaseNamesFromChart(rels), nil
}

// releaseNamesFromChart picks out the releases installed from the DeviceChain chart.
//
// 🔑 A RELEASE WITH NO CHART METADATA IS NOT OURS. Helm can return a record whose chart
// could not be loaded, and the tempting reading — "include it, failing closed is safer" —
// is the wrong one here: this list feeds a refusal, so including a release we cannot
// identify would refuse bootstraps into clusters holding somebody else's broken release.
// The failing-closed direction that DOES matter is one level up, where a release we have
// identified as ours but cannot attribute to an instance is an error rather than a skip.
func releaseNamesFromChart(rels []*release.Release) []string {
	var names []string
	for _, rel := range rels {
		if rel == nil || rel.Chart == nil || rel.Chart.Metadata == nil {
			continue
		}
		if rel.Chart.Metadata.Name != helmChartName {
			continue
		}
		names = append(names, rel.Name)
	}
	sort.Strings(names)
	return names
}
