// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
)

// preSplitMeasuredAddresses is every address the PRE-SPLIT instance root put in a
// real instance's state.
//
// 🔴 THIS IS A FIXTURE FROM THE OLD VERSION, AND THAT IS THE ONLY THING THAT MAKES IT
// WORTH HAVING. The rule this arc keeps relearning: where a change crosses a version
// boundary, the fixture must come from the OLD version, not from the test and not
// from the change. A list derived from "what the split moved" would agree with the
// split by construction and could not detect the split getting it wrong — which is
// precisely the risk of shipping the fence in the same change as the split.
//
// ✅ MEASURED 2026-09-14 by planning the pre-split root against an EMPTY state on an
// empty kind cluster, over six variable combinations (defaults, ha, no-TLS, no-CNPG,
// external backups, no backups). `defaults` is the superset at 17; no variant
// produced a eighteenth. The recipe is recorded on preSplitStateAddresses.
var preSplitMeasuredAddresses = []string{
	"module.cert_manager[0].helm_release.cert_manager",
	"module.cnpg[0].helm_release.barman_plugin[0]",
	"module.cnpg[0].helm_release.cnpg",
	"module.cnpg_rdb.helm_release.cluster",
	"module.cnpg_tsdb.helm_release.cluster",
	"module.ingress_nginx[0].helm_release.ingress_nginx",
	"module.monitoring[0].helm_release.kube_prometheus_stack",
	"module.namespace.kubernetes_namespace_v1.this[0]",
	"module.nats.helm_release.nats",
	"module.nats.kubernetes_config_map_v1.nats_ca[0]",
	"module.object_store[0].kubernetes_deployment_v1.this",
	"module.object_store[0].kubernetes_persistent_volume_claim_v1.data",
	"module.object_store[0].kubernetes_service_v1.this",
	"terraform_data.backup_destination_guard[0]",
	"terraform_data.backup_removal_guard",
	"terraform_data.cutover_guard[\"rdb\"]",
	"terraform_data.cutover_guard[\"tsdb\"]",
}

// 🔴🔴 THE WATCHER FOR THE FENCE ITSELF, AND THE REASON THIS FILE EXISTS.
//
// Every address a pre-split instance holds must be accounted for in exactly ONE of
// three ways: the instance root still declares it, the fence refuses it, or it is
// named in deliberatelyNotFenced with a reason. An address in NONE of them is the
// silent hole — the apply proceeds and destroys that resource while reporting
// success. An address in TWO of them is a contradiction about who owns it.
//
// 🔑 The instance root's half is read from the EMBEDDED tree rather than from a
// second literal list, so the two halves cannot agree with each other while both
// disagreeing with what ships. Move another module to the cluster root and forget the
// fence, and this fails naming the address — which is the edit most likely to be made
// by someone who has stopped thinking about pre-split state.
func TestEveryPreSplitAddressIsAccountedFor(t *testing.T) {
	declared := instanceRootOwners(t)
	fenced := make(map[string]bool, len(preSplitStateAddresses))
	for _, a := range preSplitStateAddresses {
		fenced[a] = true
	}

	for _, address := range preSplitMeasuredAddresses {
		owner := ownerOf(address)
		_, excused := deliberatelyNotFenced[address]

		var in []string
		if declared[owner] && !excused {
			// An excused address may legitimately still have its BLOCK declared —
			// that is cutover_guard's whole situation — so being declared only counts
			// as an account when nothing else claims it.
			in = append(in, fmt.Sprintf("still declared by the instance root (as %q)", owner))
		}
		if fenced[address] {
			in = append(in, "fenced as pre-split")
		}
		if excused {
			in = append(in, fmt.Sprintf("excused (%s)", deliberatelyNotFenced[address]))
		}

		switch len(in) {
		case 1: // exactly one account: correct
		case 0:
			t.Errorf("%q is accounted for NOWHERE: the instance root declares nothing named "+
				"%q, it is not in preSplitStateAddresses, and it is not excused.\n"+
				"  An apply over a pre-split instance would ORPHAN it, and orphans are "+
				"destroyed without consulting prevent_destroy. Add it to the fence.", address, owner)
		default:
			t.Errorf("%q is accounted for %d times — %s. Exactly one must be true, or the "+
				"fence and the tree disagree about who owns it.", address, len(in), strings.Join(in, "; "))
		}
	}
}

// Every excuse must name an address the pre-split root actually created. An excuse for
// something that was never there is a line nobody can check, and it makes the list
// above look more considered than it is — the same reason the embed guard refuses an
// exemption with no file behind it.
func TestNothingIsExcusedThatWasNeverThere(t *testing.T) {
	measured := make(map[string]bool, len(preSplitMeasuredAddresses))
	for _, a := range preSplitMeasuredAddresses {
		measured[a] = true
	}
	for address, reason := range deliberatelyNotFenced {
		if !measured[address] {
			t.Errorf("%q is excused from the fence (%q) but the pre-split root never created "+
				"it. Remove the excuse, or re-run the enumeration.", address, reason)
		}
		if reason == "" {
			t.Errorf("%q is excused with no reason given", address)
		}
	}
}

// The counterweight to the list being a literal: if the pre-split root really put
// seventeen addresses in state, a shorter list here is a coverage set that shrank
// without anything saying so. The number is measured, so changing it means re-running
// the enumeration rather than adjusting a constant to match.
func TestThePreSplitMeasurementIsTheWholeMeasuredSet(t *testing.T) {
	if got, want := len(preSplitMeasuredAddresses), 17; got != want {
		t.Fatalf("the pre-split root was measured to create %d addresses, this list has %d. "+
			"Re-run the enumeration on preSplitStateAddresses rather than editing the count.", want, got)
	}
}

// 🔴 AN INSTANCE BUILT BEFORE THE SPLIT MUST BE REFUSED, and the refusal has to name
// what it found and what to do. The address is a literal for the same reason the list
// is: it names a resource this root no longer declares, so nothing would fail to
// compile if it moved.
func TestAnInstanceBuiltBeforeTheSplitIsRefused(t *testing.T) {
	f := &fakeState{addresses: []string{"module.cnpg_rdb.helm_release.cluster"}}

	err := checkNoPreSplitInfrastructure(context.Background(), f, "prod")
	if err == nil {
		t.Fatal("an instance whose state still holds the shared relational database was " +
			"accepted: the apply would destroy every instance's control-plane data")
	}
	for _, want := range []string{
		"module.cnpg_rdb.helm_release.cluster", // which resource
		"dcctl destroy prod",                   // what to do about it
		"prevent_destroy",                      // why the guard they know about did not save them
		// ...and the case where that remedy is not enough: destroy leaves a cluster
		// dcctl did not create running, prerequisites and all.
		"--kube-context",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// 🔴 EVERY FENCED ADDRESS MUST FIRE ON ITS OWN, not just the first one anybody thought
// of. A missing entry is a silent hole: that one resource is destroyed on an apply
// that reports success. Each is exercised alone, so a list that happens to catch an
// instance through a DIFFERENT entry cannot hide a dead one.
func TestEveryFencedAddressRefusesOnItsOwn(t *testing.T) {
	for _, address := range preSplitStateAddresses {
		t.Run(address, func(t *testing.T) {
			f := &fakeState{addresses: []string{address}}
			if err := checkNoPreSplitInfrastructure(context.Background(), f, "prod"); err == nil {
				t.Errorf("state holding only %q was accepted; the apply would destroy it", address)
			}
		})
	}
}

// ...and the half that makes this a guard rather than a ban: an instance built by a
// post-split binary holds only what the instance root still declares, and must pass.
//
// 🔑 These four are the set difference the fence was derived from, written out so a
// change to either side has to confront this test.
func TestAPostSplitInstancePasses(t *testing.T) {
	f := &fakeState{addresses: []string{
		"module.nats.helm_release.nats",
		"module.nats.kubernetes_config_map_v1.nats_ca[0]",
		"module.cnpg_tsdb.helm_release.cluster",
		"terraform_data.cutover_guard[\"tsdb\"]",
	}}
	if err := checkNoPreSplitInfrastructure(context.Background(), f, "prod"); err != nil {
		t.Errorf("an instance built after the split was refused: %v", err)
	}
}

// A fresh instance has no state at all, and must not be refused for having none.
func TestAnInstanceWithNoStatePasses(t *testing.T) {
	if err := checkNoPreSplitInfrastructure(context.Background(), &fakeState{}, "prod"); err != nil {
		t.Errorf("an instance with empty state was refused: %v", err)
	}
}

// 🔴 FAIL CLOSED. Reading an unreadable state as "nothing pre-split here" is exactly
// the reading that lets the apply through to destroy the database, so a state that
// cannot be read is an error and not a pass.
func TestAnUnreadableStateStopsTheRun(t *testing.T) {
	boom := errors.New("state file is corrupt")
	err := checkNoPreSplitInfrastructure(context.Background(), &fakeState{showErr: boom}, "prod")
	if err == nil {
		t.Fatal("an unreadable state was treated as clean; the fence must fail closed")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the underlying read error was swallowed: %v", err)
	}
}

// ownerOf reduces a state address to the thing a root declares: a module block name,
// or a root-level resource's name. `module.object_store[0].kubernetes_service_v1.this`
// and `module.object_store[0].kubernetes_deployment_v1.this` share one owner, which is
// what makes "does the root still declare this?" answerable from source.
func ownerOf(address string) string {
	if rest, ok := strings.CutPrefix(address, "module."); ok {
		name, _, _ := strings.Cut(rest, ".")
		name, _, _ = strings.Cut(name, "[")
		return "module " + name
	}
	// A root-level resource: `terraform_data.cutover_guard["rdb"]` is declared as
	// `resource "terraform_data" "cutover_guard"`.
	typ, rest, ok := strings.Cut(address, ".")
	if !ok {
		return address
	}
	name, _, _ := strings.Cut(rest, "[")
	return "resource " + typ + " " + name
}

var (
	moduleBlockRe   = regexp.MustCompile(`(?m)^module\s+"([^"]+)"\s*\{`)
	resourceBlockRe = regexp.MustCompile(`(?m)^resource\s+"([^"]+)"\s+"([^"]+)"\s*\{`)
)

// instanceRootOwners reads what the instance root declares TODAY, from the embedded
// tree — the same bytes dcctl ships and applies, not a copy on disk and not a second
// literal list in this file.
func instanceRootOwners(t *testing.T) map[string]bool {
	t.Helper()

	owners := map[string]bool{}
	root := assets.OpenTofuInstance()
	for _, name := range []string{"main.tf", "outputs.tf", "variables.tf", "versions.tf", "providers.tf"} {
		b, err := fs.ReadFile(root, name)
		if err != nil {
			continue // a root need not have every file; main.tf is checked below
		}
		src := string(b)
		for _, m := range moduleBlockRe.FindAllStringSubmatch(src, -1) {
			owners["module "+m[1]] = true
		}
		for _, m := range resourceBlockRe.FindAllStringSubmatch(src, -1) {
			owners["resource "+m[1]+" "+m[2]] = true
		}
	}
	// 🔑 The parser's own control. A regex that matched nothing would report that the
	// instance root declares NOTHING, which would make every address look fenced-or-
	// missing and turn the test above into one that cannot pass for the right reason.
	if len(owners) == 0 {
		t.Fatal("no module or resource blocks were parsed out of the embedded instance root: " +
			"the parser is reading nothing, so the coverage check above means nothing. Fix the parser.")
	}
	return owners
}
