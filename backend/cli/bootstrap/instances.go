// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/devicechain-io/dcctl/dcdir"
)

// The instance record: which cluster a DeviceChain instance was bootstrapped into.
//
// 🔴 WHY THIS FILE EXISTS. Before it, the instance→cluster binding was ASSUMED at both
// ends and written down nowhere: `localProvider.EnsureCluster` targeted `kind-<instance>`
// and destroy re-derived the same name to delete. The assumption is false the
// moment an operator points dcctl at a cluster BY NAME, which is exactly what both
// validation rigs do — `ha-rig.sh` bootstraps instance `harig` into cluster
// `devicechain-ha`, `upgrade-rig.sh` bootstraps `upgrig` into `devicechain-upgrade`.
//
// The failure that produced was not an error. It was a SUCCESS MESSAGE over a no-op:
// `dcctl destroy local harig` ran `kind delete cluster --name harig`, and because kind's
// delete is IDEMPOTENT a cluster that does not exist exits 0 silently — so the state was
// removed, `Instance "harig" destroyed.` was printed, and all four `devicechain-ha` node
// containers kept running. Measured 2026-09-01, with four such clusters and nine orphaned
// state directories accumulated on one machine and no command able to report any of it.
//
// So the binding is recorded at the one moment both halves are in hand, and destroy reads
// it instead of guessing.
//
// 🔴 SECURITY: IDENTIFIERS ONLY, AND THE LIST IS CLOSED. This file sits in
// ~/.devicechain/instances/<instance>/, beside OpenTofu state that holds the database superuser
// password and the broker's TLS PRIVATE KEY in cleartext (see instanceStateDir's comment
// in tofu.go) — so the directory's threat model is already the highest there is, and these
// identifiers are strictly less sensitive than their neighbours. What is NEW is that
// `dcctl instances list` PRINTS this file, and printed output gets pasted into issues and
// screen-shares. Nothing that is not a name, a flag or a timestamp may be added here, and
// the listing must read this file and nothing else — a display field sourced from the
// tfstate would be one path away from printing a private key to a terminal.

// instanceRecordFile is the record's name inside ~/.devicechain/instances/<instance>/.
//
// 🔴 IT MUST NOT MATCH looksLikeEscrow. A full `dcctl destroy` removes the instance
// directory but SPARES every name containing ".escrow", "rootkey" or "root-key". A record
// spared that way would outlive its instance, and a same-name rebuild would then find a
// binding pointing at a cluster that no longer exists — or, worse, at a DIFFERENT cluster
// somebody has since created under that name, which destroy would then delete on its
// owner's behalf. That is the generational-inheritance trap broker_record.go documents for
// credentials, applied to cluster identity. TestInstanceRecordIsNotSparedAsEscrow pins it.
const instanceRecordFile = "instance.json"

// ErrNoInstanceRecord is returned when an instance directory carries no record. It is a
// first-class STATE, not a failure: every instance bootstrapped before this existed is in
// it, including live ones, and callers are expected to degrade loudly rather than refuse.
var ErrNoInstanceRecord = errors.New("no instance record")

// ClusterBinding is what EnsureCluster resolved — which cluster an instance lives in,
// how to reach it, and whether it is dcctl's to delete.
type ClusterBinding struct {
	// Cluster is the provider's own name for the cluster (for local, the kind cluster
	// name). It may be EMPTY for an adopted context that does not follow a naming
	// convention dcctl can read — an operator's `--kube-context prod-eu-west` says
	// nothing about the cluster's name. Empty is honest; a guess would not be.
	Cluster string
	// KubeContext is the context to target. Always populated.
	KubeContext string
	// Managed reports whether this is dcctl's OWN cluster. It is a recorded fact about the
	// binding and nothing more: destroy never deletes a cluster, managed or not.
	//
	// 🔴 IT IS NOT "dcctl created it". EnsureCluster REUSES an existing `kind-<cluster>`
	// cluster when it finds one (`kind-devicechain` unless --cluster names another), and
	// a kind cluster created from deploy/local/kind-cluster.yaml is exactly that cluster,
	// and it exists before dcctl runs. So Managed means "this is the kind-<cluster>
	// cluster dcctl names by convention", created or reused, and it is false only when
	// the operator pointed dcctl somewhere BY NAME with --kube-context. That is the rig
	// case.
	Managed bool
	// ClusterUID is the cluster's identity — the kube-system namespace UID, read by
	// ClusterUID(). EMPTY where it was never read: a guessed binding knows nothing, and
	// an instance bootstrapped before this field existed recorded nothing.
	//
	// 🔴 EMPTY AND DIFFERENT ARE NOT THE SAME ANSWER, and a consumer that collapses them
	// repeats the defect this whole type was introduced to fix. "No UID recorded" is the
	// pre-record state, where the honest move is to degrade loudly; "recorded, and it
	// does not match the cluster in front of us" is knowledge that this is a DIFFERENT
	// cluster wearing the same name. The first is BindingGuessed's shape, the second is
	// BindingUnreadable's, and they were collapsed once before.
	ClusterUID string
}

// InstanceRecord is what is persisted. Every field is an identifier, a flag or a
// timestamp; see the security note above before adding one.
type InstanceRecord struct {
	Instance    string `json:"instance"`
	Provider    string `json:"provider"`
	Cluster     string `json:"cluster,omitempty"`
	KubeContext string `json:"kubeContext"`
	Managed     bool   `json:"managed"`
	// ClusterUID is the cluster's identity; see ClusterBinding.ClusterUID. It clears the
	// closed list above because a namespace UID IS an identifier — assigned by the API
	// server, readable by anyone who can read the cluster, and a credential in no sense.
	// Being printed with the rest of the record is the test that list is really about.
	// Omitted when empty, so an instance from before this existed reads as silent rather
	// than as a cluster whose identity is the empty string.
	ClusterUID   string    `json:"clusterUid,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	DcctlVersion string    `json:"dcctlVersion,omitempty"`
}

// Binding returns the record's cluster half.
func (r InstanceRecord) Binding() ClusterBinding {
	return ClusterBinding{
		Cluster:     r.Cluster,
		KubeContext: r.KubeContext,
		Managed:     r.Managed,
		ClusterUID:  r.ClusterUID,
	}
}

// maxInstanceNameLen is how long an instance name may be before its Helm release name
// stops being one.
//
// 🔴 DERIVED, NOT CHOSEN. Helm caps a release name at 53 characters
// (chartutil.ValidateReleaseName), and this instance's release is helmReleaseNameFor(name)
// — the three-character prefix plus the name. Before release names carried the instance
// the cap did not apply to the name at all, so a long one worked; now it fails inside the
// Helm step, after the declaration, the infrastructure and the credentials have all been
// written. A number this far from the code that enforces it drifts silently, so
// TestTheInstanceNameCeilingIsTheOneHelmActuallyEnforces pins both ends against the real
// validator rather than against this constant.
const maxInstanceNameLen = 50

// ValidateInstanceName rejects names that cannot safely become a directory under
// ~/.devicechain/instances.
//
// 🔴 THERE WAS NO VALIDATION ANYWHERE, and what that costs is now smaller than it was
// but not zero. A name containing a separator or ".." escapes the directory altogether,
// and an empty one resolves to the instances directory itself, which a destroy would
// then try to remove wholesale — taking every other instance with it.
//
// 🔑 THE DIRECTORY COLLISION IS GONE, AND THE REASON IS STRUCTURAL RATHER THAN
// ENFORCED HERE. While instances sat directly under the root, a name like "escrow"
// aimed an instance at a sibling dcctl owns, so this function had to reserve each
// sibling by hand and ListInstances had to skip the same names — two lists that
// disagreed. Nesting under instances/ put instance names in their own namespace, so
// there is no DIRECTORY left to reserve. Do not add a directory reservation back
// here; add it to dcdir's inventory instead, where it is a sibling of instances/
// and cannot collide with anything in it.
//
// 🔴 THAT SAID "THERE IS NOTHING LEFT TO RESERVE", AND THAT WAS NEVER TRUE OF EVERY
// GRAMMAR THE NAME LIVES IN. A name still collides in two more of them, and a reader
// who takes the sentence above as a general statement will reinvent the defect it
// describes by folding them in here. One of them IS a list: reservedInstanceNames
// below, the labels POSTGRESQL owns, which can be a list because the store's
// vocabulary is fixed and knowable without asking the store. The other is not, and
// that is the point — whether the NAMESPACE this name becomes is free is settled
// against the cluster by precheckInstanceNamespace, because "it exists and is not
// this instance's" is a question only the cluster can answer. A hand-copied list of
// the namespaces `dcctl install` creates used to sit alongside it and has been
// removed: an instance's namespace is being given a prefix of its own, which puts the
// two sets of names out of each other's reach and retires that question rather than
// answering it twice. Each
// grammar answered where its answer lives is correct; one list answering all of them
// is the shape that disagrees with itself.
//
// 🔴 AND IT IS A DNS-1123 LABEL, BECAUSE A NAME IS NOW FOUR THINGS AT ONCE. It names a
// directory here, a Kubernetes namespace, and — on the shared relational store — both
// the instance's login and its database. A label is the narrowest of the four grammars
// and the only one that fits all of them: lowercase, so PostgreSQL's case-folding cannot
// turn one instance's identifiers into another's, and no underscore, which is what keeps
// the store's own roles (`dc_owner`, `dc_provisioner`) out of reach of any instance name.
//
// reservedInstanceNames are the labels PostgreSQL already means something by.
func ValidateInstanceName(instance string) error {
	switch {
	case instance == "":
		return fmt.Errorf("instance name is empty")
	case len(instance) > maxInstanceNameLen:
		return fmt.Errorf(
			"instance name %q is %d characters; the most that fits is %d, because this "+
				"instance's Helm release is named after it and Helm caps a release name at 53. "+
				"Refusing here rather than in the Helm step, which runs after the declaration, "+
				"the infrastructure and every credential have already been written",
			instance, len(instance), maxInstanceNameLen)
	}
	for _, r := range instance {
		if !(r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("instance name %q contains %q; use lowercase letters, digits and '-' "+
				"(the name also becomes a Kubernetes namespace and the instance's database login)",
				instance, r)
		}
	}
	if instance[0] == '-' || instance[len(instance)-1] == '-' {
		return fmt.Errorf("instance name %q must start and end with a lowercase letter or digit", instance)
	}
	if reservedInstanceNames[instance] {
		return fmt.Errorf("instance name %q is reserved: the relational store already has a database "+
			"or role by that name, and every instance gets a login and a database named after it", instance)
	}
	return nil
}

// reservedInstanceNames are the DNS-1123 labels that name something PostgreSQL itself
// owns — a built-in database, or a role name it refuses to create.
var reservedInstanceNames = map[string]bool{
	"postgres":  true,
	"template0": true,
	"template1": true,
	"public":    true,
	"none":      true,
}

// instanceRecordPath is the record's path for an instance. It does NOT create anything,
// so it is safe to call for an instance that may not exist.
func instanceRecordPath(instance string) (string, error) {
	root, err := instanceRoot(instance)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, instanceRecordFile), nil
}

// WriteInstanceRecord persists the binding for an instance, replacing any previous one.
//
// Rewriting rather than merging is the point: a re-bootstrap into a different cluster must
// CORRECT the record, and a record that only ever accumulated would preserve the very
// staleness this exists to remove.
func WriteInstanceRecord(rec InstanceRecord) error {
	// Validating here is defence in depth behind cmd/bootstrap.go's own check. What it
	// still buys is narrower than it was: a name can no longer aim a record at a sibling
	// dcctl owns, because instances/ is a level below them — but a name carrying a
	// separator still escapes the tree, and an empty one still resolves to the instances
	// directory itself, which is every instance rather than none.
	if err := ValidateInstanceName(rec.Instance); err != nil {
		return err
	}
	// instanceStateDir does the chmod walk back down every level, which matters for a
	// tree an older dcctl created at 0755 — MkdirAll leaves an EXISTING directory exactly
	// as it found it, so relying on the mode constant alone would protect only fresh
	// installs. "" asks for the instance root itself rather than a subdirectory.
	dir, err := instanceStateDir(rec.Instance, "")
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeRecordFile(dir, instanceRecordFile, append(b, '\n'))
}

// writeRecordFile puts the record's bytes in place.
//
// 🔴 WRITTEN ATOMICALLY, AND THE REASON IS NOT CONCURRENCY. Two dcctl runs against one
// instance are already out of scope. The risk is a TORN write: os.WriteFile truncates
// first, so a Ctrl-C, an ENOSPC or an OOM kill between the truncate and the write
// leaves half a JSON document. A half-record does not fail safe — ReadInstanceRecord
// rejects it, and a rejected record is one the caller must then treat as unknown,
// which lands back on the guess that deletes `kind-<instance>`. So the failure mode
// of a partial write is exactly the defect this file exists to prevent, reached by a
// power cut. writeBrokerRecord in this same directory already does it this way.
//
// Split out because PriorLocalState.Restore puts a record BACK, and a rollback written
// the non-atomic way would reintroduce the torn-write failure on the one path whose
// whole job is leaving the disk in a state somebody can trust. The cluster record in
// cluster_identity.go is written through it too —
// every local record dcctl keeps is one a half-write turns into a refusal, and a refusal
// is what sends a caller back to guessing.
func writeRecordFile(dir, name string, contents []byte) error {
	path := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, name+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename has succeeded
	// Chmod before any content is written: CreateTemp makes the file 0600 already, but
	// saying so here means the guarantee does not depend on that staying true.
	if err := tmp.Chmod(stateFileMode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(contents); err != nil {
		tmp.Close()
		return err
	}
	// Sync before rename: a rename is atomic with respect to readers, not with respect to
	// a crash that loses the not-yet-flushed contents behind it.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// PriorLocalState is what ~/.devicechain/instances/<instance>/ held before a run wrote its record
// into it.
//
// 🔴 IT EXISTS FOR THE REFUSALS THAT FIRE BEFORE THIS RUN HAS WRITTEN ANYTHING, AND THE
// REASON IS AN ORDERING NOBODY CAN CHANGE. `dcctl bootstrap` records the instance→cluster
// binding BEFORE the pipeline starts, and deliberately keeps it on every failure: the
// cluster may already be up by then, and a cluster nothing can name is the orphan the
// record exists to prevent. That reasoning holds for every failure except the ones
// stepCheckClusterSingletons raises, and what makes them the exception is WHERE they are
// raised rather than what they are about — TestTheSingletonStepRunsBeforeAnythingIsWritten
// holds that step ahead of the operator install and the declaration. On those the record
// this run wrote describes nothing, and left behind it is a phantom: `dcctl instances
// list` prints an instance that was never built. WHICH errors those are is
// unwindLocalRecordWhenNothingWasWritten's list in cmd and is deliberately not restated
// here; naming one of them as "the" refusal is how that comment went stale once already.
//
// 🔑 THE CLUSTER MAY BE THIS RUN'S OWN, AND THAT COSTS NOTHING. An earlier reading of the
// above had these refusals firing only against a cluster EnsureCluster ADOPTED, on the
// grounds that a cluster it had just created holds nothing to refuse over. The namespace
// refusal is the counter-example: `monitoring` exists on a cluster this very run created
// and installed. Nothing is orphaned by that, because a cluster is filed under its own
// kube-system UID by the cluster record (cluster_identity.go) rather than under the
// instance — so the instance's record is still the only thing that has to go back.
//
// 🔴 IT RESTORES RATHER THAN DELETES, AND THE DIFFERENCE IS A REAL INSTANCE.
// WriteInstanceRecord REPLACES, so a run that names an instance which already exists
// somewhere else — the shape of a mistyped --kube-context — has already overwritten that
// instance's binding by the time the refusal fires. Deleting would take a live
// instance's record away; putting back exactly what was there leaves both instances
// describable. The bytes are kept verbatim, not re-marshalled from a parsed record, so a
// record this build cannot parse survives too.
type PriorLocalState struct {
	instance string
	// dirExisted says whether ~/.devicechain/instances/<instance> was there before the run. When
	// it was not, the whole directory is this run's and goes back with the record.
	dirExisted bool
	// record is the record file's contents, or nil when there was no record file.
	record []byte
	// readable is false when the state could not be captured at all (no home
	// directory). Restore then does NOTHING rather than guess, because every action it
	// could take would be taken on an unknown starting point.
	readable bool
}

// CapturePriorLocalState reads what is on disk for an instance, before a run replaces
// it. Every failure is folded into "not readable": this is a rollback aid, and failing a
// bootstrap because its rollback aid could not be prepared would be the tail wagging the
// dog. Restore's own failures are reported, because by then something HAS been written.
func CapturePriorLocalState(instance string) PriorLocalState {
	prior := PriorLocalState{instance: instance}
	dir, err := instanceRoot(instance)
	if err != nil {
		return prior
	}
	prior.readable = true
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		prior.dirExisted = true
	}
	if b, err := os.ReadFile(filepath.Join(dir, instanceRecordFile)); err == nil {
		prior.record = b
	}
	return prior
}

// Restore puts the instance's local state back the way CapturePriorLocalState found it,
// reporting whether it removed the directory outright so the caller can say so.
//
// It is deliberately narrow: it restores the RECORD and, when this run created the
// directory, removes the directory. It does not attempt to undo anything else, because
// at the point its one caller fires — a refusal three steps into the pipeline, before
// the first operator write — the record is the only thing on disk that this run put
// there.
func (p PriorLocalState) Restore() (removed bool, err error) {
	if !p.readable {
		return false, nil
	}
	dir, err := instanceRoot(p.instance)
	if err != nil {
		return false, err
	}

	if p.record != nil {
		// There was a record before this run. Put it back byte for byte.
		if _, err := os.Stat(dir); err != nil {
			// The directory went away under us. Recreating it to hold a record for an
			// instance whose state is gone would invent the phantom this removes.
			return false, nil
		}
		return false, writeRecordFile(dir, instanceRecordFile, p.record)
	}

	if p.dirExisted {
		// The directory was already there and held no record — an instance from before
		// records existed, or a tree destroy left behind. Take away only what this run
		// added, and leave the directory, which is not ours to remove.
		if err := os.Remove(filepath.Join(dir, instanceRecordFile)); err != nil && !os.IsNotExist(err) {
			return false, err
		}
		return false, nil
	}

	// This run created the directory, so the whole thing goes — through the escrow-
	// sparing walk rather than a RemoveAll. Nothing dcctl writes should have put root-key
	// material under here (resolveEscrowPath refuses to), but the walk is the one place
	// that judgement is already written down, and a rollback is not the place to take a
	// second opinion on it. removeStatePreservingEscrow collapses the directory itself
	// only when nothing was spared, which is exactly the condition for the phantom to go.
	kept, err := removeStatePreservingEscrow(dir)
	if err != nil {
		return false, err
	}
	return len(kept) == 0, nil
}

// ReadInstanceRecord returns the recorded binding, or ErrNoInstanceRecord when the
// instance has none.
func ReadInstanceRecord(instance string) (InstanceRecord, error) {
	path, err := instanceRecordPath(instance)
	if err != nil {
		return InstanceRecord{}, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return InstanceRecord{}, ErrNoInstanceRecord
	}
	if err != nil {
		return InstanceRecord{}, err
	}
	var rec InstanceRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return InstanceRecord{}, fmt.Errorf("reading %s: %w", path, err)
	}
	// A record whose name disagrees with the directory it was found in is not usable:
	// something copied a tree, and acting on it would target another instance's cluster.
	if rec.Instance != "" && rec.Instance != instance {
		return InstanceRecord{}, fmt.Errorf(
			"%s records instance %q but sits in the directory for %q — refusing to act on it",
			path, rec.Instance, instance)
	}
	rec.Instance = instance
	return rec, nil
}

// KnownInstance is one entry in the listing: the instance directory that exists on disk,
// and its record if it has one.
type KnownInstance struct {
	Instance string
	Record   InstanceRecord
	// HasRecord is false for an instance bootstrapped before records existed. The
	// listing must SHOW these rather than hide them: destroy falls back to a guess for
	// them, and the guess is what silently did nothing.
	HasRecord bool
	// Err is a record that exists but could not be read (corrupt, or belonging to
	// another instance). Reported per row rather than failing the whole listing — one
	// unreadable record must not hide every healthy one.
	Err error
	// Destroying reports that a teardown of this instance started on this machine and
	// did not finish — the destroy marker beside the record is still there. See
	// destroy_marker.go.
	Destroying bool
	// DestroyingErr is set when the marker could not be STATTED, which is neither
	// "present" nor "absent". It is carried separately rather than folded into
	// Destroying because the two have opposite consequences for a reader: present means
	// "re-run destroy", could-not-tell means "this row does not know" — and a listing
	// that printed the second as the healthy answer is the defect this change exists to
	// remove.
	DestroyingErr error
}

// ListInstances enumerates every instance directory under ~/.devicechain/instances,
// with its record where it has one and whether a teardown of it is part-way through.
//
// 🔴 IT READS instance.json AND THE DESTROY MARKER, AND NOTHING ELSE. That is a security
// property rather than an optimisation — see the file header — and the marker is STATTED
// rather than read, so no byte of the instance directory beyond the record reaches this
// process. TestListInstancesReadsTheRecordAndTheDestroyMarkerAndNothingElse asserts it
// against a planted, unreadable terraform.tfstate.
//
// 🔑 THE MARKER IS CHECKED HERE RATHER THAN BY THE COMMAND THAT PRINTS IT, and that is
// the whole reason this function grew a field instead of the cmd layer growing an
// os.Stat. The claim above is only as good as the test over it, and that test drives THIS
// function; a second open in the command layer would have been outside it.
func ListInstances() ([]KnownInstance, error) {
	// One directory, in which everything IS an instance. There is no list of names
	// to skip here any more, because there is nothing beside the instances to skip:
	// escrow and sims are siblings of this directory, not of its contents.
	root, err := dcdir.Sibling(dcdir.Instances)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []KnownInstance
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		known := KnownInstance{Instance: e.Name()}
		rec, err := ReadInstanceRecord(e.Name())
		switch {
		case errors.Is(err, ErrNoInstanceRecord):
		case err != nil:
			known.Err = err
		default:
			known.Record = rec
			known.HasRecord = true
		}
		// Reported per row, never returned: one instance whose marker could not be
		// statted must not hide every other instance, for the reason an unreadable
		// record does not.
		known.Destroying, known.DestroyingErr = DestroyInProgress(e.Name())
		out = append(out, known)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instance < out[j].Instance })
	return out, nil
}

// GuessBinding is the pre-record behaviour, kept for instances that have no record and
// named so no caller can use it without noticing what it is.
//
// 🔴 IT IS A GUESS, AND EVERY CALLER MUST SAY SO. This is precisely the derivation whose
// silent failure this package exists to fix: it is right for an instance bootstrapped the
// default way and wrong for every instance bootstrapped with --kube-context, and nothing
// in it can tell the two apart. Callers degrade LOUDLY — they print the cluster name they
// are about to act on and that they are guessing it — so a no-op is visible rather than
// reported as a success.
func GuessBinding(instance string) ClusterBinding {
	return ClusterBinding{Cluster: instance, KubeContext: "kind-" + instance, Managed: true}
}

// describe names a binding for a human: the cluster when we know it, the context when
// that is all we have. Never invents a name — a message that says "cluster foo" about a
// cluster nobody confirmed is called foo is how the original defect read as success.
func (b ClusterBinding) describe() string {
	if b.Cluster != "" {
		return b.Cluster
	}
	return "(context " + b.KubeContext + ")"
}

// Describe is describe for callers outside this package.
func (b ClusterBinding) Describe() string { return b.describe() }
