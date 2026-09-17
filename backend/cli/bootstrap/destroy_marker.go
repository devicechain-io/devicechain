// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// The LOCAL half of "a destroy started here and did not finish".
//
// 🔴 WHY A SECOND RECORD WHEN THE DECLARATION ALREADY CARRIES THE PHASE. The phase
// annotation is written INTO THE CLUSTER, so it can only be written when the cluster
// answers and it can only be read by a reader that reaches it. Both halves fail in the
// same situation: beginDestroy's first two steps are "reach the cluster" and "take the
// lock", either of which can fail, and a destroy carries on regardless — by design, an
// operator who has decided to tear an instance down is not blocked because the cluster
// could not be asked politely first. Every destroy that proceeds past an unreachable
// cluster therefore writes NO phase at all, and the only evidence that a teardown ever
// started is on this machine.
//
// 🔴 AND IT IS A MARKER, NOT A STATE MACHINE. Its CONTENTS are for a human reading the
// file; every decision anything makes on it is made on its EXISTENCE, by stat. A
// teardown that started is a teardown that started whether or not the JSON in here
// parses, so a corrupt marker must never read as an absent one — see DestroyInProgress.
//
// 🔑 IT IS DELIBERATELY WRITTEN BEFORE THE FIRST DELETION IS ATTEMPTED, NOT AFTER THE
// FIRST ONE SUCCEEDS, so a destroy that removed NOTHING can leave one behind — the
// foreign-release contradiction refusal is the reachable case, and it keeps the local
// record for the same reason. That asymmetry is the point. Marking late leaves no
// evidence at all for a run killed between the first delete and the record of it, which
// is the whole defect; marking early costs a later `dcctl bootstrap` a refusal naming the
// exact file to look at, and `dcctl destroy` — which the refusal tells the operator to
// run, and which clears the marker when it finishes — is a correct next step in every
// case that produces one.

// destroyMarkerFile is the marker's name inside ~/.devicechain/instances/<instance>/.
//
// 🔴 IT MUST NOT MATCH looksLikeEscrow, for the reason instanceRecordFile must not: a
// full `dcctl destroy` removes the instance directory but SPARES every name containing
// ".escrow", "rootkey" or "root-key", so a marker matching that pattern would outlive the
// destroy that wrote it — and every later bootstrap and upgrade of that name would be
// refused over a teardown that finished. The name is permanently unusable and nothing in
// the message would say why. TestTheDestroyMarkerIsNotSparedAsEscrow pins it.
const destroyMarkerFile = "destroying.json"

// destroyMarker is what is written inside. Identifiers and a timestamp only, under the
// same closed rule instances.go states for the record beside it: this directory's
// neighbours hold the database superuser password and the broker's TLS private key in
// cleartext, and anything added here is one display field away from a terminal.
type destroyMarker struct {
	Instance  string    `json:"instance"`
	StartedAt time.Time `json:"startedAt"`
}

// writeDestroyMarker records that a teardown of this instance has begun.
//
// A package-level var so the teardown rig can watch WHEN it is called relative to the
// deletions — the ordering is the whole value of the marker, and a plain function call
// leaves that ordering the one part of it nothing can observe. The rig calls through to
// the real implementation, so it is still this code that runs.
var writeDestroyMarker = func(instance string) error {
	dir, err := instanceRoot(instance)
	if err != nil {
		return err
	}
	// 🔴 CREATED IF IT IS NOT THERE, which is not tidiness. A destroy runs against
	// whatever an operator names, including an instance whose local state was never
	// written or was removed by hand — and that is precisely the destroy most likely to
	// die half-way, because nothing local can tell it where to resume. The directory it
	// creates is collapsed again by removeStatePreservingEscrow when the destroy
	// finishes, the same walk that removes the marker itself.
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		return err
	}
	b, err := json.MarshalIndent(destroyMarker{
		Instance:  instance,
		StartedAt: time.Now().UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}
	return writeRecordFile(dir, destroyMarkerFile, append(b, '\n'))
}

// destroyMarkerPath is the marker's path for an instance. It creates nothing, so it is
// safe to call for an instance that may not exist.
func destroyMarkerPath(instance string) (string, error) {
	root, err := instanceRoot(instance)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, destroyMarkerFile), nil
}

// DestroyInProgress reports whether a teardown of this instance started on this machine
// and has not finished.
//
// 🔴 THE ANSWER IS A STAT, NOT A PARSE, AND THE THIRD RETURN IS NOT A COURTESY. Three
// outcomes, not two: present, absent, and COULD NOT TELL. A stat that fails for any
// reason other than "not there" — a directory mode an operator tightened, a home
// directory that will not resolve — says nothing about whether a teardown started, and
// every caller here treats it as a refusal or as a visibly unknown cell rather than as
// "absent". Folding it into `false` is the shape this whole change exists to remove: a
// failure that reads as health.
//
// Contents are deliberately not consulted. A teardown that started is a teardown that
// started whether or not the JSON parses, and a marker an operator half-edited must not
// become an absent one.
func DestroyInProgress(instance string) (bool, error) {
	path, err := destroyMarkerPath(instance)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ErrDestroyUnfinished is the refusal of a verb that has been asked to act on an instance
// whose teardown started and did not finish.
//
// Typed rather than a message, because two commands raise it and a third reads it: what
// distinguishes it from every other refusal is that the instance is neither built nor
// gone, and a caller that has to match on wording to know that is a caller that stops
// matching the day the wording improves.
type ErrDestroyUnfinished struct {
	Instance string
	Marker   string
}

func (e *ErrDestroyUnfinished) Error() string {
	return fmt.Sprintf("instance %q has a teardown that started on this machine and did not finish, "+
		"so the cluster holds some part of it and no longer holds the rest — building on that "+
		"produces a half-old, half-new instance whose failures are attributed to the new run. "+
		"Finish the teardown with `dcctl destroy %s`, which is resumable, and `dcctl bootstrap` "+
		"builds the instance again afterwards. If you are certain nothing of this instance "+
		"remains anywhere, the marker is %s",
		e.Instance, e.Instance, e.Marker)
}

// RefuseUnfinishedDestroy is the refusal itself, for the verbs that must not act on a
// half-destroyed instance: `dcctl bootstrap` before it writes anything, and `dcctl
// upgrade` before it touches the declaration.
//
// 🔴 IT REFUSES RATHER THAN CLEARING, AND NOTHING HERE MAY BE TEMPTED TO CLEAR IT. The
// marker is removed by the destroy that finishes — removeInstanceState and the foreign-
// release path both reach removeStatePreservingEscrow, whose default branch removes
// ordinary files, and TestAFinishedDestroyRemovesTheMarker asserts that end to end. A
// verb that cleared the marker itself would be deciding, from a machine that may never
// have reached the cluster, that a teardown it did not run had finished.
//
// 🔴 AND "COULD NOT TELL" REFUSES TOO. The cost of refusing wrongly is an operator who
// reads the path in the message and looks at it; the cost of proceeding wrongly is a
// bootstrap over a half-removed instance, or an upgrade stamping Ready over Destroying.
func RefuseUnfinishedDestroy(instance string) error {
	marked, err := DestroyInProgress(instance)
	if err != nil {
		path, pathErr := destroyMarkerPath(instance)
		if pathErr != nil {
			path = "~/.devicechain/instances/" + instance + "/" + destroyMarkerFile
		}
		return fmt.Errorf("could not tell whether a teardown of instance %q is part-way through "+
			"(%s could not be read: %w). Refusing rather than guessing: acting on a half-destroyed "+
			"instance is what this check exists to stop", instance, path, err)
	}
	if !marked {
		return nil
	}
	path, err := destroyMarkerPath(instance)
	if err != nil {
		path = "~/.devicechain/instances/" + instance + "/" + destroyMarkerFile
	}
	return &ErrDestroyUnfinished{Instance: instance, Marker: path}
}

// refuseUnfinishedTeardown is the same refusal asked of BOTH witnesses, for a verb that
// already holds the declaration.
//
// 🔴 EITHER ONE CAN BE THE ONLY EVIDENCE THERE IS, so a check wired to one of them is
// silent in the other's case. The local marker is written by every destroy, including one
// that never reached the cluster to write a phase; the declaration's phase is the only
// half visible to an operator on a DIFFERENT machine from the one that ran the destroy.
func refuseUnfinishedTeardown(instance string, inst *dcv1beta1.Instance) error {
	if err := RefuseUnfinishedDestroy(instance); err != nil {
		return err
	}
	if inst == nil || inst.Annotations[dcv1beta1.AnnotationPhase] != dcv1beta1.PhaseDestroying {
		return nil
	}
	return fmt.Errorf("instance %q has a declaration reading %s, so a destroy started against it "+
		"and did not finish; the cluster holds some part of it and no longer holds the rest. "+
		"Finish the teardown with `dcctl destroy %s`, which is resumable, and build it again with "+
		"`dcctl bootstrap` afterwards — or, if you are certain nothing of it remains, drop the "+
		"declaration with `dcctl instances release %s`, which destroys nothing",
		instance, dcv1beta1.PhaseDestroying, instance, instance)
}
