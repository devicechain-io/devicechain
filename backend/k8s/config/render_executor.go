// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	api "github.com/devicechain-io/dc-k8s/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/yaml"
)

// Kept separate from overlay: rendering an operator never installs executor privileges.
//
//go:embed executor
var executorOverlay embed.FS

var digestImage = regexp.MustCompile(`^[^@\s]+@sha256:[a-f0-9]{64}$`)
var executorOwner = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

type EnrollmentInput struct {
	Owner           string
	ProtocolVersion int32
}

func RenderExecutor(image string, enrollment EnrollmentInput) ([]byte, error) {
	if !digestImage.MatchString(image) {
		return nil, fmt.Errorf("executor image must be pinned as name@sha256:<64 lowercase hex digits>")
	}
	if !executorOwner.MatchString(enrollment.Owner) || len(enrollment.Owner) > 63 {
		return nil, fmt.Errorf("executor owner must be a nonempty DNS label of at most 63 characters")
	}
	if enrollment.ProtocolVersion < 1 {
		return nil, fmt.Errorf("executor protocolVersion must be at least 1")
	}
	fsys := filesys.MakeFsInMemory()
	if err := fs.WalkDir(executorOverlay, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := executorOverlay.ReadFile(path)
		if err != nil {
			return err
		}
		return fsys.WriteFile(path, b)
	}); err != nil {
		return nil, fmt.Errorf("staging executor overlay: %w", err)
	}
	b, err := fsys.ReadFile("executor/deployment.yaml")
	if err != nil {
		return nil, err
	}
	// Replace a fixed scalar, never interpolate caller text into YAML syntax.
	quoted, err := yaml.Marshal(image)
	if err != nil {
		return nil, err
	}
	b = []byte(strings.Replace(string(b), "image: controller:latest", "image: "+strings.TrimSpace(string(quoted)), 1))
	if err := fsys.WriteFile("executor/deployment.yaml", b); err != nil {
		return nil, err
	}
	opts := krusty.MakeDefaultOptions()
	opts.Reorder = krusty.ReorderOptionLegacy
	resources, err := krusty.MakeKustomizer(opts).Run(fsys, "executor")
	if err != nil {
		return nil, fmt.Errorf("rendering executor overlay: %w", err)
	}
	rendered, err := resources.AsYaml()
	if err != nil {
		return nil, err
	}
	obj := api.ExecutorEnrollment{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "ExecutorEnrollment"}, ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Spec: api.ExecutorEnrollmentSpec{Owner: enrollment.Owner, ExecutorImage: image, EscrowDelivery: "OwnerOffline", ProtocolVersion: enrollment.ProtocolVersion}}
	// Marshal only desired fields; status is exclusively the executor's observation.
	declaration := map[string]interface{}{"apiVersion": obj.APIVersion, "kind": obj.Kind, "metadata": map[string]string{"name": obj.Name}, "spec": obj.Spec}
	tail, err := yaml.Marshal(declaration)
	if err != nil {
		return nil, err
	}
	return append(append(rendered, []byte("---\n")...), tail...), nil
}
