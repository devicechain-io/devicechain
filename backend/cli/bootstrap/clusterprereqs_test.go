// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// 🔴 NO ARCHIVE MEANS NO VARIABLES, NOT EMPTY ONES. Every archive variable has a
// default in the instance root, and an empty endpoint does NOT mean "no archive" to
// barman-cloud — it means the default AWS S3 endpoint. Passing empties would override
// working defaults with a configuration that archives at real AWS, to a bucket that
// does not exist, failing per WAL segment without stalling a single write.
func TestNoArchiveMeansNoArchiveVariables(t *testing.T) {
	if got := (ClusterArchive{}).archiveVars(); got != nil {
		t.Errorf("a cluster with no archive emitted %v; empty values would override the "+
			"instance root's defaults with the real AWS endpoint", got)
	}
	// ...and an archive that names a store but whose endpoint never arrived is the
	// same hazard wearing a fuller struct.
	partial := ClusterArchive{CredentialsSecret: "s", AccessKeyIDKey: "a", BucketTsdb: "b"}
	if got := partial.archiveVars(); got != nil {
		t.Errorf("an archive with no endpoint emitted %v; the endpoint is the one field "+
			"whose absence must suppress the rest", got)
	}
}

// 🔴🔴 AND EVERY NAME IT EMITS MUST BE ONE THE INSTANCE ROOT DECLARES.
//
// This is the assertion that ties the contract to the tree rather than to itself. The
// five names are written out in archiveVars as literals; renaming one in the instance
// root's variables.tf would leave dcctl passing a name nothing receives, and the
// symptom is an event store built with the root's DEFAULT archive settings — pointed
// at an endpoint and credentials from a different install, or at none.
func TestTheArchiveContractNamesOnlyInstanceRootVariables(t *testing.T) {
	archive := ClusterArchive{
		EndpointURL:       "http://dc-object-store.dc-system:9000",
		CredentialsSecret: "dc-object-store-credentials",
		AccessKeyIDKey:    "MINIO_ROOT_USER",
		SecretAccessKey:   "MINIO_ROOT_PASSWORD",
		BucketTsdb:        "devicechain-tsdb",
	}
	vars := archive.archiveVars()
	if len(vars) != 5 {
		t.Fatalf("the archive contract emitted %d variables, want 5: %v", len(vars), vars)
	}

	// Routed through splitVars, which is what the apply does — so a name neither root
	// declares fails here exactly as it would there.
	cluster, instance, err := splitVars(vars)
	if err != nil {
		t.Fatalf("the archive contract names a variable no root declares: %v", err)
	}
	if len(instance) != 5 {
		t.Errorf("only %d of the 5 archive variables reach the INSTANCE root (%v); the event "+
			"store would be built with the root's own defaults for the rest", len(instance), instance)
	}
	// 🔑 The counterweight. backup_endpoint_url and backup_credentials_secret are
	// declared by BOTH roots — the cluster root uses them for an external destination
	// — so this must not assert the cluster root receives nothing. What it asserts is
	// that the two names unique to the contract are the instance root's alone.
	for _, own := range []string{"backup_access_key_id_key", "backup_secret_access_key_key"} {
		for _, got := range cluster {
			if strings.HasPrefix(got, own+"=") {
				t.Errorf("%q reached the cluster root; the credential key NAMES are what the "+
					"cluster root exports, not what it consumes", own)
			}
		}
	}
}

// 🔴🔴 THE ORDERING GUARD, AND IT IS THE ONLY THING ENFORCING A DEPENDENCY OPENTOFU
// USED TO ENFORCE FOR US.
//
// One root and one graph ordered "install the operator" before "create a database
// Cluster", and "create the namespace" before "write a Secret into it". Two roots are
// two graphs, so the edge between them is applyInfra's SEQUENCE and nothing else.
// applyInfra reaches a real tofu binary and a real cluster, so no unit test can run
// it — which is exactly the case leg 3 met and answered the same way: read the source.
//
// 🔑 IT PINS ORDER, NOT PRESENCE. All four calls being present in the wrong order is
// the defect: prerequisites applied after the instance means the event store is
// created before the operator that reconciles it, and credentials written after the
// namespace apply means CloudNativePG mints its own password while every service
// holds the one dcctl minted. Both come up green and silently wrong.
func TestApplyInfraAppliesThePrerequisitesBeforeTheInstance(t *testing.T) {
	fset := token.NewFileSet()
	src, err := os.ReadFile(filepath.Join("tofu.go"))
	if err != nil {
		t.Fatalf("reading tofu.go: %v", err)
	}
	file, err := parser.ParseFile(fset, "tofu.go", src, 0)
	if err != nil {
		t.Fatalf("parsing tofu.go: %v", err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == "applyInfra" {
			fn = d
		}
		return true
	})
	if fn == nil {
		t.Fatal("tofu.go no longer declares applyInfra; the two applies are no longer " +
			"sequenced by one function and nothing orders them")
	}

	positions := map[string]token.Pos{}
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		switch id.Name {
		case "ensureInfraNamespace", "writeMintedSecrets", "applyClusterPrereqs", "applyInstanceInfra", "splitVars":
			// First occurrence wins, so a later reference cannot reorder the record.
			if _, seen := positions[id.Name]; !seen {
				positions[id.Name] = call.Pos()
			}
		}
		return true
	})

	for _, want := range []struct{ name, why string }{
		{"splitVars", "every -var would go to both roots, and each would refuse the other's"},
		{"ensureInfraNamespace", "the credentials below cannot be written into a namespace that is not there"},
		{"writeMintedSecrets", "CloudNativePG would mint its own password and no service would hold it"},
		{"applyClusterPrereqs", "the cluster would have no operator, no ingress and no shared database"},
		{"applyInstanceInfra", "the instance's own broker and event store would never be applied"},
	} {
		if _, ok := positions[want.name]; !ok {
			t.Fatalf("applyInfra no longer calls %s — %s", want.name, want.why)
		}
	}

	for _, pair := range []struct{ first, then, why string }{
		{"ensureInfraNamespace", "writeMintedSecrets",
			"a Secret cannot be written into a namespace that does not exist yet"},
		{"writeMintedSecrets", "applyClusterPrereqs",
			"CloudNativePG reads the credentials Secret when it CREATES the shared relational " +
				"Cluster and never again, so a Secret written afterwards leaves the role on one " +
				"password and every service on another"},
		{"applyClusterPrereqs", "applyInstanceInfra",
			"the instance root's event store needs the operator and the backup plugin the " +
				"cluster root installs, and it is handed the archive contract that apply RETURNS"},
	} {
		if positions[pair.first] > positions[pair.then] {
			t.Errorf("applyInfra calls %s at %s, AFTER %s at %s — %s",
				pair.first, fset.Position(positions[pair.first]),
				pair.then, fset.Position(positions[pair.then]), pair.why)
		}
	}
}

// 🔴 A MISSING OUTPUT MUST STOP THE RUN, and the mutation round is why this test
// exists: reading an absent output as an empty string SURVIVED, because every branch
// inside applyClusterPrereqs needs a tofu binary and a live cluster to reach. The
// decode was extracted so the input class that reaches it can be supplied.
//
// 🔑 THE FAILURE THIS PREVENTS IS SILENT. Empty is a legitimate value — it is what the
// cluster root exports when backups are off — so an absent output read as empty says
// "this cluster has no archive" when what actually happened is that the cluster root
// stopped exporting it. The instance is then built with archiving disabled, comes up
// green, and the difference surfaces when someone tries to restore.
func TestAMissingArchiveOutputIsAnErrorNotAnEmptyArchive(t *testing.T) {
	full := map[string]tfexec.OutputMeta{
		"backup_endpoint_url":          {Value: []byte(`"http://dc-object-store.dc-system:9000"`)},
		"backup_credentials_secret":    {Value: []byte(`"dc-object-store-credentials"`)},
		"backup_access_key_id_key":     {Value: []byte(`"MINIO_ROOT_USER"`)},
		"backup_secret_access_key_key": {Value: []byte(`"MINIO_ROOT_PASSWORD"`)},
		"backup_bucket_tsdb":           {Value: []byte(`"devicechain-tsdb"`)},
	}

	// The counterweight first: a complete set must decode, or the refusals below are
	// satisfied by a function that rejects everything.
	got, err := archiveFromOutputs(full)
	if err != nil {
		t.Fatalf("a complete output set was refused: %v", err)
	}
	if got.EndpointURL == "" || got.BucketTsdb != "devicechain-tsdb" {
		t.Fatalf("the contract decoded wrongly: %+v", got)
	}

	// ...and every field on its own, so a check that happens to notice one absence
	// cannot stand in for the rest.
	for name := range full {
		t.Run("missing "+name, func(t *testing.T) {
			partial := map[string]tfexec.OutputMeta{}
			for k, v := range full {
				if k != name {
					partial[k] = v
				}
			}
			_, err := archiveFromOutputs(partial)
			if err == nil {
				t.Fatalf("a missing %q output was read as an empty value; the instance would "+
					"be built with archiving silently disabled", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("the refusal does not name %q: %v", name, err)
			}
		})
	}
}

// 🔴 EMPTY IS A REAL ANSWER, and must not be confused with the absence above. The
// cluster root exports empty strings when backups are off, and that has to decode
// cleanly into an archive that emits no variables.
func TestAnArchiveThatIsOffDecodesRatherThanFailing(t *testing.T) {
	off := map[string]tfexec.OutputMeta{
		"backup_endpoint_url":          {Value: []byte(`""`)},
		"backup_credentials_secret":    {Value: []byte(`""`)},
		"backup_access_key_id_key":     {Value: []byte(`""`)},
		"backup_secret_access_key_key": {Value: []byte(`""`)},
		"backup_bucket_tsdb":           {Value: []byte(`""`)},
	}
	got, err := archiveFromOutputs(off)
	if err != nil {
		t.Fatalf("a cluster with backups off was refused: %v", err)
	}
	if vars := got.archiveVars(); vars != nil {
		t.Errorf("a cluster with no archive emitted %v", vars)
	}
}

// 🔴 NO CLUSTER IDENTITY, NO APPLY. The guard is the first statement in applyInfra
// and returns before any tofu binary or cluster is reached, which is what makes it
// testable at all — and the mutation round found it untested.
//
// Falling back to the kube-context name is the tempting alternative and is exactly
// what must not happen: a cluster deleted and recreated wears the same context name,
// so state filed under it would be inherited by a cluster holding none of those
// resources, and the apply would plan updates to things that do not exist.
func TestApplyInfraRefusesAClusterItCannotIdentify(t *testing.T) {
	err := applyInfra(context.Background(), &State{
		Instance: "a", KubeContext: "kind-a", Values: map[string]string{},
	})
	if err == nil {
		t.Fatal("a bootstrap with no cluster identity was allowed to apply; it would file " +
			"the shared prerequisite state under a key the next cluster inherits")
	}
	if !strings.Contains(err.Error(), "identity") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}
