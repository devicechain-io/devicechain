// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"runtime"
	"testing"

	"helm.sh/helm/v3/pkg/releaseutil"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

// apiServerSuiteBuilt is set by apiserver_render_test.go, which is built everywhere but
// Windows. It lets a test that builds on every platform see whether the API-server
// suites were compiled into this binary.
var apiServerSuiteBuilt bool

// TestTheAPIServerSuitesAreBuiltOffWindows fails if the build constraint on
// apiserver_render_test.go ever excludes a platform it should not. A constraint that
// drops the file reports nothing by itself: its tests simply stop existing, and the
// package still passes.
func TestTheAPIServerSuitesAreBuiltOffWindows(t *testing.T) {
	want := runtime.GOOS != "windows"
	if apiServerSuiteBuilt != want {
		t.Fatalf("on %s the API-server suites are built=%v, want %v: check the build constraint on apiserver_render_test.go",
			runtime.GOOS, apiServerSuiteBuilt, want)
	}
}

// renderedDeployment returns the named Deployment from manifest, decoded.
func renderedDeployment(t *testing.T, manifest, name string) *appsv1.Deployment {
	t.Helper()
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var d appsv1.Deployment
		if err := yaml.Unmarshal([]byte(doc), &d); err != nil {
			t.Fatalf("decoding a rendered document: %v\n%s", err, doc)
		}
		if d.Kind == "Deployment" && d.Name == name {
			return &d
		}
	}
	t.Fatalf("the manifest renders no Deployment %q", name)
	return nil
}
