// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"io/fs"
	"strings"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
)

// readTofuTree returns every .tf file in the embedded infrastructure tree, keyed by
// path. Reading the EMBEDDED copy rather than the source tree is deliberate: it is
// the one dcctl actually applies.
func readTofuTree(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	tree := assets.OpenTofu()
	err := fs.WalkDir(tree, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".tf") {
			return err
		}
		b, err := fs.ReadFile(tree, p)
		if err != nil {
			return err
		}
		out[p] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("reading the embedded infrastructure tree: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("the embedded infrastructure tree has no .tf files in it, so every " +
			"assertion below would pass by finding nothing")
	}
	return out
}

func tofuTreeContains(tree map[string]string, needle string) bool {
	for _, src := range tree {
		if strings.Contains(src, needle) {
			return true
		}
	}
	return false
}

// tofuTreeDeclares is tofuTreeContains with the COMMENTS TAKEN OUT, and the
// difference is not cosmetic.
//
// 🔴 A CHECK THAT READS PROSE IS A CHECK THAT FIRES ON PROSE. The retirement below
// is worth explaining where it happened, and every one of those explanations names
// the thing it removed — so a plain substring search reports each retired resource as
// still declared, by matching the comment that says it is gone. Measured: this test
// failed on its first run against a correctly retired tree, on the sentence
// describing the retirement.
//
// Stripping `#` comments is enough here because HCL has no string literal in this
// tree containing one, and the alternative — matching only assignment forms — would
// still read a commented-out assignment as live. This is the direction that fails
// safe: it can only ever report FEWER declarations, and the assertions below are all
// "this must be absent", so a miss is a test that stops protecting rather than one
// that blocks a correct change.
func tofuTreeDeclares(tree map[string]string, needle string) bool {
	for _, src := range tree {
		for _, line := range strings.Split(src, "\n") {
			if i := strings.Index(line, "#"); i >= 0 {
				line = line[:i]
			}
			if strings.Contains(line, needle) {
				return true
			}
		}
	}
	return false
}

// 🔴 THE SECRET NAMES ARE LITERALS ON BOTH SIDES OF A BOUNDARY NOTHING COMPILES
// ACROSS. dcctl writes these Secrets and the infrastructure tree tells its charts to
// read them, by name, as strings. Nothing fails to build if one side moves — the
// apply simply comes up with CloudNativePG minting a password of its own, or Grafana
// generating one, and the failure surfaces as an authentication error far from the
// rename that caused it.
//
// So this asserts the agreement from the embedded tree, which is the copy that runs.
func TestTheSecretNamesDcctlWritesAreTheOnesTheInfrastructureReads(t *testing.T) {
	tree := readTofuTree(t)

	for _, c := range []struct {
		what, written, expectedIn string
	}{
		// The cnpg module composes "<name>-app-credentials" and the root names the
		// two clusters, so both halves have to be present for the name to resolve.
		{"the relational store's credential", rdbClusterName, `name               = "dc-rdb"`},
		{"the event store's credential", tsdbClusterName, `name               = "dc-tsdb"`},
		{"the object store's credential", objectStoreName + "-credentials",
			`default     = "dc-object-store-credentials"`},
		{"the dashboard's credential", grafanaSecretName, `default     = "dc-grafana-admin"`},
		{"the broker's TLS material", natsReleaseName, `default     = "dc-nats"`},
	} {
		if !tofuTreeContains(tree, c.expectedIn) {
			t.Errorf("%s: dcctl writes a Secret named after %q, and the infrastructure tree no "+
				"longer contains %s. One side was renamed without the other, and the apply will "+
				"come up with a credential nothing else holds",
				c.what, c.written, c.expectedIn)
		}
	}

	// The composed forms, stated here because the module builds them by
	// interpolation and a test that only checked the pieces would miss the shape.
	if !tofuTreeContains(tree, `credentials_secret = "${var.name}-app-credentials"`) {
		t.Error("the database credential Secret is no longer named <cluster>-app-credentials, " +
			"so the names dcctl writes are not the ones CloudNativePG bootstraps from")
	}
	if !tofuTreeContains(tree, `tls_secret_name       = "${var.release_name}-tls"`) {
		t.Error("the broker's TLS Secret is no longer named <release>-tls, so the Secret dcctl " +
			"writes is not the one the broker's pods mount")
	}
}

// 🔴 THE RETIRED RESOURCES MUST STAY RETIRED, AND THE FENCE CANNOT TELL YOU THEY DID
// NOT. fence.go refuses an instance whose STATE still holds these, which is about
// instances built before the cutover. This is the other half: that the configuration
// itself has not grown them back. A re-added resource would take ownership of a
// credential dcctl writes, and the two writers would overwrite each other on
// alternating applies.
func TestTheInfrastructureNoLongerDeclaresACredentialItGaveUp(t *testing.T) {
	tree := readTofuTree(t)

	for _, c := range []struct{ decl, why string }{
		{`resource "kubernetes_secret_v1" "app"`,
			"the database credentials, which CloudNativePG reads when it creates a Cluster"},
		{`resource "kubernetes_secret_v1" "credentials"`,
			"the object store's root credentials"},
		{`resource "tls_private_key" "ca"`,
			"the broker's certificate authority private key — the one confirmed disclosure this moved to close"},
		{`resource "tls_self_signed_cert" "ca"`, "the broker's certificate authority"},
		{`resource "tls_locally_signed_cert" "server"`, "the broker's server certificate"},
		{`resource "kubernetes_secret_v1" "nats_tls"`, "the broker's TLS material"},
		{`adminPassword =`, "the dashboard's break-glass login"},
	} {
		if tofuTreeDeclares(tree, c.decl) {
			t.Errorf("the infrastructure tree declares %s again (%s). dcctl writes that "+
				"credential now, so two writers would overwrite each other on alternating "+
				"applies — and the value would be back in the infrastructure state",
				c.decl, c.why)
		}
	}
}

// The counterweight: the credential that is SUPPLIED rather than minted stays where
// it is. An external backup destination is somebody else's object store, so its
// credentials cannot be minted by anyone here — and a sweep that retired this one
// along with the rest would leave no way to reach the archive at all.
func TestTheSuppliedBackupCredentialIsNotRetiredWithTheMintedOnes(t *testing.T) {
	tree := readTofuTree(t)
	if !tofuTreeContains(tree, `resource "kubernetes_secret_v1" "backup_credentials"`) {
		t.Error("the external backup destination's Secret was retired along with the minted " +
			"credentials. It is supplied, not minted — removing it leaves an external archive " +
			"unreachable, with nothing to replace it until --backup-credentials-file lands")
	}
}
