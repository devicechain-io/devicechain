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

package keycloak

import (
	"context"
	"fmt"
	"time"

	gocloak "github.com/Nerzal/gocloak/v11"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

// Manages lifecycle of Keycloak interactions.
type KeycloakManager struct {
	Microservice *core.Microservice

	// Keycloak client.
	Keycloak gocloak.GoCloak
	JWT      *gocloak.JWT

	lifecycle core.LifecycleManager
}

// Create a new Keycloak manager.
func NewKeycloakManager(ms *core.Microservice, callbacks core.LifecycleCallbacks) *KeycloakManager {
	kc := &KeycloakManager{
		Microservice: ms,
	}

	// Create lifecycle manager.
	name := fmt.Sprintf("%s-%s", ms.FunctionalArea, "keycloak")
	kc.lifecycle = core.NewLifecycleManager(name, kc, callbacks)
	return kc
}

// Initialize component.
func (kmgr *KeycloakManager) Initialize(ctx context.Context) error {
	return kmgr.lifecycle.Initialize(ctx)
}

// Get realm name used for tenant.
func (kmgr *KeycloakManager) GetRealmName(ctx context.Context) string {
	return fmt.Sprintf("%s-%s", kmgr.Microservice.InstanceId, kmgr.Microservice.TenantId)
}

// Verify that a realm exists for the given tenant.
func (kmgr *KeycloakManager) VerifyTenantRealm(ctx context.Context) error {
	// Handle verification within a distributed lock in case multiple replicas run concurrently.
	err := kmgr.Microservice.WithDistributedLock(ctx, 3*time.Second, 3, func(ctx context.Context) error {
		log.Info().Msg("Verifying tenant realm...")

		// Search for existing realm.
		realmName := kmgr.GetRealmName(ctx)
		existing, err := kmgr.Keycloak.GetRealm(ctx, kmgr.JWT.AccessToken, realmName)
		if err != nil {
			log.Info().Msg(fmt.Sprintf("Tenant realm not found. Error was '%s'.", err))
		}

		if existing != nil {
			log.Info().Msg(fmt.Sprintf("Found tenant realm '%s'.", *existing.Realm))
		} else {
			// Create new realm.
			rname := fmt.Sprintf("Instance %s - Tenant %s", kmgr.Microservice.InstanceId, kmgr.Microservice.TenantName)
			realm := gocloak.RealmRepresentation{
				ID:          &realmName,
				Realm:       &realmName,
				DisplayName: &rname,
				Enabled:     gocloak.BoolP(true),
			}
			result, err := kmgr.Keycloak.CreateRealm(ctx, kmgr.JWT.AccessToken, realm)
			if err != nil {
				return err
			}
			log.Info().Msg(fmt.Sprintf("Realm created resulted in '%s'.", result))
		}

		return nil
	})
	if err != nil {
		return err
	}
	log.Info().Msg("Verified tenant realm successfully.")
	return nil
}

// Update token by executing a login.
func (kmgr *KeycloakManager) UpdateToken(ctx context.Context) error {
	jwt, err := kmgr.Keycloak.LoginAdmin(ctx, "devicechain", "devicechain", "master")
	if err != nil {
		return err
	}
	kmgr.JWT = jwt
	return nil
}

// Lifecycle callback that runs initialization logic.
func (kmgr *KeycloakManager) ExecuteInitialize(ctx context.Context) error {
	kconfig := kmgr.Microservice.InstanceConfiguration.Infrastructure.Keycloak
	url := fmt.Sprintf("%s://%s:%d", "http", kconfig.Hostname, 8080)

	// Create client and update token.
	kmgr.Keycloak = gocloak.NewClient(url)
	kmgr.UpdateToken(ctx)
	log.Info().Msg("Logged in to Keycloak master realm successfully.")

	// Verify tenant realm exists.
	err := kmgr.VerifyTenantRealm(ctx)
	if err != nil {
		return err
	}

	return nil
}

// Start component.
func (kmgr *KeycloakManager) Start(ctx context.Context) error {
	return kmgr.lifecycle.Start(ctx)
}

// Lifecycle callback that runs startup logic.
func (kmgr *KeycloakManager) ExecuteStart(context.Context) error {
	return nil
}

// Stop component.
func (kmgr *KeycloakManager) Stop(ctx context.Context) error {
	return kmgr.lifecycle.Stop(ctx)
}

// Lifecycle callback that runs shutdown logic.
func (kmgr *KeycloakManager) ExecuteStop(context.Context) error {
	return nil
}

// Terminate component.
func (kmgr *KeycloakManager) Terminate(ctx context.Context) error {
	return kmgr.lifecycle.Terminate(ctx)
}

// Lifecycle callback that runs termination logic.
func (kmgr *KeycloakManager) ExecuteTerminate(context.Context) error {
	return nil
}
