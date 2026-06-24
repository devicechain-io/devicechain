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

package v1beta1

import (
	"context"
	"errors"
	"fmt"
	"log"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	LABEL_TENANT       = "devicechain.io.tenant"
	LABEL_MICROSERVICE = "devicechain.io.microservice"
)

var (
	ClientConfig  *rest.Config
	V1Beta1Client client.Client
	V1Client      client.Client
)

// Get instance configuraion by id
func GetInstanceConfiguration(id string) (*InstanceConfiguration, error) {
	ic := &InstanceConfiguration{}
	err := V1Beta1Client.Get(context.Background(), client.ObjectKey{
		Name: id,
	}, ic)
	if err != nil {
		return nil, err
	}
	return ic, nil
}

// Create a new DeviceChain instance CR.
func CreateInstance(request InstanceCreateRequest) (*Instance, error) {
	ic, err := GetInstanceConfiguration(request.ConfigurationId)
	if err != nil {
		return nil, err
	}

	instance := &Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name: request.Id,
		},
		Spec: InstanceSpec{
			Name:            request.Name,
			Description:     request.Description,
			ConfigurationId: request.ConfigurationId,
			Configuration:   EntityConfiguration{RawMessage: ic.Spec.Configuration.RawMessage},
		},
	}

	// Attempt to create the instance.
	err = V1Beta1Client.Create(context.Background(), instance)
	if err != nil {
		return nil, err
	}

	// Attempt to get the created instance.
	err = V1Beta1Client.Get(context.Background(), client.ObjectKey{
		Name: request.Id,
	}, instance)
	if err != nil {
		return nil, err
	}
	return instance, nil
}

// Get an instance based on request criteria
func GetInstance(request InstanceGetRequest) (*Instance, error) {
	instance := &Instance{}
	err := V1Beta1Client.Get(context.Background(), client.ObjectKey{
		Name: request.Id,
	}, instance)
	if err != nil {
		return nil, err
	}
	return instance, nil
}

// Create a new DeviceChain tenant CR.
func CreateTenant(request TenantCreateRequest) (*Tenant, error) {
	// Create tenant in instance namespace
	tenant := &Tenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      request.TenantId,
			Namespace: request.InstanceId,
		},
		Spec: TenantSpec{
			Name:        request.Name,
			Description: request.Description,
		},
	}

	// Attempt to create the tenant.
	err := V1Beta1Client.Create(context.Background(), tenant)
	if err != nil {
		return nil, err
	}

	// Attempt to get the created tenant.
	err = V1Beta1Client.Get(context.Background(), client.ObjectKey{
		Name:      request.TenantId,
		Namespace: request.InstanceId,
	}, tenant)
	if err != nil {
		return nil, err
	}
	return tenant, nil
}

// Get a tenant based on request criteria
func GetTenant(request TenantGetRequest) (*Tenant, error) {
	tenant := &Tenant{}
	err := V1Beta1Client.Get(context.Background(), client.ObjectKey{
		Name:      request.TenantId,
		Namespace: request.InstanceId,
	}, tenant)
	if err != nil {
		return nil, err
	}
	return tenant, nil
}

// Get an microservice configuration based on request criteria
func GetMicroserviceConfiguration(request MicroserviceConfigurationGetRequest) (*MicroserviceConfiguration, error) {
	msconfig := &MicroserviceConfiguration{}
	err := V1Beta1Client.Get(context.Background(), client.ObjectKey{
		Name: request.Id,
	}, msconfig)
	if err != nil {
		return nil, err
	}
	return msconfig, nil
}

// Create a new DeviceChain microservice CR.
func CreateMicroservice(request MicroserviceCreateRequest) (*Microservice, error) {
	if request.ConfigurationId == "" {
		return nil, errors.New("configuration id must be provided when creating microservice")
	}
	msc, err := GetMicroserviceConfiguration(MicroserviceConfigurationGetRequest{Id: request.ConfigurationId})
	if err != nil {
		return nil, err
	}

	// Create ms in instance namespace
	ms := &Microservice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      request.Id,
			Namespace: request.InstanceId,
		},
		Spec: MicroserviceSpec{
			Name:            request.Name,
			Description:     request.Description,
			FunctionalArea:  msc.Spec.FunctionalArea,
			Image:           msc.Spec.Image,
			ImagePullPolicy: v1.PullIfNotPresent,
			ConfigurationId: request.ConfigurationId,
		},
	}

	// Attempt to create the microservice.
	err = V1Beta1Client.Create(context.Background(), ms)
	if err != nil {
		return nil, err
	}

	// Attempt to get the created microservice.
	err = V1Beta1Client.Get(context.Background(), client.ObjectKey{
		Name:      request.Id,
		Namespace: request.InstanceId,
	}, ms)
	if err != nil {
		return nil, err
	}
	return ms, nil
}

// Get a microservice based on request criteria
func GetMicroservice(request MicroserviceGetRequest) (*Microservice, error) {
	ms := &Microservice{}
	err := V1Beta1Client.Get(context.Background(), client.ObjectKey{
		Name:      request.MicroserviceId,
		Namespace: request.InstanceId,
	}, ms)
	if err != nil {
		return nil, err
	}
	return ms, nil
}

// List microservices that match the given criteria
func ListMicroservices(request MicroserviceListRequest) (*MicroserviceList, error) {
	mslist := &MicroserviceList{}
	err := V1Beta1Client.List(context.Background(), mslist, client.InNamespace(request.InstanceId))
	if err != nil {
		return nil, err
	}
	return mslist, nil
}

// Create a new tenant microservice CR.
func CreateTenantMicroservice(request TenantMicroserviceCreateRequest) (*TenantMicroservice, error) {
	if request.TenantId == "" {
		return nil, errors.New("tenant id must be provided when creating tenant microservice")
	}
	tenant, err := GetTenant(TenantGetRequest{
		InstanceId: request.InstanceId,
		TenantId:   request.TenantId})
	if err != nil {
		return nil, err
	}

	if request.MicroserviceId == "" {
		return nil, errors.New("microservice id must be provided when creating tenant microservice")
	}
	ms, err := GetMicroservice(MicroserviceGetRequest{
		InstanceId:     request.InstanceId,
		MicroserviceId: request.MicroserviceId})
	if err != nil {
		return nil, err
	}

	msc, err := GetMicroserviceConfiguration(MicroserviceConfigurationGetRequest{
		Id: ms.Spec.ConfigurationId,
	})
	if err != nil {
		return nil, err
	}

	// Create tenant ms in instance namespace
	tmsid := fmt.Sprintf("%s-%s-%s", "tms", tenant.ObjectMeta.Name, ms.ObjectMeta.Name)
	tms := &TenantMicroservice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tmsid,
			Namespace: tenant.GetObjectMeta().GetNamespace(),
			Labels: map[string]string{
				LABEL_TENANT:       tenant.GetObjectMeta().GetName(),
				LABEL_MICROSERVICE: ms.GetObjectMeta().GetName(),
			},
		},
		Spec: TenantMicroserviceSpec{
			MicroserviceId: request.MicroserviceId,
			TenantId:       request.TenantId,
			Configuration:  EntityConfiguration{RawMessage: msc.Spec.Configuration.RawMessage},
		},
	}

	// Attempt to create the tenant microservice.
	err = V1Beta1Client.Create(context.Background(), tms)
	if err != nil {
		return nil, err
	}

	// Attempt to get the created microservice.
	err = V1Beta1Client.Get(context.Background(), client.ObjectKey{
		Name:      tmsid,
		Namespace: tenant.GetObjectMeta().GetNamespace(),
	}, tms)
	if err != nil {
		return nil, err
	}
	return tms, nil
}

// Get a tenant microservice based on request criteria
func GetTenantMicroservice(request TenantMicroserviceGetRequest) (*TenantMicroservice, error) {
	tms := &TenantMicroservice{}
	err := V1Beta1Client.Get(context.Background(), client.ObjectKey{
		Name:      request.TenantMicroserviceId,
		Namespace: request.InstanceId,
	}, tms)
	if err != nil {
		return nil, err
	}
	return tms, nil
}

// Get a tenant microservice based on request criteria
func GetTenantMicroservicesForTenant(request TenantMicroserviceByTenantRequest) (*TenantMicroserviceList, error) {
	// List tenant microservices in instance namespace with tenant label
	tmslist := &TenantMicroserviceList{}
	err := V1Beta1Client.List(context.Background(), tmslist, client.InNamespace(request.InstanceId),
		client.MatchingLabels{LABEL_TENANT: request.TenantId})
	if err != nil {
		return nil, err
	}
	return tmslist, nil
}

// Delete a tenant microservice based on request criteria
func DeleteTenantMicroservice(request TenantMicroserviceDeleteRequest) (*TenantMicroservice, error) {
	// Look up the existing tenant microservice
	tms := &TenantMicroservice{}
	err := V1Beta1Client.Get(context.Background(), client.ObjectKey{
		Name:      request.TenantMicroserviceId,
		Namespace: request.InstanceId,
	}, tms)
	if err != nil {
		return nil, err
	}

	// Delete the tenant microservice
	err = V1Beta1Client.Delete(context.Background(), tms)
	if err != nil {
		return nil, err
	}

	return tms, nil
}

// Initialize client configuration
func initClientConfig() {
	ClientConfig = config.GetConfigOrDie()
	ClientConfig.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
}

// Initialize client for DeviceChain operations
func initV1Beta1Client() error {
	v1beta1, err := SchemeBuilder.Build()
	if err != nil {
		return err
	}
	V1Beta1Client, err = client.New(ClientConfig, client.Options{Scheme: v1beta1})
	if err != nil {
		return err
	}
	return nil
}

// Init client for interacting with v1 objects
func initV1Client() error {
	scheme := runtime.NewScheme()
	err := v1.SchemeBuilder.AddToScheme(scheme)
	if err != nil {
		return err
	}

	V1Client, err = client.New(ClientConfig, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	return nil
}

func init() {
	initClientConfig()
	err := initV1Beta1Client()
	if err != nil {
		log.Fatal("unable to initialize v1beta1 client", err)
	}
	err = initV1Client()
	if err != nil {
		log.Println("unable to initialize v1 client", err)
	}
}
