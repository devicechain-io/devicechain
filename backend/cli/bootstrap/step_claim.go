// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	"github.com/fatih/color"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// stepDeclareInstance records what this run is about to build, then reads it back
// as the input to everything downstream.
//
// 🔴 THIS STEP IS WHAT MAKES THE INSTANCE CR REAL. The type, its closed-list
// guards and the read/write functions all landed with the schema, and until this
// step existed NOTHING CALLED THEM — a declaration that no run ever wrote and no
// run ever read. The pipeline went on deriving everything from flags, so every
// property the schema enforces (immutability across re-runs, the restore fact
// carried forward, validation against the area catalog) was enforced against an
// object that did not exist outside its own tests.
//
// The order inside the step is deliberate: lock, then declare. Writing the
// declaration first would mean the write itself is the race — two operators
// producing two different specs on one object, with the loser's values lingering
// in whichever fields the winner did not set.
func stepDeclareInstance(ctx context.Context, st *State) error {
	spec := InstanceSpecFrom(st, st.Binding, st.Provider)

	if st.DryRun {
		// 🔴 A dry run takes no lock and writes no declaration, because both are
		// writes and a plan that mutates the cluster is not a plan. It still
		// REPORTS the claim it would have met — an operator asking "what would
		// this do" is asking a question whose answer includes "refuse, because
		// somebody else is already running".
		doing("declaring the instance")
		fmt.Println()
		wouldDo(fmt.Sprintf("declare instance %q (provider %s, profile %s)",
			st.Instance, spec.Provider, spec.Profile))
		return nil
	}

	if err := ValidateInstanceSpec(spec); err != nil {
		return err
	}

	doing(fmt.Sprintf("declaring instance %q", st.Instance))
	if err := WriteInstanceCR(ctx, st.KubeContext, st.Instance, spec, st.DcctlVersion); err != nil {
		return err
	}
	done()

	// 🔴 READ IT BACK, AND USE WHAT COMES BACK. A declaration that is written and
	// never read is a parallel copy of the flags, and copies drift: the API server
	// applies defaults, CEL rules and the immutability checks on the way in, so
	// what lands is not necessarily what was sent. Reading it is what makes the
	// object the source of truth rather than a log of it.
	inst, err := ReadInstanceCR(ctx, st.KubeContext, st.Instance)
	if err != nil {
		return err
	}
	if inst == nil {
		// We wrote it one call ago. Its absence now is not a fresh install, it is
		// something deleting declarations underneath a running bootstrap.
		return fmt.Errorf("the declaration for instance %q was not there immediately after being written; "+
			"something else is deleting it", st.Instance)
	}
	if err := ValidateInstanceSpec(inst.Spec); err != nil {
		return fmt.Errorf("the declaration read back from the cluster is not usable: %w", err)
	}
	applyDeclaration(st, inst.Spec)
	return nil
}

// reportExistingClaim prints who holds the cluster lock, without taking it.
func reportExistingClaim(ctx context.Context, st *State) error {
	_, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		// A dry run against an unreachable cluster still has a plan worth
		// printing; the claim is the one part of it we cannot know.
		fmt.Println(color.YellowString("  could not check whether another operator holds this cluster (%v)", err))
		return nil
	}
	if st.OperatorNamespace == "" {
		return nil
	}
	lease, err := PeekClaim(ctx, typed, st.OperatorNamespace)
	if err != nil {
		fmt.Println(color.YellowString("  could not check whether another operator holds this cluster (%v)", err))
		return nil
	}
	if lease == nil {
		fmt.Println("  the cluster lock is free")
		return nil
	}
	fmt.Println(color.YellowString("  %s", heldError(lease, st.KubeContext).Error()))
	return nil
}

// applyDeclaration feeds the read-back declaration into the state the rest of the
// pipeline runs on, so downstream steps consume the DECLARATION and not the flags
// that produced it.
//
// Only the fields a re-run can legitimately inherit are taken. The cluster binding
// and provider are immutable and already match; the image source was resolved in
// the command layer and is what the declaration was built from.
func applyDeclaration(st *State, spec dcv1beta1.InstanceSpec) {
	st.Profile = spec.Profile
	st.HA = spec.HA
	st.Compact = spec.Compact
	st.NoMonitoring = !spec.Monitoring
	st.NoCNPG = !spec.CNPG
	// 🔴 GrafanaSSO is deliberately NOT written back. InstanceSpecFrom records the
	// RESOLVED value (grafanaSSOEnabled), while st.GrafanaSSO holds what was
	// REQUESTED — and stepRenderConfig compares the two to warn when SSO was asked
	// for and could not be switched on. Copying the resolved value over the request
	// makes them equal, which silences that warning by erasing its input rather
	// than by fixing anything. The two fields mean different things; only one of
	// them is a request.
	st.IngressHost = spec.Host
	st.NoTLS = !spec.TLS
	if len(spec.ExtraFunctionalAreas) > 0 {
		st.EnableAreas = append([]string(nil), spec.ExtraFunctionalAreas...)
	}
}
