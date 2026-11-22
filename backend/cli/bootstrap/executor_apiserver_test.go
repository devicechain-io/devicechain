// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package bootstrap

import (
	"io"
	"path/filepath"
	"testing"

	dcapply "github.com/devicechain-io/dc-k8s/apply"
	operatorconfig "github.com/devicechain-io/dc-k8s/config"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// CI supplies envtest binaries. In addition to chart profile validation, submit
// the real executor overlay against the real release CRDs and API validation.
func TestExecutorOverlayIsAcceptedByAnAPIServer(t *testing.T) {
	t.Parallel()
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "k8s", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting the executor test API server: %v; install envtest with: %s", err, envtestVersionHint)
	}
	t.Cleanup(func() { _ = env.Stop() })
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mapper, err := dcapply.NewApplyOptions(dyn, disco).ToRESTMapper()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := operatorconfig.RenderExecutor(executorTestImage(), operatorconfig.EnrollmentInput{Owner: "ops", ProtocolVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	objects, err := dcapply.Decode(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyExecutorObjects(t.Context(), dyn, mapper, objects, io.Discard); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if missing := everyObjectIsThere(t, c, string(manifest)); len(missing) != 0 {
		t.Fatalf("executor objects were not applied: %v", missing)
	}
}
