// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func TestS6EmptyPullAndUnexpectedStatuses(t *testing.T) {
	for _, status := range []int{204, 201, 202, 404, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := harborWith(t)
			d := DeviceInstance{Token: HarborpulsePullLights()[0]}
			s.s6.dims = map[string]float64{d.Token: 100}
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet {
					t.Error("empty poll caused an ACK or response")
				}
				w.WriteHeader(status)
			}))
			defer srv.Close()
			rt := &Runtime{HTTPClient: srv.Client(), Endpoints: Endpoints{Ingress: srv.URL}}
			err := s.pollS6Lamp(context.Background(), rt, d)
			if status == 204 && err != nil {
				t.Fatalf("normal empty poll failed: %v", err)
			}
			if status != 204 && (err == nil || !strings.Contains(err.Error(), "status")) {
				t.Fatalf("unexpected status %d was hidden: %v", status, err)
			}
			if requests != 1 || s.s6.dims[d.Token] != 100 || len(s.s6.pending[d.Token]) != 0 {
				t.Fatal("empty poll changed device state")
			}
		})
	}
}

func TestS6RevokedConnectSuccessStillFails(t *testing.T) {
	s := harborWith(t)
	s.s6.initialized, s.s6.configured, s.s6.revoked = true, true, true
	device := DeviceInstance{Token: HarborpulseRevokedCharger(), DeviceTypeToken: HarborpulseX509ChargerType}
	client := &s6WireClient{}
	s.s6.clients = map[string]mqtt.Client{device.Token: client}
	rt := &Runtime{Devices: []DeviceInstance{device}}
	if err := s.tickS6(context.Background(), rt, time.Now(), 10*time.Minute); err == nil {
		t.Fatal("revoked charger connected successfully")
	}
	if len(client.topics) != 0 {
		t.Fatal("revoked charger published telemetry")
	}
}
