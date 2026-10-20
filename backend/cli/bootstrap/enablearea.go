// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"slices"
	"strings"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-k8s/functionalarea"
	"github.com/fatih/color"
)

// AreaChange is what `dcctl enable-area` asked for and what that means for one
// instance: which of the requested areas are new to it, and whether there is anything
// to do at all.
//
// 🔑 ENABLING AN AREA IS AN UPGRADE THAT DOES NOT MOVE THE VERSION. It is not a second
// pipeline. Everything an added area needs — its Deployment, Service, ingress rule and
// configuration, a larger database login, and (under --ha) the event store's reserve
// for a second event-management pod — is something the upgrade already composes from
// the area set, so the day-2 change is the upgrade with the area appended to the
// declaration's delta. A second path would be a second answer to "which values does
// an area set render", and the first time they disagreed an enabled area would be
// pruned by the next ordinary upgrade.
type AreaChange struct {
	// Requested is the normalized request, in the order it was given.
	Requested []string
	// Added are the requested areas this instance does not already run, in request
	// order. These, and only these, are appended to the declaration's delta: an area
	// the profile already deploys is not recorded again.
	Added []string
	// Settled says there is nothing to do: every requested area is already declared,
	// and the declaration says the last run over it ended Ready. See planAreaChange.
	Settled bool
}

// normalizeAreaRequest trims the requested names and drops repeats, refusing an empty
// request or a blank name.
//
// 🔴 A BLANK IS REFUSED, NOT DROPPED. bootstrap drops a blank `--enable-area " "`
// because the flag is optional there and the run has other work; here the names ARE
// the command, and a blank that was dropped silently would turn `dcctl enable-area
// local prod ""` into a full upgrade nobody asked for.
func normalizeAreaRequest(areas []string) ([]string, error) {
	if len(areas) == 0 {
		return nil, fmt.Errorf("name at least one functional area to enable")
	}
	out := make([]string, 0, len(areas))
	for _, a := range areas {
		a = strings.TrimSpace(a)
		if a == "" {
			return nil, fmt.Errorf("an empty functional-area name was given; name the area to enable")
		}
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out, nil
}

// planAreaChange works out the declaration delta an enable-area run records, and which
// of the requested areas it actually adds.
//
// The area set an instance runs is its profile's expansion plus the declared delta
// (ResolveEnabledAreas). A requested area already in that set adds nothing — including
// one the profile deploys, which is NOT appended to the delta: the delta records what
// was asked for on top of the profile, and the profile's own membership is the
// release's to define.
//
// The resulting set is validated whole, with the same catalog bootstrap and the chart
// use, so an unknown name or an unmet hard dependency is refused here, before anything
// is read from or written to the cluster beyond the declaration.
func planAreaChange(profile string, delta, requested []string) (newDelta, added []string, err error) {
	base, err := functionalarea.ResolveEnabled(profile, nil)
	if err != nil {
		return nil, nil, err
	}
	running := make(map[string]bool, len(base)+len(delta))
	for _, a := range base {
		running[string(a)] = true
	}
	for _, a := range delta {
		running[strings.TrimSpace(a)] = true
	}

	newDelta = append([]string(nil), delta...)
	for _, a := range requested {
		if running[a] {
			continue
		}
		running[a] = true
		added = append(added, a)
		newDelta = append(newDelta, a)
	}

	if _, err := ResolveEnabledAreas(profile, newDelta); err != nil {
		return nil, nil, fmt.Errorf("%w. Known areas: %s", err, strings.Join(knownAreaNames(), ", "))
	}
	return newDelta, added, nil
}

// knownAreaNames lists the catalog, so a refused name is answered with the names that
// would have been accepted rather than with a pointer to somewhere else. Read through
// the "full" profile, which is contractually exhaustive (it gains an area whenever the
// catalog does), so this is the catalog without a second list to keep in step with it.
func knownAreaNames() []string {
	all, err := functionalarea.ResolveEnabled(string(functionalarea.ProfileFull), nil)
	if err != nil {
		return nil
	}
	names := make([]string, len(all))
	for i, a := range all {
		names[i] = string(a)
	}
	return names
}

// applyAreaChange settles an enable-area request against the instance's declaration:
// it appends the added areas to st.EnableAreas (the delta the rest of the upgrade
// resolves and records) and records what changed on st.AreaChange.
//
// It also refuses the one mismatch an enable-area cannot carry: an instance whose
// images are not this dcctl's release. The run renders THIS dcctl's chart and applies
// THIS dcctl's infrastructure configuration, so over an instance on another release it
// would quietly move the chart and the infrastructure while leaving the services'
// images where they were — a version change nobody asked for, under a command that
// promises not to make one. Moving versions is `dcctl upgrade`, and the refusal says
// so. A dcctl built from source carries no release version and is let through: it is
// what a developer enables an area with on an instance running a release's images, or
// images they pushed under a tag of their own. (An instance built with --build runs the
// unpublished tag "dev", which the image resolution has already refused — see
// applyUpgradeDeclaration.)
func applyAreaChange(st *State, inst *dcv1beta1.Instance, requested []string, provider string) error {
	newDelta, added, err := planAreaChange(st.Profile, st.EnableAreas, requested)
	if err != nil {
		return fmt.Errorf("enabling %s on instance %q: %w", strings.Join(requested, ", "), st.Instance, err)
	}

	// 🔴 AN EMPTY STAMP IS NOT A SOURCE BUILD. A source build carries "dev"; "" is what a
	// broken ldflags stamp produces, and reading it as "no release, let it through" would
	// turn a build defect into a skipped refusal.
	v := DefaultImageVersion
	if v == "" {
		return fmt.Errorf("this dcctl carries no image version at all, so whether it belongs to "+
			"the release instance %q runs (%q) cannot be judged; enable the area with a released "+
			"dcctl, or one built from source", st.Instance, st.ImageVersion)
	}
	if !IsUnpublishedImageVersion(v) && st.ImageVersion != v {
		return fmt.Errorf(
			"instance %q runs release %q and this dcctl is release %q. Enabling an area applies "+
				"this dcctl's chart and infrastructure configuration, so here it would also move the "+
				"instance onto %s without moving its services' images. Move the instance first "+
				"(`dcctl upgrade %s %s --version %s`), or enable the area with the dcctl of release %s",
			st.Instance, st.ImageVersion, v, v, provider, st.Instance, v, st.ImageVersion)
	}

	st.EnableAreas = newDelta
	st.AreaChange = &AreaChange{
		Requested: requested,
		Added:     added,
		// 🔴 "ALREADY DECLARED" IS NOT "ALREADY RUNNING". The declaration is written
		// BEFORE anything moves (recordUpgradedVersion), so an enable-area that died
		// half-way leaves the area declared over an instance that never got it. Only a
		// declaration whose last run ended Ready is evidence the area is live; anything
		// else re-applies, which is the resumable direction this verb shares with
		// upgrade. Ready also rules out an unfinished first bootstrap: that record never
		// stands over a Ready phase (bootstrapUnfinished).
		Settled: len(added) == 0 &&
			inst.Annotations[dcv1beta1.AnnotationPhase] == dcv1beta1.PhaseReady,
	}
	return nil
}

// precheckAreaChange refuses, before anything is written, an added area the instance's
// object store cannot be mounted by.
//
// 🔴 THE DECLARATION IS WRITTEN BEFORE THE HELM STEP, SO A CHART REFUSAL FOUND THERE IS
// PERMANENT. update-management mounts the filesystem object store beside user-management;
// on a cluster dcctl was only pointed at, a ReadWriteOnce store that one pod mounted at
// bootstrap is two pods' after the change, and the chart refuses that. Met at the Helm
// step, the refusal arrives after recordUpgradedVersion has recorded the area — the run
// ends Failed, every later `dcctl upgrade` renders the same area set and is refused the
// same way, and there is no command that takes the area back out. Here, nothing has been
// written. It is the check bootstrap makes for the same reason (checkObjectStoreMountable),
// asked of the area set this run is about to record.
func precheckAreaChange(ctx context.Context, st *State) error {
	if st.AreaChange == nil || len(st.AreaChange.Added) == 0 {
		return nil
	}
	if err := checkObjectStoreMountable(ctx, st); err != nil {
		return fmt.Errorf("enabling %s on instance %q: %w. Nothing has been changed",
			strings.Join(st.AreaChange.Added, ", "), st.Instance, err)
	}
	return nil
}

// precheckUpgradeMount is precheckAreaChange for a plain upgrade: the same refusal, asked of
// the area set the new chart will render over the declaration, before the version is recorded.
func precheckUpgradeMount(ctx context.Context, st *State) error {
	if err := checkObjectStoreMountable(ctx, st); err != nil {
		return fmt.Errorf("upgrading instance %q: %w. Nothing has been changed", st.Instance, err)
	}
	return nil
}

// sayAreaChange is the opening line of an enable-area run: what was asked, and what it
// means here.
func sayAreaChange(st *State) {
	c := st.AreaChange
	defer sayAreaChangeNotes(st)
	if len(c.Added) > 0 {
		fmt.Printf("  %s %s\n", color.WhiteString("Adding:"), color.GreenString(strings.Join(c.Added, ", ")))
	}
	if already := alreadyPresent(c); len(already) > 0 {
		fmt.Printf("  %s %s\n", color.WhiteString("Already enabled:"), strings.Join(already, ", "))
	}
}

// alreadyPresent is the requested areas the instance already runs.
func alreadyPresent(c *AreaChange) []string {
	var out []string
	for _, a := range c.Requested {
		if !slices.Contains(c.Added, a) {
			out = append(out, a)
		}
	}
	return out
}

// sayAreaChangeSettled is the whole of a run with nothing to do.
func sayAreaChangeSettled(c *AreaChange, instance string) {
	fmt.Println(color.HiGreenString("\nNothing to do: %s already enabled on instance %q, "+
		"and its last run ended Ready. Nothing was changed.", strings.Join(c.Requested, ", "), instance))
}

// sayAreaChangeDone closes a finished enable-area run with what changed.
func sayAreaChangeDone(st *State) {
	c := st.AreaChange
	if len(c.Added) > 0 {
		fmt.Printf("  %s %s\n", color.WhiteString("Enabled:"), color.GreenString(strings.Join(c.Added, ", ")))
	} else {
		fmt.Printf("  %s %s\n", color.WhiteString("Re-applied:"),
			color.GreenString(strings.Join(c.Requested, ", ")+" (already declared; the previous run had not ended Ready)"))
	}
	if len(st.EnabledAreas) > 0 {
		fmt.Printf("  %s %s\n", color.WhiteString("Areas now deployed:"), strings.Join(st.EnabledAreas, ", "))
	}
}

// CheckAreaRequest is the part of an enable-area request that can be judged from the
// command line alone: at least one name, no blanks, and every name in the catalog. The
// command layer calls it before anything touches a cluster, so a typo is answered in
// the first second with the names that would have been accepted. Hard dependencies are
// judged later, against the instance's own area set (planAreaChange), because whether
// one is met depends on what the instance already runs.
func CheckAreaRequest(areas []string) ([]string, error) {
	requested, err := normalizeAreaRequest(areas)
	if err != nil {
		return nil, err
	}
	for _, a := range requested {
		if !functionalarea.Known(functionalarea.FunctionalArea(a)) {
			return nil, fmt.Errorf("unknown functional area %q. Known areas: %s",
				a, strings.Join(knownAreaNames(), ", "))
		}
	}
	return requested, nil
}

// areaChangeNotes are what an operator should know about an added area that the run
// cannot fix for them. Said, not refused: the area starts and serves what it can, and
// refuses the rest loudly itself.
//
// update-management keeps artifact content in the instance's object store, and the
// store is fixed at bootstrap — nothing adds one to a running instance. Without one the
// service runs and refuses every artifact, which reads like a broken feature unless the
// run that enabled it said why.
func areaChangeNotes(st *State) []string {
	if st.AreaChange == nil {
		return nil
	}
	var notes []string
	if slices.Contains(st.AreaChange.Added, string(functionalarea.Lwm2mIngest)) {
		notes = append(notes, "lwm2m-ingest authenticates devices against the pre-shared keys given at "+
			"bootstrap (--lwm2m-identities), and none can be given here: the adapter will run and refuse "+
			"every handshake.")
	}
	if slices.Contains(st.AreaChange.Added, string(functionalarea.UpdateMgmt)) && st.ObjectStore == nil {
		notes = append(notes, "update-management keeps artifact content in the instance's object store, "+
			"and this instance was built without one: the service will run and refuse every artifact. "+
			"An object store cannot be added to a running instance; it is chosen at bootstrap "+
			"(--object-store).")
	}
	return notes
}

// sayAreaChangeNotes prints areaChangeNotes, in yellow.
func sayAreaChangeNotes(st *State) {
	for _, n := range areaChangeNotes(st) {
		fmt.Println(color.YellowString("  note: %s", n))
	}
}
