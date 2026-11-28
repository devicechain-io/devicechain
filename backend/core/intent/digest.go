// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/ledger"
)

// CompilerBuild identifies the semantics of this package: the intended-value function, the diff
// and its precedence, and every digest format. It is stored with each frozen version and each
// plan, and it is an input of InputsDigest, so changing it re-plans every intent once. Change it
// whenever any of those semantics change; TestPinnedVectors fails until it is changed in step
// with the vectors.
const CompilerBuild = "core-intent/2"

// inputsDigestVersion versions the InputsDigest envelope itself.
const inputsDigestVersion = 1

// InputsDigestInput is everything a plan is a function of, and nothing else. Owner standing and
// dependency health are status inputs, not plan inputs, and are deliberately absent: an
// unchanged digest skips the walk, never the standing check.
type InputsDigestInput struct {
	V                 int64  `json:"v"`
	SpecDigest        string `json:"specDigest"`
	ObservationDigest string `json:"observationDigest"`
	InstantBucket     int64  `json:"instantBucket"`
	CompilerBuild     string `json:"compilerBuild"`
}

// InputsDigest is ledger.Digest of the input envelope. When it equals the previous evaluation's,
// the plan would be the same, so no plan row is written.
func InputsDigest(specDigest, observationDigest string, instantBucket int64, compilerBuild string) (string, error) {
	if !validDigest(specDigest) {
		return "", fmt.Errorf("inputs digest: spec digest %q is malformed", specDigest)
	}
	if !validDigest(observationDigest) {
		return "", fmt.Errorf("inputs digest: observation digest %q is malformed", observationDigest)
	}
	if compilerBuild == "" {
		return "", fmt.Errorf("inputs digest: compiler build is required")
	}
	return ledger.Digest(InputsDigestInput{
		V:                 inputsDigestVersion,
		SpecDigest:        specDigest,
		ObservationDigest: observationDigest,
		InstantBucket:     instantBucket,
		CompilerBuild:     compilerBuild,
	})
}
