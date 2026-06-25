// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package generators

import (
	"bytes"
	"encoding/json"

	"github.com/devicechain-io/dc-k8s/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/printers"
)

// Information required to create a resource file
type ConfigurationResource struct {
	Name    string
	Content []byte
}

// Generate an instance configuration custom resource
func GenerateInstanceConfig(id string, content interface{}) ([]byte, error) {
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}

	config := &v1beta1.InstanceConfiguration{
		TypeMeta: metav1.TypeMeta{
			Kind:       "InstanceConfiguration",
			APIVersion: v1beta1.GroupVersion.Group + "/" + v1beta1.GroupVersion.Version,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: id,
		},
		Spec: v1beta1.InstanceConfigurationSpec{
			Configuration: v1beta1.EntityConfiguration{RawMessage: raw},
		}}
	y := printers.YAMLPrinter{}
	var buff = new(bytes.Buffer)
	err = y.PrintObj(config, buff)
	if err != nil {
		return nil, err
	}
	return buff.Bytes(), nil
}
