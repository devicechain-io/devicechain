/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

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

type ConfigurationResourceProvider interface {
	GetConfigurationResources() ([]ConfigurationResource, error)
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

// Generate a microservice configuration custom resource
func GenerateMicroserviceConfig(id string, functionalArea string, image string, content interface{}) ([]byte, error) {
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}

	config := &v1beta1.MicroserviceConfiguration{
		TypeMeta: metav1.TypeMeta{
			Kind:       "MicroserviceConfiguration",
			APIVersion: v1beta1.GroupVersion.Group + "/" + v1beta1.GroupVersion.Version,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: id,
		},
		Spec: v1beta1.MicroserviceConfigurationSpec{
			FunctionalArea: functionalArea,
			Image:          image,
			Configuration:  v1beta1.EntityConfiguration{RawMessage: raw},
		}}
	y := printers.YAMLPrinter{}
	var buff = new(bytes.Buffer)
	err = y.PrintObj(config, buff)
	if err != nil {
		return nil, err
	}
	return buff.Bytes(), nil
}
