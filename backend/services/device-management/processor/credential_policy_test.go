// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// The policy's numbers are PUBLISHED, so they are pinned here as literals rather than
// read back from the variable: every other test in this package takes Free and Base
// from DeviceCredentialPolicy, so a changed number passes all of them. The places that
// quote these values and go stale with them:
//   - docs/docs/guides/device-credentials.md and its docs/i18n/es twin (10 free, 1 s, 30 s);
//   - the release note under {#next-upgrade} in releases-and-upgrades.md, both locales;
//   - the tip in docs/docs/guides/connecting-a-device.md, both locales;
//   - the DeviceCredentialAttemptStoreFull description in
//     deploy/helm/devicechain/templates/prometheusrule-sign-in.yaml ("at most 30 seconds").
//
// Changing the policy is legitimate; changing it without those is not. Update them and
// this literal together.
func TestDeviceCredentialPolicyIsThePublishedOne(t *testing.T) {
	want := credential.Policy{Free: 10, Base: time.Second, Cap: 30 * time.Second}
	if DeviceCredentialPolicy != want {
		t.Fatalf("DeviceCredentialPolicy = %+v, the docs and the alert text publish %+v",
			DeviceCredentialPolicy, want)
	}
	if got := DeviceCredentialPolicies[credential.KindDeviceCredential]; got != want {
		t.Fatalf("the callout's Checker is built with %+v for device credentials, want %+v", got, want)
	}
	if len(DeviceCredentialPolicies) != 1 {
		t.Fatalf("DeviceCredentialPolicies declares %d kinds, want only %q",
			len(DeviceCredentialPolicies), credential.KindDeviceCredential)
	}
}

// The device Checker keeps its backoff in the DEVICE credential-attempt bucket and
// never touches the one people's sign-in backoff lives in. Both buckets are one method
// call apart on the same NatsManager, so the wrong one compiles, starts and throttles
// just as well; the only difference is which bucket the records land in, which is what
// this asserts, against a real JetStream.
func TestDeviceCredentialCheckerUsesTheDeviceBucket(t *testing.T) {
	const instance = "inst-creds"
	nmgr := startCheckerNats(t, instance)

	checker, err := NewDeviceCredentialChecker(nmgr)
	if err != nil {
		t.Fatalf("NewDeviceCredentialChecker: %v", err)
	}
	p := credential.Principal{Kind: credential.KindDeviceCredential, ID: "acme:sensor-001"}
	err = checker.Check(context.Background(), p, "wrong", func(context.Context) (string, error) {
		return "right", nil
	})
	if err == nil {
		t.Fatal("a wrong password was accepted")
	}

	js, err := nmgr.Conn().JetStream()
	if err != nil {
		t.Fatal(err)
	}
	device, err := js.KeyValue(messaging.DeviceCredentialAttemptsBucketName(instance))
	if err != nil {
		t.Fatalf("the device credential-attempt bucket %q was not created: %v",
			messaging.DeviceCredentialAttemptsBucketName(instance), err)
	}
	if _, err := device.Get(credential.Key(p)); err != nil {
		t.Fatalf("the failed attempt was not recorded in the device bucket: %v", err)
	}
	people := messaging.CredentialAttemptsBucketName(instance)
	if _, err := js.KeyValue(people); !errors.Is(err, nats.ErrBucketNotFound) {
		t.Fatalf("the device Checker reached people's credential-attempt bucket %q (err=%v): a "+
			"spray of MQTT usernames that filled it would switch off the sign-in backoff", people, err)
	}
}

// startCheckerNats runs an in-process JetStream server and a started NatsManager on it.
func startCheckerNats(t *testing.T, instance string) *messaging.NatsManager {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	t.Cleanup(srv.Shutdown)
	u, err := url.Parse(srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	ms := &core.Microservice{InstanceId: instance, FunctionalArea: "device-management",
		Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: u.Hostname(), Port: uint32(port)}
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	if err := nmgr.Initialize(context.Background()); err != nil {
		t.Fatal(fmt.Errorf("initialize nats manager: %w", err))
	}
	if err := nmgr.Start(context.Background()); err != nil {
		t.Fatal(fmt.Errorf("start nats manager: %w", err))
	}
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	return nmgr
}
