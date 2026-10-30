// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"testing"
)

// The credential types are part of the device wire contract the versioning policy covers: a
// device provisioned with one must keep authenticating after a minor upgrade. This freezes the
// vocabulary shipped as of v0.27.0 (and how each type is presented). Adding a type is fine and
// needs a new row here; a row that no longer holds is a break, and it takes a major release.
var shippedCredentialTypes = []struct {
	name          CredentialType
	presentedById bool
}{
	{"ACCESS_TOKEN", true},
	{"MQTT_BASIC", true},
	{"X509_CERTIFICATE", false},
}

// credentialVocabularyBreaks lists every way the vocabulary fails the frozen rows. It takes the
// predicates as arguments so the negative control can hand it a vocabulary that has lost a type.
func credentialVocabularyBreaks(valid func(CredentialType) bool, byId func(CredentialType) bool) []string {
	var out []string
	for _, c := range shippedCredentialTypes {
		if !valid(c.name) {
			out = append(out, fmt.Sprintf("%s is no longer a valid credential type", c.name))
			continue
		}
		if byId(c.name) != c.presentedById {
			out = append(out, fmt.Sprintf("%s changed how it is presented (by id: %v -> %v)", c.name, c.presentedById, byId(c.name)))
		}
	}
	return out
}

func TestCredentialVocabularyKeepsEveryShippedType(t *testing.T) {
	for _, b := range credentialVocabularyBreaks(CredentialType.Valid, CredentialType.PresentedById) {
		t.Error(b)
	}
}

// The negative control: a vocabulary that dropped a type, and one that changed how a type is
// presented, must each be reported. Without this the test above could pass over nothing.
func TestCredentialVocabularyCheckCanFail(t *testing.T) {
	dropped := func(c CredentialType) bool { return c != "MQTT_BASIC" && c.Valid() }
	if got := credentialVocabularyBreaks(dropped, CredentialType.PresentedById); len(got) != 1 {
		t.Fatalf("a dropped type must be reported once, got %v", got)
	}
	flipped := func(c CredentialType) bool { return c == "X509_CERTIFICATE" || c.PresentedById() }
	if got := credentialVocabularyBreaks(CredentialType.Valid, flipped); len(got) != 1 {
		t.Fatalf("a changed presentation must be reported once, got %v", got)
	}
}
