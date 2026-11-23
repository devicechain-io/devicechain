// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	core "github.com/devicechain-io/dc-microservice/core"

	"github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-event-processing/graphql"
)

// The shard count a deployment configures is the one the processor is built with. A processor
// config that dropped it would run every instance unsplit while the setting, the docs and the
// detect_shards gauge's own default all looked right.
func TestProcessorConfigCarriesDetectShards(t *testing.T) {
	c := config.NewEventProcessingConfiguration()
	c.DetectShards = 5
	if got := detectProcessorConfig(c).Shards; got != 5 {
		t.Fatalf("processor Shards = %d, want the configured 5", got)
	}
	if got := detectProcessorConfig(config.NewEventProcessingConfiguration()).Shards; got != 1 {
		t.Fatalf("default processor Shards = %d, want 1", got)
	}
}

// The publish window a deployment configures is the one the processor runs at; an absent one is
// the default.
func TestProcessorConfigCarriesThePublishWindow(t *testing.T) {
	c := config.NewEventProcessingConfiguration()
	w := 4
	c.DerivedPublishing.MaxInFlight = &w
	if got := detectProcessorConfig(c).Output.MaxInFlight; got != 4 {
		t.Fatalf("processor publish window = %d, want the configured 4", got)
	}
	if got := detectProcessorConfig(config.NewEventProcessingConfiguration()).Output.MaxInFlight; got != 32 {
		t.Fatalf("default processor publish window = %d, want 32", got)
	}
}

// The REACT concurrency a deployment configures is the one REACT runs at (reader slots and
// dispatcher lanes both take reactWorkers); an absent one is the default, 8, not the serial 1
// round 19 measured as the ceiling.
func TestReactWorkersCarriesTheConfiguredCount(t *testing.T) {
	c := config.NewEventProcessingConfiguration()
	w := 3
	c.React.Workers = &w
	if got := reactWorkers(c); got != 3 {
		t.Fatalf("react workers = %d, want the configured 3", got)
	}
	if got := reactWorkers(config.NewEventProcessingConfiguration()); got != config.DefaultReactWorkers || got < 2 {
		t.Fatalf("default react workers = %d, want %d", got, config.DefaultReactWorkers)
	}
}

// The preview cap a deployment configures is the one the replay gate enforces; an absent one is
// the default. A main that never installed it would run every instance at the default while the
// setting and the docs looked right.
func TestConfigurePreviewCapInstallsTheConfiguredCap(t *testing.T) {
	t.Cleanup(func() { graphql.SetPreviewInstanceCap(config.DefaultPreviewMaxConcurrent) })
	c := config.NewEventProcessingConfiguration()
	n := 3
	c.Preview.MaxConcurrent = &n
	configurePreviewCap(c)
	if got := graphql.PreviewInstanceCap(); got != 3 {
		t.Fatalf("preview cap = %d, want the configured 3", got)
	}
	configurePreviewCap(config.NewEventProcessingConfiguration())
	if got := graphql.PreviewInstanceCap(); got != config.DefaultPreviewMaxConcurrent {
		t.Fatalf("default preview cap = %d, want %d", got, config.DefaultPreviewMaxConcurrent)
	}
}

// Loading the service's configuration installs the preview cap it carries: this is the path
// startup takes, so a main that parsed the key and never applied it fails here.
func TestParseConfigurationInstallsThePreviewCap(t *testing.T) {
	oldMs, oldCfg := Microservice, Configuration
	t.Cleanup(func() {
		Microservice, Configuration = oldMs, oldCfg
		graphql.SetPreviewInstanceCap(config.DefaultPreviewMaxConcurrent)
	})
	Microservice = &core.Microservice{MicroserviceConfigurationRaw: []byte(`{"preview":{"maxConcurrent":3}}`)}
	if err := parseConfiguration(); err != nil {
		t.Fatal(err)
	}
	if got := graphql.PreviewInstanceCap(); got != 3 {
		t.Fatalf("preview cap after loading the configuration = %d, want 3", got)
	}
}
