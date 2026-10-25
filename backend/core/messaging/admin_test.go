// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/streams"
)

// WHO MAY SHAPE THE BROKER: the administrator creates and reconciles, a deployed service only
// verifies. Each claim runs against the embedded server.

// adminOn builds and connects the broker administrator's manager over srv.
func adminOn(t *testing.T, srv *natsserver.Server) *NatsManager {
	t.Helper()
	ms := testMicroservice(t, srv, "ignored")
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	// Small ceilings: the administrator can create every stream of every area, and a test
	// machine's disk is not a production volume.
	n := &ms.InstanceConfiguration.Infrastructure.Nats
	n.StreamMaxBytes, n.StreamMaxBytesCold, n.KvStateMaxBytes, n.KvCacheMaxBytes = 16<<20, 4<<20, 4<<20, 2<<20
	admin := NewBrokerAdmin(ms)
	require.NoError(t, admin.ExecuteInitialize(context.Background()))
	t.Cleanup(admin.nc.Close)
	return admin
}

// deployedOn builds and connects a manager over a DEPLOYED microservice of area.
func deployedOn(t *testing.T, srv *natsserver.Server, area string) *NatsManager {
	t.Helper()
	ms := testMicroservice(t, srv, area)
	ms.Deployed = true
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	require.NoError(t, nmgr.Initialize(context.Background()))
	t.Cleanup(nmgr.nc.Close)
	return nmgr
}

func drift(t *testing.T, err error) *BrokerDriftError {
	t.Helper()
	var d *BrokerDriftError
	require.True(t, errors.As(err, &d), "expected a BrokerDriftError, got %v", err)
	return d
}

func TestADeployedServiceDoesNotCreateWhatTheAdministratorHasNot(t *testing.T) {
	srv := startEmbeddedServer(t)
	svc := deployedOn(t, srv, "event-sources")

	_, err := svc.NewWriter(streams.InboundEvents)
	d := drift(t, err)
	require.Equal(t, "stream", d.Kind)
	require.Equal(t, StreamName("test", streams.InboundEvents), d.Name)
	require.Contains(t, d.Error(), "does not exist")
	require.Contains(t, d.Error(), "dcctl broker reconcile", "the error must say what to run")

	_, err = svc.js.StreamInfo(StreamName("test", streams.InboundEvents))
	require.ErrorIs(t, err, nats.ErrStreamNotFound, "the service created the stream it was only to verify")

	// A bucket, the same way.
	_, err = svc.NewDistributedLease(30 * time.Second)
	d = drift(t, err)
	require.Equal(t, "KV bucket", d.Kind)
	require.Contains(t, d.Error(), "does not exist")
	_, err = svc.js.StreamInfo(kvStreamPrefix + LeaseBucketName("test"))
	require.ErrorIs(t, err, nats.ErrStreamNotFound, "the service created a bucket it was only to verify")
}

func TestOnceTheAdministratorHasReconciledTheServiceStarts(t *testing.T) {
	srv := startEmbeddedServer(t)
	admin := adminOn(t, srv)
	report, err := admin.ReconcileBroker(context.Background(), []string{"event-sources"})
	require.NoError(t, err)
	require.Positive(t, report.Count("created"))

	svc := deployedOn(t, srv, "event-sources")
	w, err := svc.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	require.NotNil(t, w)
	lease, err := svc.NewDistributedLease(DefaultLeaseTTL)
	require.NoError(t, err)
	require.NotNil(t, lease)
}

// The administrator creates what the deployed areas open and nothing of the rest, so an
// installation with fewer areas has fewer streams.
func TestTheAdministratorCreatesOnlyWhatTheDeployedAreasOpen(t *testing.T) {
	srv := startEmbeddedServer(t)
	admin := adminOn(t, srv)
	_, err := admin.ReconcileBroker(context.Background(), []string{"command-delivery"})
	require.NoError(t, err)

	_, err = admin.js.StreamInfo(StreamName("test", streams.DeviceCommands))
	require.NoError(t, err, "command-delivery opens device-commands")
	_, err = admin.js.StreamInfo(StreamName("test", streams.ResolvedEvents))
	require.ErrorIs(t, err, nats.ErrStreamNotFound, "no deployed area opens resolved-events")
	_, err = admin.js.StreamInfo(kvStreamPrefix + LeaseBucketName("test"))
	require.ErrorIs(t, err, nats.ErrStreamNotFound, "no deployed area holds a lease")
}

func TestReconcilingTwiceChangesNothing(t *testing.T) {
	srv := startEmbeddedServer(t)
	admin := adminOn(t, srv)
	areas := []string{"event-sources", "device-management", "user-management", "event-processing"}
	first, err := admin.ReconcileBroker(context.Background(), areas)
	require.NoError(t, err)
	require.Positive(t, first.Count("created"))

	before := map[string]nats.StreamConfig{}
	for _, a := range first.Actions {
		name := a.Name
		if a.Kind == "bucket" {
			name = kvStreamPrefix + a.Name
		}
		info, err := admin.js.StreamInfo(name)
		require.NoError(t, err, name)
		before[name] = info.Config
	}

	second, err := admin.ReconcileBroker(context.Background(), areas)
	require.NoError(t, err)
	require.Zero(t, second.Count("created"), "a second pass created something: %v", second.Actions)
	require.Zero(t, second.Count("updated"), "a second pass updated something: %v", second.Actions)
	require.Zero(t, second.Count("migrated"))
	require.Len(t, second.Actions, len(first.Actions), "the two passes covered different streams and buckets")
	for name, cfg := range before {
		info, err := admin.js.StreamInfo(name)
		require.NoError(t, err, name)
		require.Equal(t, cfg, info.Config, "%s changed on a repeat pass", name)
	}
}

func TestOnlyTheAdministratorReconciles(t *testing.T) {
	srv := startEmbeddedServer(t)
	svc := deployedOn(t, srv, "event-sources")
	_, err := svc.ReconcileBroker(context.Background(), []string{"event-sources"})
	require.Error(t, err)

	admin := adminOn(t, srv)
	_, err = admin.ReconcileBroker(context.Background(), []string{"no-such-area"})
	require.Error(t, err, "an area that does not exist is a typo, not an empty set")
}

// Drift is named, whichever way the stream was changed, and the administrator puts it back.
func TestADriftedStreamIsRefusedByNameAndTheAdministratorRestoresIt(t *testing.T) {
	srv := startEmbeddedServer(t)
	admin := adminOn(t, srv)
	_, err := admin.ReconcileBroker(context.Background(), []string{"event-sources"})
	require.NoError(t, err)
	name := StreamName("test", streams.InboundEvents)
	// A stream outside the catalog for the "sources" case to copy from.
	_, err = admin.js.AddStream(&nats.StreamConfig{Name: "elsewhere", Subjects: []string{"elsewhere.>"}})
	require.NoError(t, err)

	for label, mutate := range map[string]func(*nats.StreamConfig){
		"sources": func(c *nats.StreamConfig) {
			c.Sources = []*nats.StreamSource{{Name: "elsewhere"}}
		},
		"republish": func(c *nats.StreamConfig) {
			c.RePublish = &nats.RePublish{Source: ">", Destination: "forged.>"}
		},
		"subject transform": func(c *nats.StreamConfig) {
			c.SubjectTransform = &nats.SubjectTransformConfig{Source: ">", Destination: "forged.>"}
		},
		"extra subject": func(c *nats.StreamConfig) { c.Subjects = append(c.Subjects, "somewhere.else") },
		"discard new":   func(c *nats.StreamConfig) { c.Discard = nats.DiscardNew },
	} {
		t.Run(label, func(t *testing.T) {
			info, err := admin.js.StreamInfo(name)
			require.NoError(t, err)
			cfg := info.Config
			mutate(&cfg)
			_, err = admin.js.UpdateStream(&cfg)
			require.NoError(t, err, "the setup could not drift the stream")

			svc := deployedOn(t, srv, "event-sources")
			_, err = svc.NewWriter(streams.InboundEvents)
			d := drift(t, err)
			require.Equal(t, name, d.Name)
			require.NotEmpty(t, d.Problems)

			report, err := admin.ReconcileBroker(context.Background(), []string{"event-sources"})
			require.NoError(t, err)
			require.Positive(t, report.Count("updated"), "the administrator did not put the stream back")
			_, err = deployedOn(t, srv, "event-sources").NewWriter(streams.InboundEvents)
			require.NoError(t, err, "the stream still differs after the administrator reconciled it")
		})
	}
}

// The streams read by sequence answer direct gets: a stream that has lost AllowDirect is drift a
// service names, and the administrator turns it back on (the replay and preview cannot read without
// it). The administrator only ever turns it on.
func TestAStreamThatLostDirectGetsIsDriftTheAdministratorRestores(t *testing.T) {
	srv := startEmbeddedServer(t)
	admin := adminOn(t, srv)
	_, err := admin.ReconcileBroker(context.Background(), []string{"device-management"})
	require.NoError(t, err)
	name := StreamName("test", streams.ResolvedEvents)
	info, err := admin.js.StreamInfo(name)
	require.NoError(t, err)
	require.True(t, info.Config.AllowDirect, "the administrator creates the stream answering direct gets")

	cfg := info.Config
	cfg.AllowDirect = false
	_, err = admin.js.UpdateStream(&cfg)
	require.NoError(t, err, "the setup could not drift the stream")

	_, err = deployedOn(t, srv, "device-management").NewWriter(streams.ResolvedEvents)
	d := drift(t, err)
	require.Equal(t, name, d.Name)
	require.Contains(t, strings.Join(d.Problems, "; "), "direct gets")

	report, err := admin.ReconcileBroker(context.Background(), []string{"device-management"})
	require.NoError(t, err)
	require.Positive(t, report.Count("updated"))
	info, err = admin.js.StreamInfo(name)
	require.NoError(t, err)
	require.True(t, info.Config.AllowDirect, "the administrator did not turn direct gets back on")
	_, err = deployedOn(t, srv, "device-management").NewWriter(streams.ResolvedEvents)
	require.NoError(t, err)
}

func TestADriftedBucketIsRefusedByName(t *testing.T) {
	srv := startEmbeddedServer(t)
	admin := adminOn(t, srv)
	_, err := admin.ReconcileBroker(context.Background(), []string{"device-management"})
	require.NoError(t, err)

	bucket := CacheBucketName("test", "device-management", kv.BucketDeviceByToken)
	info, err := admin.js.StreamInfo(kvStreamPrefix + bucket)
	require.NoError(t, err)
	cfg := info.Config
	cfg.RePublish = &nats.RePublish{Source: ">", Destination: "forged.>"}
	_, err = admin.js.UpdateStream(&cfg)
	require.NoError(t, err)

	svc := deployedOn(t, srv, "device-management")
	_, err = svc.NewCache(kv.BucketDeviceByToken, time.Minute)
	d := drift(t, err)
	require.Equal(t, "KV bucket", d.Kind)
	require.Equal(t, bucket, d.Name)
}

// A manager that is not over a deployed microservice (a test's) still builds what it opens, so
// the many tests that open a writer on a fresh broker are not each a two-step.
func TestAManagerThatIsNotDeployedStillCreatesWhatItOpens(t *testing.T) {
	srv := startEmbeddedServer(t)
	ms := testMicroservice(t, srv, uniqueArea("fixture"))
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	require.NoError(t, nmgr.Initialize(context.Background()))
	t.Cleanup(nmgr.nc.Close)
	_, err := nmgr.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	_, err = nmgr.js.StreamInfo(StreamName("test", streams.InboundEvents))
	require.NoError(t, err)
}

// The bucket lifetimes the administrator creates with are the ones their users used to ask for.
func TestTheCatalogLifetimesMatchTheirUsers(t *testing.T) {
	lookup := func(name string) time.Duration {
		b, ok := kv.Lookup(name)
		require.True(t, ok, name)
		return b.TTL
	}
	require.Equal(t, DefaultLeaseTTL, lookup(kv.BucketLeases), "the lease bucket's lifetime is the fencing window")
	require.Equal(t, credential.AttemptTTL, lookup(kv.BucketDeviceCredentialAttempts))
	require.Equal(t, credential.AttemptTTL, lookup(kv.BucketCredentialAttempts))
	require.Equal(t, auth.DefaultRefreshTTL, lookup(kv.BucketRefreshTokens))
	// Every bucket, the identity account's included: without that account the administrator
	// creates the sign-in buckets in the shared one (user-management's own lifetimes are pinned
	// against these in user-management, which this package cannot import).
	for _, b := range kv.All {
		require.Positive(t, b.TTL, "bucket %s has no lifetime for the administrator to create it with", b.Name)
	}
}

// On a broker without the identity account (a chart-only install brings its own broker, and the
// chart's default document says it has none), the sign-in buckets live in the shared account and
// user-management only verifies them, so the administrator is their creator and has to give them
// their lifetimes. A sign-in bucket created with none keeps every OAuth code redeemable and every
// failed sign-in charged forever, until the bucket fills; a full attempt bucket fails OPEN.
func TestWithoutTheIdentityAccountTheSignInBucketsAreCreatedWithTheirLifetimes(t *testing.T) {
	srv := startEmbeddedServer(t)
	admin := adminOn(t, srv)
	require.False(t, admin.identityRequired(), "this test is about a broker without the identity account")
	_, err := admin.ReconcileBroker(context.Background(), []string{"user-management"})
	require.NoError(t, err)

	for _, b := range kv.All {
		if b.Account != kv.Identity {
			continue
		}
		name := identityBucketName("test", b.Name)
		store, err := admin.js.KeyValue(name)
		require.NoError(t, err, "the administrator did not create sign-in bucket %s", name)
		status, err := store.Status()
		require.NoError(t, err)
		require.Positive(t, status.TTL(), "sign-in bucket %s was created with no lifetime", name)
		require.Equal(t, b.TTL, status.TTL(), "sign-in bucket %s", name)
	}
}

// The administrator is the only holder of the retention migration: it waits for the area that
// reads the old stream to drain it, then recreates it as declared.
func TestTheAdministratorMigratesAnOlderCaptureStreamOnceItsReaderHasDrainedIt(t *testing.T) {
	srv := startEmbeddedServer(t)
	// The capture stream as an older release left it, with a backlog, read by event-sources.
	ms := testMicroservice(t, srv, "event-sources")
	var reader MessageReader
	reading := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(n *NatsManager) error {
		r, err := n.NewReader(captureSuffix)
		reader = r
		return err
	})
	reading.RecordMaxDeliveries(recordNothing)
	ctx := context.Background()
	require.NoError(t, reading.Initialize(ctx))
	_, err := reading.js.AddStream(&nats.StreamConfig{
		Name:      captureStreamOn("test"),
		Subjects:  StreamSubjects("test", captureSuffix),
		Retention: nats.LimitsPolicy,
		Storage:   nats.FileStorage,
	})
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		publishCaptured(t, reading.js, "preloaded")
	}
	reading.SetAckWaitForTesting(t, time.Second)
	require.NoError(t, reading.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = reading.Stop(stopCtx)
	})
	seen := consumeCaptured(t, reader)

	admin := adminOn(t, srv)
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	report, err := admin.ReconcileBroker(rctx, []string{"event-sources"})
	require.NoError(t, err)
	require.Equal(t, 1, report.Count("migrated"), "actions: %v", report.Actions)
	require.Len(t, seen(), 3, "the backlog was not forwarded before the stream was recreated")

	info, err := admin.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, err)
	require.Equal(t, nats.WorkQueuePolicy, info.Config.Retention)
}

// An undrained stream blocks the administrator, loudly, and is left as it was: the hook fails
// and the upgrade stops, rather than a service running on a stream that does not behave as declared.
func TestTheAdministratorRefusesToRecreateAStreamNobodyHasDrained(t *testing.T) {
	srv := startEmbeddedServer(t)
	admin := adminOn(t, srv)
	_, err := admin.js.AddStream(&nats.StreamConfig{
		Name:      captureStreamOn("test"),
		Subjects:  StreamSubjects("test", captureSuffix),
		Retention: nats.LimitsPolicy,
		Storage:   nats.FileStorage,
	})
	require.NoError(t, err)
	publishCaptured(t, admin.js, "unread")
	admin.retentionDrainBound = 600 * time.Millisecond
	admin.retentionDrainPoll = 20 * time.Millisecond

	_, err = admin.ReconcileBroker(context.Background(), []string{"event-sources"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to start")
	info, serr := admin.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, serr)
	require.Equal(t, nats.LimitsPolicy, info.Config.Retention, "an undrained stream was recreated")
	require.EqualValues(t, 1, info.State.Msgs)
}
