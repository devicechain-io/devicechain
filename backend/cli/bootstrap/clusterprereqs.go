// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/hashicorp/terraform-exec/tfexec"
)

// prereqStateSubdir is where the cluster prerequisite root's working directory and
// state live, under the cluster's own identity directory. A sibling of cluster.json
// rather than a replacement for it: the record says WHICH cluster this is, the state
// says what was built on it.
const prereqStateSubdir = "infra"

// ClusterArchive is the archive contract the cluster root hands to every instance
// root applied against the same cluster, as the install record stores it.
//
// 🔑 IT IS READ BACK, NOT RE-DERIVED. dcctl knows what it asked the cluster root
// for, and that is precisely the thing not worth passing on: the endpoint is a
// Service DNS name the root composes, and the credential KEY NAMES depend on which
// destination the store turned out to be. Recomputing either here would be a second
// copy of the cluster root's logic, free to disagree with the store that exists.
type ClusterArchive struct {
	EndpointURL       string `json:"endpointUrl,omitempty"`
	CredentialsSecret string `json:"credentialsSecret,omitempty"`
	AccessKeyIDKey    string `json:"accessKeyIdKey,omitempty"`
	SecretAccessKey   string `json:"secretAccessKeyKey,omitempty"`
	BucketTsdb        string `json:"bucketTsdb,omitempty"`
}

// applyClusterPrereqs applies the cluster prerequisite root — the CloudNativePG
// operator and its backup plugin, cert-manager, ingress-nginx, monitoring, the
// shared infrastructure namespace, the shared relational store and the backup object
// store — and returns what it built, for the install record.
//
// 🔴 IT IS KEYED ON THE CLUSTER'S IDENTITY, NOT ON AN INSTANCE. That is what makes
// it convergent: every install of one cluster drives the same state, so a re-install
// re-applies it as a no-op instead of building a second copy of the prerequisites.
// Keying it on an instance would put one operator's ingress controller inside that
// instance's state and destroy it with that instance.
//
// 🔴 AND THE IDENTITY IS THE kube-system NAMESPACE UID, NOT THE CONTEXT NAME. A
// `kind delete cluster` followed by `kind create cluster` produces a NEW cluster
// wearing the SAME context name; state keyed on the name would be inherited by a
// cluster that has none of these resources, and the apply would plan updates to
// things that do not exist.
func applyClusterPrereqs(ctx context.Context, st *State, uid string, vars []string, namespace string) (_ InstallOutputs, err error) {

	tofuBin, err := findTofu()
	if err != nil {
		return InstallOutputs{}, err
	}

	workdir, err := clusterStateDir(uid, prereqStateSubdir)
	if err != nil {
		return InstallOutputs{}, err
	}
	// The extracted tree holds THIS root plus the shared modules, and a root reaches
	// those as "../modules/<x>" — so tofu runs one level down, in the root's own
	// directory, exactly as the instance apply does.
	rootdir := filepath.Join(workdir, assets.ClusterRootDir)
	// Registered the moment the directory exists, because a FAILED apply writes state
	// too. The apply's own error is always the better one to hand back, so a chmod
	// failure only surfaces when nothing else went wrong.
	defer func() {
		if herr := hardenStateFiles(rootdir); herr != nil && err == nil {
			err = herr
		}
	}()
	if err := extractRoot(assets.OpenTofu(), assets.ClusterRootDir, workdir); err != nil {
		return InstallOutputs{}, fmt.Errorf("extracting cluster prerequisite config: %w", err)
	}

	tf, err := tfexec.NewTerraform(rootdir, tofuBin)
	if err != nil {
		return InstallOutputs{}, err
	}
	tf.SetStdout(os.Stdout)
	tf.SetStderr(os.Stderr)
	tf.SetWaitDelay(tofuGracefulStopBudget)

	if err := tf.Init(ctx); err != nil {
		return InstallOutputs{}, fmt.Errorf("tofu init (cluster prerequisites): %w", err)
	}

	// The shared infrastructure namespace is IMPORTED into this root's state rather
	// than switched off in its configuration.
	//
	// 🔴 AND IT IS THIS ROOT THAT ADOPTS IT, WHICH IS THE WHOLE POINT OF THE SPLIT.
	// dcctl creates the namespace before this apply, so something has to reconcile
	// the object with the declaration that manages it; before the split that was the
	// instance root, which meant one instance's teardown could plan the destruction
	// of a namespace holding every other instance's data. The namespace is a cluster
	// prerequisite, so it belongs to the cluster's state and outlives every instance.
	if err := adoptInfraNamespace(ctx, tf, namespace, vars); err != nil {
		return InstallOutputs{}, err
	}

	opts := make([]tfexec.ApplyOption, 0, len(vars))
	for _, v := range vars {
		opts = append(opts, tfexec.Var(v))
	}
	// 🔴 THIS IS THE ROOT THE CNPG ADMISSION RACE LIVES IN. It installs the operator
	// and creates the shared relational store in one graph, which is the race's exact
	// precondition — see applyWithCNPGAdmissionRetry.
	if err := applyWithCNPGAdmissionRetry(ctx, tf, opts, "tofu apply (cluster prerequisites)", func(ctx context.Context) error {
		return waitForCNPGAdmission(ctx, st.KubeContext, cnpgAdmissionTimeout)
	}); err != nil {
		return InstallOutputs{}, err
	}

	outputs, err := tf.Output(ctx)
	if err != nil {
		return InstallOutputs{}, fmt.Errorf("reading cluster prerequisite outputs: %w", err)
	}
	return clusterOutputs(outputs)
}

// clusterOutputs decodes what the cluster root built into the install record's outputs.
//
// 🔴 THESE WERE READ FROM THE INSTANCE ROOT AFTER THE SPLIT, WHICH DECLARES NONE OF
// THEM. Every optional read below is `if meta, ok := outputs[...]; ok`, so an output the
// root does not export is indistinguishable from one that is null — and the split
// turned four live reads into four permanent no-ops without a single failure. The CNPG
// operator's PodMonitor and control-plane alerts stopped rendering on every fresh
// install, the Grafana access line vanished from the report, and an off-site backup
// was reported as in-cluster. Found in review, not by a test; which root each key is
// read from is now held against each root's outputs.tf by
// TestEveryOutputDcctlReadsIsDeclaredByTheRootItIsReadFrom.
//
// Each of the four is null when what it describes was not installed, and null decodes
// as empty — which is what a bootstrap reads as "not there".
func clusterOutputs(outputs map[string]tfexec.OutputMeta) (InstallOutputs, error) {
	archive, err := archiveFromOutputs(outputs)
	if err != nil {
		return InstallOutputs{}, err
	}
	rdb, err := rdbFromOutputs(outputs)
	if err != nil {
		return InstallOutputs{}, err
	}
	out := InstallOutputs{Rdb: rdb, Archive: archive}
	// Whether those backups survive losing the cluster, which is a different
	// question from whether they exist and the one an operator is most likely to
	// get wrong. Null when backups are off; false for the default in-cluster
	// destination.
	if meta, ok := outputs["database_backup_survives_cluster_loss"]; ok {
		_ = json.Unmarshal(meta.Value, &out.BackupSurvivesClusterLoss)
	}
	// The namespace the CloudNativePG OPERATOR runs in — the database CONTROL
	// PLANE, which is a different tier from the databases and a different
	// namespace: dc-system holds the Clusters, cnpg-system holds the operator that
	// drives them. It gates the operator's PodMonitor and the control-plane
	// alerting rules together (ADR-020 A1.5).
	if meta, ok := outputs["cnpg_namespace"]; ok {
		out.CNPGNamespace = optionalStringOutput(meta)
	}
	// Grafana access (when monitoring was installed), for the report's port-forward hint.
	if meta, ok := outputs["grafana_service"]; ok {
		out.GrafanaService = optionalStringOutput(meta)
	}
	if meta, ok := outputs["grafana_namespace"]; ok {
		out.GrafanaNamespace = optionalStringOutput(meta)
	}
	return out, nil
}

// optionalStringOutput is a string output that may be null. Anything that does not
// decode as a string is empty too: validate refuses a record whose settings promise
// what an empty output cannot deliver.
func optionalStringOutput(meta tfexec.OutputMeta) string {
	var v string
	if json.Unmarshal(meta.Value, &v) != nil {
		return ""
	}
	return v
}

// archiveFromOutputs decodes the archive contract out of the cluster root's outputs.
//
// 🔑 SEPARATED FROM THE APPLY SO IT CAN BE EXERCISED. applyClusterPrereqs needs a tofu
// binary and a live cluster, so every branch inside it is unreachable from a test —
// and a mutation round proved it: reading a MISSING output as an empty one survived,
// because nothing could supply the input that reaches that branch. The decode is
// ordinary map handling and does not need a cluster to be wrong.
func archiveFromOutputs(outputs map[string]tfexec.OutputMeta) (ClusterArchive, error) {
	var archive ClusterArchive
	for _, field := range []struct {
		name string
		into *string
	}{
		{"backup_endpoint_url", &archive.EndpointURL},
		{"backup_credentials_secret", &archive.CredentialsSecret},
		{"backup_access_key_id_key", &archive.AccessKeyIDKey},
		{"backup_secret_access_key_key", &archive.SecretAccessKey},
		{"backup_bucket_tsdb", &archive.BucketTsdb},
	} {
		meta, ok := outputs[field.name]
		if !ok {
			// 🔴 A MISSING OUTPUT IS AN ERROR, NOT AN EMPTY STRING. Empty is a
			// meaningful value here — it is what the cluster root returns when backups
			// are off — so reading an ABSENT output as empty would turn "the cluster
			// root no longer exports this" into "this cluster has no backups", and the
			// instance would be built with archiving silently disabled.
			return archive, fmt.Errorf("the cluster prerequisite root did not export %q; "+
				"dcctl cannot tell whether this cluster has an archive or whether the "+
				"output was removed", field.name)
		}
		if err := json.Unmarshal(meta.Value, field.into); err != nil {
			return archive, fmt.Errorf("decoding %s output: %w", field.name, err)
		}
	}
	return archive, nil
}

// archiveVars renders the archive contract as instance-root variable assignments.
//
// 🔑 Emitted only when there is an archive to name. Every one of these has a default
// in the instance root, and passing an empty endpoint would override that default
// with a value that means "archive to the AWS S3 default endpoint" rather than "no
// archive" — the exact confusion the cluster root's own destination guard exists to
// refuse one level up.
func (a ClusterArchive) archiveVars() []string {
	if a.EndpointURL == "" {
		return nil
	}
	return []string{
		"backup_endpoint_url=" + a.EndpointURL,
		"backup_credentials_secret=" + a.CredentialsSecret,
		"backup_access_key_id_key=" + a.AccessKeyIDKey,
		"backup_secret_access_key_key=" + a.SecretAccessKey,
		"backup_bucket_tsdb=" + a.BucketTsdb,
	}
}
