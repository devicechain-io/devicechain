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
// root applied against the same cluster.
//
// 🔑 IT IS READ BACK, NOT RE-DERIVED. dcctl knows what it asked the cluster root
// for, and that is precisely the thing not worth passing on: the endpoint is a
// Service DNS name the root composes, and the credential KEY NAMES depend on which
// destination the store turned out to be. Recomputing either here would be a second
// copy of the cluster root's logic, free to disagree with the store that exists.
type ClusterArchive struct {
	EndpointURL       string
	CredentialsSecret string
	AccessKeyIDKey    string
	SecretAccessKey   string
	BucketTsdb        string
}

// applyClusterPrereqs applies the cluster prerequisite root — the CloudNativePG
// operator and its backup plugin, cert-manager, ingress-nginx, monitoring, the
// shared infrastructure namespace, the shared relational store and the backup object
// store — and returns the archive contract an instance root needs.
//
// 🔴 IT IS KEYED ON THE CLUSTER'S IDENTITY, NOT ON AN INSTANCE. That is what makes
// it convergent: every instance bootstrapped against one cluster drives the same
// state, so the second bootstrap re-applies it as a no-op instead of building a
// second copy of the prerequisites. Keying it on the instance would put one
// operator's ingress controller inside another instance's state and destroy it with
// that instance.
//
// 🔴 AND THE IDENTITY IS THE kube-system NAMESPACE UID, NOT THE CONTEXT NAME. A
// `kind delete cluster` followed by `kind create cluster` produces a NEW cluster
// wearing the SAME context name; state keyed on the name would be inherited by a
// cluster that has none of these resources, and the apply would plan updates to
// things that do not exist.
func applyClusterPrereqs(ctx context.Context, st *State, uid string, vars []string, namespace string) (_ ClusterArchive, err error) {
	var archive ClusterArchive

	tofuBin, err := findTofu()
	if err != nil {
		return archive, err
	}

	workdir, err := clusterStateDir(uid, prereqStateSubdir)
	if err != nil {
		return archive, err
	}
	// The extracted tree holds every root plus the shared modules, and a root reaches
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
	if err := extractFS(assets.OpenTofu(), workdir); err != nil {
		return archive, fmt.Errorf("extracting cluster prerequisite config: %w", err)
	}

	tf, err := tfexec.NewTerraform(rootdir, tofuBin)
	if err != nil {
		return archive, err
	}
	tf.SetStdout(os.Stdout)
	tf.SetStderr(os.Stderr)
	tf.SetWaitDelay(tofuGracefulStopBudget)

	if err := tf.Init(ctx); err != nil {
		return archive, fmt.Errorf("tofu init (cluster prerequisites): %w", err)
	}

	// The shared infrastructure namespace is IMPORTED into this root's state rather
	// than switched off in its configuration.
	//
	// 🔴 AND IT IS THIS ROOT THAT ADOPTS IT, WHICH IS THE WHOLE POINT OF THE SPLIT.
	// dcctl creates the namespace before either apply, so something has to reconcile
	// the object with the declaration that manages it; before the split that was the
	// instance root, which meant one instance's teardown could plan the destruction
	// of a namespace holding every other instance's data. The namespace is a cluster
	// prerequisite, so it belongs to the cluster's state and outlives every instance.
	if err := adoptInfraNamespace(ctx, tf, namespace, vars); err != nil {
		return archive, err
	}

	opts := make([]tfexec.ApplyOption, 0, len(vars))
	for _, v := range vars {
		opts = append(opts, tfexec.Var(v))
	}
	if err := tf.Apply(ctx, opts...); err != nil {
		// 🔑 The CNPG admission race the instance apply retries around cannot happen
		// here, and the reason is worth stating rather than leaving as an omission:
		// that race is the API server failing to reach the CloudNativePG webhook
		// while creating a database Cluster. This root INSTALLS the operator, so on
		// a fresh cluster there is no webhook to be unreachable, and on a re-run the
		// operator is already converged. The shared relational store is created in
		// the same apply as the operator, which is the one ordering OpenTofu's graph
		// still enforces for us.
		return archive, fmt.Errorf("tofu apply (cluster prerequisites): %w", err)
	}

	outputs, err := tf.Output(ctx)
	if err != nil {
		return archive, fmt.Errorf("reading cluster prerequisite outputs: %w", err)
	}
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
			// meaningful value here — it is what this root returns when backups are
			// off — so reading an ABSENT output as empty would turn "the cluster root
			// no longer exports this" into "this cluster has no backups", and the
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
