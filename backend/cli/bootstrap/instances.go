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
	"strings"
	"time"
)

// The instance record: which cluster a DeviceChain instance was bootstrapped into.
//
// 🔴 WHY THIS FILE EXISTS. Before it, the instance→cluster binding was ASSUMED at both
// ends and written down nowhere: `localProvider.EnsureCluster` targeted `kind-<instance>`
// and `DestroyCluster` re-derived the same name to delete. The assumption is false the
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
// ~/.devicechain/<instance>/, beside OpenTofu state that holds the database superuser
// password and the broker's TLS PRIVATE KEY in cleartext (see instanceStateDir's comment
// in tofu.go) — so the directory's threat model is already the highest there is, and these
// identifiers are strictly less sensitive than their neighbours. What is NEW is that
// `dcctl instances list` PRINTS this file, and printed output gets pasted into issues and
// screen-shares. Nothing that is not a name, a flag or a timestamp may be added here, and
// the listing must read this file and nothing else — a display field sourced from the
// tfstate would be one path away from printing a private key to a terminal.

// instanceRecordFile is the record's name inside ~/.devicechain/<instance>/.
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
	// Managed reports whether this is dcctl's OWN cluster, and therefore dcctl's to
	// delete.
	//
	// 🔴 IT IS NOT "dcctl created it", AND THE DIFFERENCE IS DELIBERATE. EnsureCluster
	// REUSES an existing `kind-<instance>` cluster when it finds one, and
	// deploy/local/up.sh:104 creates exactly that cluster itself before bootstrap runs.
	// A literal created-by-dcctl rule would therefore stop `dcctl destroy` deleting the
	// cluster in the primary local flow — a regression in the one path that works
	// correctly today. So Managed means "this is the kind-<instance> cluster dcctl names
	// by convention", created or reused, and it is false only when the operator pointed
	// dcctl somewhere BY NAME with --kube-context. That is the rig case, and the case
	// that was actually broken.
	Managed bool
}

// InstanceRecord is what is persisted. Every field is an identifier, a flag or a
// timestamp; see the security note above before adding one.
type InstanceRecord struct {
	Instance     string    `json:"instance"`
	Provider     string    `json:"provider"`
	Cluster      string    `json:"cluster,omitempty"`
	KubeContext  string    `json:"kubeContext"`
	Managed      bool      `json:"managed"`
	CreatedAt    time.Time `json:"createdAt"`
	DcctlVersion string    `json:"dcctlVersion,omitempty"`
}

// Binding returns the record's cluster half.
func (r InstanceRecord) Binding() ClusterBinding {
	return ClusterBinding{Cluster: r.Cluster, KubeContext: r.KubeContext, Managed: r.Managed}
}

// ValidateInstanceName rejects names that cannot safely become a directory under
// ~/.devicechain.
//
// 🔴 THERE WAS NO VALIDATION ANYWHERE, and the record widened what that costs. An
// instance called "escrow" writes its record INTO the escrow directory — where destroy
// deliberately spares everything, and where ListInstances deliberately does not look — so
// it would be invisible to `instances list`, skipped by `destroy --all`, and its record
// would outlive it to be inherited by the next instance of that name. A name containing a
// separator or ".." escapes the directory altogether, and an empty one resolves to
// ~/.devicechain itself, which a destroy would then try to remove wholesale.
//
// Deliberately strict rather than clever: these are directory names on one machine, and
// nothing is lost by requiring them to look like identifiers.
func ValidateInstanceName(instance string) error {
	switch {
	case instance == "":
		return fmt.Errorf("instance name is empty")
	case instance == escrowDirName:
		return fmt.Errorf(
			"instance name %q collides with the root-key escrow directory under ~/.devicechain; choose another name",
			instance)
	case instance == "." || instance == "..":
		return fmt.Errorf("instance name %q is not a usable directory name", instance)
	case strings.ContainsAny(instance, `/\`) || strings.Contains(instance, ".."):
		return fmt.Errorf("instance name %q may not contain a path separator or \"..\"", instance)
	}
	for _, r := range instance {
		if !(r == '-' || r == '_' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("instance name %q contains %q; use letters, digits, '-', '_' or '.'", instance, r)
		}
	}
	return nil
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
	// instanceStateDir does the chmod walk back down every level, which matters for a
	// tree an older dcctl created at 0755 — MkdirAll leaves an EXISTING directory exactly
	// as it found it, so relying on the mode constant alone would protect only fresh
	// installs. "" asks for the instance root itself rather than a subdirectory.
	// Defence in depth behind cmd/bootstrap.go's check: this is the function that would
	// place a record inside the escrow directory, where destroy spares it and the listing
	// never looks.
	if err := ValidateInstanceName(rec.Instance); err != nil {
		return err
	}
	dir, err := instanceStateDir(rec.Instance, "")
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}

	// 🔴 WRITTEN ATOMICALLY, AND THE REASON IS NOT CONCURRENCY. Two dcctl runs against one
	// instance are already out of scope. The risk is a TORN write: os.WriteFile truncates
	// first, so a Ctrl-C, an ENOSPC or an OOM kill between the truncate and the write
	// leaves half a JSON document. A half-record does not fail safe — ReadInstanceRecord
	// rejects it, and a rejected record is one the caller must then treat as unknown,
	// which lands back on the guess that deletes `kind-<instance>`. So the failure mode
	// of a partial write is exactly the defect this file exists to prevent, reached by a
	// power cut. writeBrokerRecord in this same directory already does it this way.
	path := filepath.Join(dir, instanceRecordFile)
	tmp, err := os.CreateTemp(dir, instanceRecordFile+".*.tmp")
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
	if _, err := tmp.Write(append(b, '\n')); err != nil {
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
}

// ListInstances enumerates every instance directory under ~/.devicechain, with its record
// where it has one.
//
// 🔴 IT READS ONLY instance.json. Nothing else in the instance directory is opened, and
// that is a security property rather than an optimisation — see the file header.
func ListInstances() ([]KnownInstance, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, ".devicechain")
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
		// The escrow directory is a sibling of the instance directories, not one of
		// them. Listing it as an instance would invent one that cannot be destroyed.
		if e.Name() == escrowDirName {
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
