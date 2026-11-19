// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unansweredRoster stands in for a broker that does not answer: every publish waits out
// wait (the real writer's ceiling is 5 s) and is refused. It counts the attempts.
type unansweredRoster struct {
	wait     time.Duration
	attempts int
}

func (u *unansweredRoster) PublishDeviceRoster(context.Context, *DeviceRosterEvent) error {
	u.attempts++
	time.Sleep(u.wait)
	return errors.New("nats: timeout")
}

// seedRosterFanout creates a type adopting profile-a with n devices, and profile-b to re-point it at.
func seedRosterFanout(t *testing.T, api *Api, ctx context.Context, n int) {
	t.Helper()
	seedType(t, api, ctx, "sensor", "profile-a")
	_, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "profile-b"})
	require.NoError(t, err)
	for i := range n {
		_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: fmt.Sprintf("d%02d", i), DeviceTypeToken: "sensor"})
		require.NoError(t, err)
	}
}

// A type re-point made while the broker does not answer used to publish one roster fact per
// device, each waiting out the publish ceiling: 5 s times the device count before the mutation
// answered. It now stops at the first refusal, so the re-point answers after ONE wait, and the
// devices it did not announce are left to the detection engine's reconcile.
func TestATypeRepointStopsAnnouncingTheRosterAtTheFirstRefusedPublish(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedRosterFanout(t, api, ctx, 20)

	down := &unansweredRoster{wait: 50 * time.Millisecond}
	api.DeviceRosterPublisher = down
	started := time.Now()
	_, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{ProfileToken: dcgraphql.OptionalStringOf("profile-b")})
	took := time.Since(started)
	require.NoError(t, err, "the re-point committed; a refused announcement must not fail it")
	assert.Equal(t, 1, down.attempts, "the fan-out kept publishing after the broker refused")
	assert.Less(t, took, 10*down.wait, "the re-point waited out a publish per device")

}

// The same bound for a bulk create: its roster announcement stops at the first refusal, across
// read-back chunks too.
func TestABulkCreateStopsAnnouncingTheRosterAtTheFirstRefusedPublish(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedType(t, api, ctx, "sensor", "profile-a")

	down := &unansweredRoster{}
	api.DeviceRosterPublisher = down
	requests := make([]*DeviceCreateRequest, 0, rosterEmitChunk+3)
	for i := range rosterEmitChunk + 3 {
		requests = append(requests, &DeviceCreateRequest{Token: fmt.Sprintf("bulk-%04d", i), DeviceTypeToken: "sensor"})
	}
	created, err := api.CreateDevices(ctx, requests)
	require.NoError(t, err)
	assert.Len(t, created, rosterEmitChunk+3)
	assert.Equal(t, 1, down.attempts, "the bulk create kept announcing devices after the broker refused")
}

// The counterweight: a healthy broker is still told about every device of a re-pointed type.
func TestATypeRepointStillAnnouncesEveryDeviceWhenTheBrokerAnswers(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedRosterFanout(t, api, ctx, 20)

	cap := &captureRoster{}
	api.DeviceRosterPublisher = cap
	_, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{ProfileToken: dcgraphql.OptionalStringOf("profile-b")})
	require.NoError(t, err)
	assert.Len(t, cap.events, 20)
}
