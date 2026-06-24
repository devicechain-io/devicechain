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

package main

import (
	"context"
	"encoding/json"

	gql "github.com/graph-gophers/graphql-go"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/graphql"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-device-management/schema"
	esconfig "github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	kcore "github.com/devicechain-io/dc-microservice/kafka"
	"github.com/devicechain-io/dc-microservice/rdb"
)

var (
	Microservice  *core.Microservice
	Configuration *config.DeviceManagementConfiguration

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	KakfaManager   *kcore.KafkaManager

	Api       *model.Api
	CachedApi *model.CachedApi

	InboundEventsReader    kcore.KafkaReader
	InboundEventsProcessor *processor.InboundEventsProcessor
	ResolvedEventsWriter   kcore.KafkaWriter
	FailedEventsWriter     kcore.KafkaWriter
)

func main() {
	callbacks := core.LifecycleCallbacks{
		Initializer: core.LifecycleCallback{
			Preprocess:  func(context.Context) error { return nil },
			Postprocess: afterMicroserviceInitialized,
		},
		Starter: core.LifecycleCallback{
			Preprocess:  func(context.Context) error { return nil },
			Postprocess: afterMicroserviceStarted,
		},
		Stopper: core.LifecycleCallback{
			Preprocess:  beforeMicroserviceStopped,
			Postprocess: func(context.Context) error { return nil },
		},
		Terminator: core.LifecycleCallback{
			Preprocess:  beforeMicroserviceTerminated,
			Postprocess: func(context.Context) error { return nil },
		},
	}
	Microservice = core.NewMicroservice(callbacks)
	Microservice.Run()
}

// Parses the configuration from raw bytes.
func parseConfiguration() error {
	config := &config.DeviceManagementConfiguration{}
	err := json.Unmarshal(Microservice.MicroserviceConfigurationRaw, config)
	if err != nil {
		return err
	}
	Configuration = config
	return nil
}

// Create kafka components used by this microservice.
func createKafkaComponents(kmgr *kcore.KafkaManager) error {
	// Create reader for inbound events.
	ievents, err := kmgr.NewReader(
		kmgr.NewScopedConsumerGroup(esconfig.KAFKA_TOPIC_INBOUND_EVENTS),
		kmgr.NewScopedTopic(esconfig.KAFKA_TOPIC_INBOUND_EVENTS))
	if err != nil {
		return err
	}
	InboundEventsReader = ievents

	// Add and initialize resolved events writer.
	revents, err := kmgr.NewWriter(kmgr.NewScopedTopic(config.KAFKA_TOPIC_RESOLVED_EVENTS))
	if err != nil {
		return err
	}
	ResolvedEventsWriter = revents

	// Add and initialize failed events writer.
	fevents, err := kmgr.NewWriter(kmgr.NewScopedTopic(config.KAFKA_TOPIC_FAILED_EVENTS))
	if err != nil {
		return err
	}
	FailedEventsWriter = fevents

	// Add and initialize inbound events processor.
	InboundEventsProcessor = processor.NewInboundEventsProcessor(Microservice, InboundEventsReader,
		ResolvedEventsWriter, FailedEventsWriter, core.NewNoOpLifecycleCallbacks(), Api)
	err = InboundEventsProcessor.Initialize(context.Background())
	if err != nil {
		return err
	}

	return nil
}

// Called after microservice has been initialized.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	err := parseConfiguration()
	if err != nil {
		return err
	}

	// Create and initialize rdb manager.
	rdbcb := core.NewNoOpLifecycleCallbacks()
	RdbManager = rdb.NewRdbManager(Microservice, rdbcb, schema.Migrations,
		Microservice.InstanceConfiguration.Persistence.Rdb, Configuration.RdbConfiguration)
	err = RdbManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Create RDB caches.
	model.InitializeCaches(RdbManager)

	// Wrap api around rdb manager.
	Api = model.NewApi(RdbManager)
	CachedApi = model.NewCachedApi(Api)

	// Create and initialize kafka manager.
	KakfaManager = kcore.NewKafkaManager(Microservice, core.NewNoOpLifecycleCallbacks(), createKafkaComponents)
	err = KakfaManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Map of providers that will be injected into graphql http context.
	providers := map[gqlcore.ContextKey]interface{}{
		gqlcore.ContextRdbKey: RdbManager,
		gqlcore.ContextApiKey: Api,
	}

	// Create and initialize graphql manager.
	gqlcb := core.NewNoOpLifecycleCallbacks()

	schema := graphql.SchemaContent
	parsed := gql.MustParseSchema(schema, &graphql.SchemaResolver{})
	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, gqlcb, *parsed, providers)
	err = GraphQLManager.Initialize(ctx)
	if err != nil {
		return err
	}

	return nil
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	err := RdbManager.Start(ctx)
	if err != nil {
		return err
	}

	err = GraphQLManager.Start(ctx)
	if err != nil {
		return err
	}

	// Start kafka manager.
	err = KakfaManager.Start(ctx)
	if err != nil {
		return err
	}

	// Start inbound events processor.
	err = InboundEventsProcessor.Start(ctx)
	if err != nil {
		return err
	}

	return nil
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	// Stop inbound events processor.
	err := InboundEventsProcessor.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop kafka manager.
	err = KakfaManager.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop graphql manager.
	err = GraphQLManager.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop rdb manager.
	err = RdbManager.Stop(ctx)
	if err != nil {
		return err
	}

	return nil
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	// Terminate inbound events processor.
	err := InboundEventsProcessor.Terminate(ctx)
	if err != nil {
		return err
	}

	// Terminate kafka manager.
	err = KakfaManager.Terminate(ctx)
	if err != nil {
		return err
	}

	// Terminate graphql manager.
	err = GraphQLManager.Terminate(ctx)
	if err != nil {
		return err
	}

	// Terminate rdb manager.
	err = RdbManager.Terminate(ctx)
	if err != nil {
		return err
	}

	return nil
}
