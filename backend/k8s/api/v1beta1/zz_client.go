// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"sync"

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

var clientInitOnce sync.Once
var clientInitErr error

// InitClient lazily builds the package-global Kubernetes clients from the ambient
// kubeconfig, exactly once, returning any error instead of dying. Commands that
// use ClientConfig / V1Client / V1Beta1Client must call it first.
//
// It is deliberately NOT invoked from init(): dcctl must run (version, help, and
// `bootstrap` — which creates the cluster) with no reachable cluster or even no
// current-context set. An eager GetConfigOrDie() here would abort the process
// before main, breaking exactly the bootstrap-from-nothing path.
func InitClient() error {
	clientInitOnce.Do(func() {
		cfg, err := config.GetConfig()
		if err != nil {
			clientInitErr = err
			return
		}
		cfg.RateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
		ClientConfig = cfg
		if clientInitErr = initV1Beta1Client(); clientInitErr != nil {
			return
		}
		clientInitErr = initV1Client()
	})
	return clientInitErr
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
