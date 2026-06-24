// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
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
	LABEL_INSTANCE     = "devicechain.io.instance"
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

// Get a microservice configuration based on request criteria
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
