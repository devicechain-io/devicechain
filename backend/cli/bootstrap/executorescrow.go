// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-microservice/secrets/escrow"
	"k8s.io/client-go/dynamic"
)

type executorEscrowEvidence struct {
	Instance, ArtifactInstance string
	ArtifactPresent, Matches   bool
	ArtifactErr, KeyErr        error
}

func judgeExecutorEscrow(evidence []executorEscrowEvidence) error {
	var failures []string
	for _, e := range evidence {
		reason := ""
		switch {
		case e.ArtifactErr != nil:
			reason = "escrow artifact is unreadable or invalid"
		case !e.ArtifactPresent:
			reason = "escrow artifact is missing"
		case e.ArtifactInstance != e.Instance:
			reason = "escrow artifact names another instance"
		case e.KeyErr != nil:
			reason = "deployed root key cannot be read"
		case !e.Matches:
			reason = "escrow fingerprint does not match the deployed root key"
		}
		if reason != "" {
			failures = append(failures, e.Instance+": "+reason)
		}
	}
	if len(failures) == 0 {
		return nil
	}
	sort.Strings(failures)
	// Read errors are deliberately not formatted: a credential parser's error
	// must never turn into a way to print root-key material.
	return fmt.Errorf("executor enrollment requires an escrow artifact whose fingerprint matches the deployed root key for every Instance (OwnerOffline):\n  %s\nRe-run dcctl bootstrap for each listed instance to reconcile escrow (a missing artifact is written). Preserve and inspect a mismatched artifact before moving it aside; use --escrow-dir when the artifacts are stored elsewhere", strings.Join(failures, "\n  "))
}

var executorReadEscrow = os.ReadFile
var executorReadRootKey = DeployedRootKey

func executorEscrowPath(instance, dir string) (string, error) {
	path, err := DefaultEscrowPath(instance)
	if err != nil {
		return "", err
	}
	if dir != "" {
		path = filepath.Join(dir, filepath.Base(path))
	}
	return path, nil
}

func readExecutorEscrow(ctx context.Context, kubeContext, instance, dir string) executorEscrowEvidence {
	e := executorEscrowEvidence{Instance: instance}
	path, err := executorEscrowPath(instance, dir)
	if err != nil {
		e.ArtifactErr = err
		return e
	}
	raw, err := executorReadEscrow(path)
	if err != nil {
		if !os.IsNotExist(err) {
			e.ArtifactErr = err
		}
		return e
	}
	art, err := escrow.Decode(raw)
	if err != nil {
		e.ArtifactErr = err
		return e
	}
	e.ArtifactPresent, e.ArtifactInstance = true, art.Instance
	key, err := executorReadRootKey(ctx, kubeContext, instance)
	defer zeroBytes(key)
	e.KeyErr = err
	if err == nil {
		e.Matches = art.Matches(key)
	}
	return e
}

func requireExecutorEscrows(ctx context.Context, dyn dynamic.Interface, kubeContext, dir string) error {
	instances, err := listExecutorResource(ctx, dyn, instanceGVR)
	if err != nil {
		return fmt.Errorf("cannot verify escrow for every Instance: listing Instances: %w", err)
	}
	evidence := make([]executorEscrowEvidence, 0, len(instances))
	for _, instance := range instances {
		evidence = append(evidence, readExecutorEscrow(ctx, kubeContext, instance.GetName(), dir))
	}
	return judgeExecutorEscrow(evidence)
}
