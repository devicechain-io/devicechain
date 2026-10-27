// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/downlink"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

// The device-commands durable MUST start at the stream tail: one created without it replays the
// stream's retained history and publishes already-expired commands to nodes as live actuations.
// The policy is read off the broker's own consumer, so dropping the option from newCommandReader
// fails here rather than at a customer's plant.
func TestCommandReaderStartsAtTheStreamTail(t *testing.T) {
	startRunningManager(t)

	reader, err := newCommandReader(NatsManager)
	require.NoError(t, err)
	require.NotNil(t, reader)

	js, err := NatsManager.Conn().JetStream()
	require.NoError(t, err)
	var policies []nats.DeliverPolicy
	for name := range js.StreamNames() {
		for info := range js.ConsumersInfo(name) {
			policies = append(policies, info.Config.DeliverPolicy)
		}
	}
	require.Len(t, policies, 1, "the reader must create exactly one durable consumer")
	require.Equal(t, nats.DeliverNewPolicy, policies[0],
		"the command reader's durable must start at the stream tail, not replay the retained backlog")
}

// The awaiting set lives in a real KV bucket, under the key layout the tenant purge decodes, and
// round-trips the entry.
func TestTheAwaitingSetIsWrittenToItsBucketUnderTheTenantKeyLayout(t *testing.T) {
	startRunningManager(t)
	bucket, err := NatsManager.SparkplugCommandsAwaitingStore()
	require.NoError(t, err)
	store := downlink.NewKVAwaitingStore(bucket)

	alias := uint64(9)
	entry := downlink.Awaiting{
		Command: "cmd-1", Device: "pump-1", Nonce: "n-1", Kind: downlink.KindWrite, Metric: "Setpoint", Alias: &alias,
		Requested: json.RawMessage(`5`), Datatype: 1, SessionEpoch: 3, BdSeq: 7, HasBdSeq: true, HostRole: "primary",
		PublishedAt: time.Unix(1_700_000_000, 0).UTC(), Deadline: time.Unix(1_700_000_120, 0).UTC(),
		Wire: downlink.AwaitingWire{Topic: "spBv1.0/g/NCMD/n", Alias: &alias, Datatype: 1, SHA256: "ab", Size: 3},
	}
	key := downlink.AwaitingKey("acme", "cmd-1")
	require.NoError(t, store.Put(context.Background(), key, entry))

	raw, err := bucket.Get(base64.RawURLEncoding.EncodeToString([]byte("acme|cmd-1")))
	require.NoError(t, err, "the entry is not under base64url(tenant|command), the layout a tenant purge scans")
	var got downlink.Awaiting
	require.NoError(t, json.Unmarshal(raw.Value(), &got))
	assert.Equal(t, entry, got)

	require.NoError(t, store.Delete(context.Background(), key))
	_, err = bucket.Get(base64.RawURLEncoding.EncodeToString([]byte("acme|cmd-1")))
	assert.ErrorIs(t, err, nats.ErrKeyNotFound)
	assert.NoError(t, store.Delete(context.Background(), key), "deleting an absent entry is not an error")
}

func TestCommandDeliveryEndpointFailsClosed(t *testing.T) {
	full := mscfg.InfrastructureConfiguration{
		ServiceAuth:     mscfg.ServiceAuthConfiguration{Secret: "s3cret"},
		CommandDelivery: mscfg.CommandDeliveryConfiguration{Hostname: "command-delivery", Port: 8080},
	}
	url, err := commandDeliveryEndpoint(full)
	require.NoError(t, err)
	assert.Equal(t, "http://command-delivery:8080/graphql", url)

	noSecret := full
	noSecret.ServiceAuth.Secret = ""
	_, err = commandDeliveryEndpoint(noSecret)
	require.Error(t, err, "no service secret must fail closed")

	noCD := full
	noCD.CommandDelivery = mscfg.CommandDeliveryConfiguration{}
	_, err = commandDeliveryEndpoint(noCD)
	require.Error(t, err, "a service with commands enabled and no command-delivery coordinate must not start")
}

// A source that enables commands but has no ingest path could not identify the devices it commands,
// and fails the load rather than starting a dispatcher that can address nothing. The paired case is
// the same source with an ingest path, which builds.
func TestResolveSourcesRefusesCommandsWithoutAnIngestPath(t *testing.T) {
	cmdSrc := src("tcp://broker.example:1883")
	cmdSrc.Commands = config.SourceCommands{Enabled: true, HostRole: config.HostRolePrimary}
	cfg := &config.SparkplugConfiguration{Sources: []config.SparkplugSource{cmdSrc}}
	cfg.ApplyDefaults()

	_, err := resolveSources(cfg, "inst", nil, nil, nil, host.Metrics{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commands are enabled")

	off := &config.SparkplugConfiguration{Sources: []config.SparkplugSource{src("tcp://broker.example:1883")}}
	off.ApplyDefaults()
	_, err = resolveSources(off, "inst", nil, nil, nil, host.Metrics{}, nil)
	require.NoError(t, err, "a source without commands must still build with no ingest path")
}

// With no dispatcher (no source enables commands) a term runs exactly as it always did.
func TestATermWithoutADispatcherStartsNothing(t *testing.T) {
	prev := Dispatcher
	Dispatcher = nil
	t.Cleanup(func() { Dispatcher = prev })

	cancel, done := startDispatcher(context.Background())
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a term with no dispatcher waited for one")
	}
}

// The dispatcher is READY before startDispatcher returns (so a source connecting right after finds a
// shard for its first birth's wake), and the cancel it returns ends it.
func TestADispatcherIsReadyWhenTheTermStartsAndStopsWithIt(t *testing.T) {
	prev := Dispatcher
	t.Cleanup(func() { Dispatcher = prev })
	Dispatcher = newBareDispatcher(t)

	cancel, done := startDispatcher(context.Background())
	// Ready: a wake now reaches a shard rather than being dropped on a missing term.
	Dispatcher.Drain("acme", "pump-1") // a term is running: queued on a shard, not a panic
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the dispatcher did not stop with its term")
	}
}

type nopResponses struct{}

func (nopResponses) WriteToDevice(context.Context, string, ...messaging.Message) error { return nil }

type nopStore struct{}

func (nopStore) Put(context.Context, string, downlink.Awaiting) error { return nil }
func (nopStore) Delete(context.Context, string) error                 { return nil }
func (nopStore) List(context.Context) (downlink.Stored, error)        { return downlink.Stored{}, nil }
func (nopStore) PutReboot(context.Context, string, string, string, time.Time) error {
	return nil
}
func (nopStore) DeleteReboot(context.Context, string, string, string) error { return nil }

// newBareDispatcher builds a dispatcher over no sources and no reader: it serves a term (workers,
// ready, stop) and does nothing else.
func newBareDispatcher(t *testing.T) *downlink.Dispatcher {
	t.Helper()
	return downlink.New(nil, nopResponses{}, cmddownlink.NewFetcher(nil, "", "sparkplug"),
		cmddownlink.NewClaimer(nil, ""), cmddownlink.NewParker(nil, ""), nopStore{}, nil, downlink.Metrics{}, downlink.Options{})
}

// buildDispatcher assembles the dispatcher as the service does, over real broker plumbing, and
// fails closed when the pieces it shares with the ingest path are missing.
func TestBuildDispatcherAssemblesTheCommandPath(t *testing.T) {
	startRunningManager(t)
	infra := &Microservice.InstanceConfiguration.Infrastructure
	infra.ServiceAuth.Secret = "s3cret"
	infra.CommandDelivery = mscfg.CommandDeliveryConfiguration{Hostname: "command-delivery", Port: 8080}
	infra.UserManagement = mscfg.UserManagementConfiguration{Hostname: "user-management", Port: 8080}

	prevR, prevG, prevReader, prevResp := commandRegistrar, tenantGate, CommandReader, ResponseWriter
	t.Cleanup(func() {
		commandRegistrar, tenantGate, CommandReader, ResponseWriter = prevR, prevG, prevReader, prevResp
	})

	cmdSrc := src("tcp://broker.example:1883")
	cmdSrc.Commands = config.SourceCommands{Enabled: true, HostRole: config.HostRolePrimary}
	cfg := &config.SparkplugConfiguration{Sources: []config.SparkplugSource{cmdSrc}}
	cfg.ApplyDefaults()
	client := host.NewClient(cfg.Sources[0], host.Broker{URL: cmdSrc.Broker.URL}, nil, nil, time.Now, host.Metrics{})

	// Without the ingest path's registrar and tenant gate there is nothing to learn tokens from.
	commandRegistrar, tenantGate = nil, nil
	_, err := buildDispatcher([]*host.Client{client}, downlink.Metrics{})
	require.Error(t, err, "a dispatcher was built with no registrar to identify devices with")

	commandRegistrar = host.NewRegistrar(nil, "http://device-management:8080/graphql", nil)
	tenantGate = func(string) bool { return false }
	d, err := buildDispatcher([]*host.Client{client}, downlink.Metrics{})
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.NotNil(t, CommandReader, "the device-commands reader was not built")
	assert.NotNil(t, ResponseWriter, "the command-responses writer was not built")

	infra.CommandDelivery = mscfg.CommandDeliveryConfiguration{}
	_, err = buildDispatcher([]*host.Client{client}, downlink.Metrics{})
	require.Error(t, err, "a dispatcher was built with no command-delivery to claim against")
}
