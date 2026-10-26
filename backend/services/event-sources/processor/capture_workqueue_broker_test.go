// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/payloadredact"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// The capture stream keeps a device publish only until it has been forwarded.
//
// These tests run against a real embedded broker with the MQTT gateway and a real store
// directory, because what is claimed is the broker's behaviour: that an ack removes a
// message from a work-queue stream, and that what the broker then holds on disk no longer
// contains it.

// startGatewayWithStore is startGateway that also returns the broker's store directory, so a
// test can read the files the broker writes.
func startGatewayWithStore(t *testing.T) (nc *nats.Conn, mqttPort int, storeDir string) {
	t.Helper()
	storeDir = dctest.JetStreamStoreDir(t)
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   storeDir,
		ServerName: "capture-workqueue-test",
		MQTT:       natsserver.MQTTOpts{Host: "127.0.0.1", Port: -1},
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)
	mqttPort = dctest.MQTTPort(t, srv)
	nc, err = nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc, mqttPort, storeDir
}

// storeHolds reports every file under dir whose bytes contain needle.
func storeHolds(t *testing.T, dir string, needle string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A file the broker removes between the listing and the read is not a hit.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			if os.IsNotExist(rerr) {
				return nil
			}
			return rerr
		}
		if bytes.Contains(b, []byte(needle)) {
			rel, _ := filepath.Rel(dir, path)
			hits = append(hits, rel)
		}
		return nil
	})
	require.NoError(t, err)
	return hits
}

// canaryEvent is a device event the JSON decoder accepts, carrying the canary in a member the
// decoder ignores. It stands for "a byte the broker stores verbatim and the platform never
// looks at": what these tests measure is how long the BROKER keeps a captured publish, which
// does not depend on what the body says. (It used to carry a body credential, which the
// capture source now refuses: see credentialCanaryEvent.)
func canaryEvent(device, canary string) string {
	return fmt.Sprintf(
		`{"device":%q,"eventType":"Location","occurredTime":%q,"x-canary":%q,`+
			`"payload":{"entries":[{"latitude":"33.74900000","longitude":"-84.38800000","elevation":"320.5"}]}}`,
		device, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), canary)
}

// credentialCanaryEvent is canaryEvent carrying the canary as the device's credential secret,
// the way firmware following the old credentials guide does.
func credentialCanaryEvent(device, canary string) string {
	return fmt.Sprintf(
		`{"device":%q,"eventType":"Location","occurredTime":%q,`+
			`"credentialType":"MQTT_BASIC","credentialId":"acme:%s","credentialSecret":%q,`+
			`"payload":{"entries":[{"latitude":"33.74900000","longitude":"-84.38800000","elevation":"320.5"}]}}`,
		device, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), device, canary)
}

func newCanary(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return "CANARY-" + hex.EncodeToString(b)
}

func captureStreamName() string {
	return messaging.StreamName(testInstance, streams.DeviceEventsCapture)
}

// captureHeld is how many messages the capture stream holds.
func captureHeld(t *testing.T, nc *nats.Conn) uint64 {
	t.Helper()
	js, err := nc.JetStream()
	require.NoError(t, err)
	info, err := js.StreamInfo(captureStreamName())
	require.NoError(t, err)
	return info.State.Msgs
}

func eventually(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", within, what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// THE HEADLINE: once the source has forwarded and acked a captured publish, the broker no
// longer holds it, through the API and in its files.
//
// The first half is the control. Before the source runs, the same scan DOES find the
// canary, so a clean result afterwards means the bytes are gone and not that the scan cannot
// see them.
func TestAForwardedCapturedPublishIsGoneFromTheStreamAndTheDisk(t *testing.T) {
	nc, mqttPort, storeDir := startGatewayWithStore(t)
	const tenant, device = "acme", "sensor-001"
	topic := messaging.SubjectToMqttTopic(messaging.DeviceEventsSubject(testInstance, tenant, device))
	canary := newCanary(t)

	reader := captureReader(t, nc, "canary-consumer")
	js, err := nc.JetStream()
	require.NoError(t, err)
	info, err := js.StreamInfo(captureStreamName())
	require.NoError(t, err)
	require.Equal(t, nats.WorkQueuePolicy, info.Config.Retention, "the capture stream must be a work queue")

	publishAsDevice(t, mqttPort, "canary-device", topic, canaryEvent(device, canary))

	// CONTROL: the stored bytes contain the canary while the message is unread.
	require.EqualValues(t, 1, captureHeld(t, nc))
	eventually(t, 10*time.Second, "the broker to write the canary to its store files", func() bool {
		return len(storeHolds(t, storeDir, canary)) > 0
	})
	held := storeHolds(t, storeDir, canary)
	require.NotEmpty(t, held, "the scan could not see the canary before the source ran, so a clean scan later would prove nothing")
	t.Logf("before forwarding, the canary is in: %v", held)
	seq := firstCaptureSeq(t, js)

	_, out := startCaptureSource(t, "gw-canary", reader)
	select {
	case e := <-out:
		require.Equal(t, device, e.device)
	case <-time.After(20 * time.Second):
		t.Fatal("the source never forwarded the published event")
	}

	// The ack lands after the callback, so wait on the stream rather than on the callback.
	eventually(t, 10*time.Second, "the acked message to leave the capture stream", func() bool {
		return captureHeld(t, nc) == 0
	})
	_, err = js.GetMsg(captureStreamName(), seq)
	require.ErrorIs(t, err, nats.ErrMsgNotFound, "the forwarded message is still retrievable from the stream")

	eventually(t, 10*time.Second, "the canary to leave the broker's store files", func() bool {
		return len(storeHolds(t, storeDir, canary)) == 0
	})
	require.Empty(t, storeHolds(t, storeDir, canary))
	require.Empty(t, storeHolds(t, storeDir, "x-canary"), "no part of the body remains on disk")
}

func firstCaptureSeq(t *testing.T, js nats.JetStreamContext) uint64 {
	t.Helper()
	info, err := js.StreamInfo(captureStreamName())
	require.NoError(t, err)
	require.NotZero(t, info.State.Msgs)
	return info.State.FirstSeq
}

// Unread messages survive a source restart. Work-queue retention removes on ack only.
func TestUnreadCapturedPublishesSurviveASourceRestart(t *testing.T) {
	nc, mqttPort, _ := startGatewayWithStore(t)
	const tenant, device = "acme", "sensor-002"
	topic := messaging.SubjectToMqttTopic(messaging.DeviceEventsSubject(testInstance, tenant, device))

	reader := captureReader(t, nc, "restart-consumer")
	src, out := startCaptureSource(t, "gw-restart-1", reader)
	publishAsDevice(t, mqttPort, "restart-before", topic, canaryEvent(device, newCanary(t)))
	select {
	case <-out:
	case <-time.After(20 * time.Second):
		t.Fatal("no event before the restart")
	}
	require.NoError(t, src.Stop(context.Background()))

	const gap = 5
	for i := 0; i < gap; i++ {
		publishAsDevice(t, mqttPort, fmt.Sprintf("restart-gap-%d", i), topic, canaryEvent(device, newCanary(t)))
	}
	eventually(t, 10*time.Second, "the unread publishes to be held", func() bool { return captureHeld(t, nc) == gap })

	replacement := captureReader(t, nc, "restart-consumer")
	_, out2 := startCaptureSource(t, "gw-restart-2", replacement)
	for i := 0; i < gap; i++ {
		select {
		case <-out2:
		case <-time.After(20 * time.Second):
			t.Fatalf("only %d of %d unread publishes were delivered after the restart", i, gap)
		}
	}
	eventually(t, 10*time.Second, "the stream to empty", func() bool { return captureHeld(t, nc) == 0 })
}

// An instance whose capture stream was created before it became a work queue. With the real
// source consuming it, an unread publish is forwarded, then the stream is recreated as a work
// queue, and the next device publish flows through the same reader and is removed on ack.
func TestAnOlderCaptureStreamIsMigratedUnderARunningSource(t *testing.T) {
	nc, mqttPort, storeDir := startGatewayWithStore(t)
	const tenant, device = "acme", "sensor-003"
	topic := messaging.SubjectToMqttTopic(messaging.DeviceEventsSubject(testInstance, tenant, device))
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{
		Name:      captureStreamName(),
		Subjects:  messaging.StreamSubjects(testInstance, streams.DeviceEventsCapture),
		Retention: nats.LimitsPolicy,
		Storage:   nats.FileStorage,
	})
	require.NoError(t, err)
	before, after := newCanary(t), newCanary(t)
	publishAsDevice(t, mqttPort, "older-before", topic, canaryEvent(device, before))

	nmgr, reader := captureManagerAndReader(t, nc, "migrating-consumer")
	require.Equal(t, []string{streams.DeviceEventsCapture}, nmgr.PendingRetentionMigrations())

	_, out := startCaptureSource(t, "gw-migrating", reader)
	select {
	case e := <-out:
		require.Equal(t, device, e.device)
	case <-time.After(20 * time.Second):
		t.Fatal("the source never forwarded the backlog of the older stream")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, nmgr.MigratePendingRetention(ctx))
	info, err := js.StreamInfo(captureStreamName())
	require.NoError(t, err)
	require.Equal(t, nats.WorkQueuePolicy, info.Config.Retention)

	// The same reader carries on across the recreate, and what it forwards leaves the stream.
	publishAsDevice(t, mqttPort, "older-after", topic, canaryEvent(device, after))
	select {
	case e := <-out:
		require.Equal(t, device, e.device)
		require.Greater(t, e.seq, uint64(1), "the sequence restarted after the stream was recreated")
	case <-time.After(30 * time.Second):
		t.Fatal("the source did not carry on across the recreated stream")
	}
	eventually(t, 10*time.Second, "the forwarded message to leave the stream", func() bool { return captureHeld(t, nc) == 0 })
	eventually(t, 10*time.Second, "the bytes to leave the broker's files", func() bool {
		return len(storeHolds(t, storeDir, after)) == 0
	})
}

// S2 THROUGH A REAL BROKER. A device on the platform MQTT broker that still puts its credential
// in the body is refused, and the refusal is terminal: the capture message is acked (so the
// work-queue stream removes it and the broker's files stop holding the secret), nothing is
// forwarded, and the record that goes to failed-decode carries no secret.
//
// The first half is the control: the same scan finds the secret while the message is unread,
// so a clean scan afterwards means the bytes are gone and not that the scan cannot see them.
// Against the code before the refusal this test is red: the event is forwarded.
func TestACapturedBodyCredentialIsRefusedAndLeavesNothingOnTheBroker(t *testing.T) {
	nc, mqttPort, storeDir := startGatewayWithStore(t)
	const tenant, device = "acme", "sensor-004"
	topic := messaging.SubjectToMqttTopic(messaging.DeviceEventsSubject(testInstance, tenant, device))
	canary := newCanary(t)

	reader := captureReader(t, nc, "refusal-consumer")
	publishAsDevice(t, mqttPort, "refusal-device", topic, credentialCanaryEvent(device, canary))

	require.EqualValues(t, 1, captureHeld(t, nc))
	eventually(t, 10*time.Second, "the broker to write the secret to its store files", func() bool {
		return len(storeHolds(t, storeDir, canary)) > 0
	})
	require.NotEmpty(t, storeHolds(t, storeDir, canary), "the scan must see the secret before the source runs")

	type refusal struct {
		payload []byte
		err     error
	}
	refused := make(chan refusal, 4)
	_, out := startCaptureSourceFailing(t, "gw-refusal", reader,
		func(_ string, _ string, payload []byte, err error) error {
			refused <- refusal{append([]byte(nil), payload...), err}
			return nil
		})
	var got refusal
	select {
	case got = <-refused:
	case e := <-out:
		t.Fatalf("a body credential on the platform broker was forwarded as an event for %s", e.device)
	case <-time.After(20 * time.Second):
		t.Fatal("the source neither forwarded nor refused the published event")
	}
	require.ErrorIs(t, got.err, model.ErrCredentialInMqttBody)
	require.NotContains(t, got.err.Error(), canary)
	// What the service writes to failed-decode is this payload through payloadredact.Redact.
	require.NotContains(t, string(payloadredact.Redact(got.payload, payloadredact.DiagnosticMaxBytes)), canary)

	// Terminal: acked, so the work queue removes it and the broker's files stop holding it.
	eventually(t, 10*time.Second, "the refused message to leave the capture stream", func() bool {
		return captureHeld(t, nc) == 0
	})
	eventually(t, 10*time.Second, "the secret to leave the broker's store files", func() bool {
		return len(storeHolds(t, storeDir, canary)) == 0
	})
	select {
	case e := <-out:
		t.Fatalf("the refused event was forwarded after all, for %s", e.device)
	case <-time.After(300 * time.Millisecond):
	}
}
