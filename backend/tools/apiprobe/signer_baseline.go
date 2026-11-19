// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// signedAssignmentRows are the rows that must ALL be written for the artifact the seed assigns
// to be signed: the signer, and its signature over that artifact.
var signedAssignmentRows = []string{"artifact-signer", "artifact-available-signature"}

// runSignerBaseline answers, for a rig, whether a seed against the baseline tree signs the
// artifact it assigns: it prints `supported` or `unsupported: <reason>` and exits 0 for either
// (a non-zero exit is a setup failure, never an answer). Only for `unsupported` does
// update-assignment need the instance to allow unverified artifacts, so the rig asks before it
// makes that edit, and an instance at the shipped default is what every newer baseline is
// seeded on.
func runSignerBaseline(argv []string) error { return runSignerBaselineTo(argv, os.Stdout) }

// runSignerBaselineTo is runSignerBaseline writing its answer to w, so the answer the rig
// parses is tested as printed.
func runSignerBaselineTo(argv []string, w io.Writer) error {
	fs := flagSetFor("signerbaseline")
	var dir string
	fs.StringVar(&dir, "baseline-schemas", "", "a `backend/services` tree from the release being seeded (required)")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(dir) == "" {
		return failWith(exitSetup, "--baseline-schemas is required")
	}
	base, err := loadBaseline(dir)
	if err != nil {
		return err
	}
	ok, why, err := signsAssignedArtifact(base)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintf(w, "unsupported: %s\n", why)
		return nil
	}
	fmt.Fprintln(w, "supported")
	return nil
}

// signsAssignedArtifact reports whether a seed against base writes every row that signs the
// assigned artifact, and if not, why. It asks plan, the same decision the seed makes, so the
// answer cannot disagree with what the seed then does.
func signsAssignedArtifact(base *baseline) (bool, string, error) {
	for _, name := range signedAssignmentRows {
		e, found := tableEntityNamed(name)
		if !found {
			return false, "", failWith(exitSetup, "the table has no %q row; signerbaseline cannot answer", name)
		}
		write, why, err := plan(e, base)
		if err != nil {
			return false, "", err
		}
		if !write {
			return false, name + " is skipped: " + why, nil
		}
	}
	return true, "", nil
}

// tableEntityNamed finds a row anywhere in the seed's table, publishes and the after-publish
// tail included (entityNamed searches only the creates).
func tableEntityNamed(name string) (entity, bool) {
	for _, e := range allEntities() {
		if e.Name == name {
			return e, true
		}
	}
	return entity{}, false
}
