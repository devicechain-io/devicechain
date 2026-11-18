// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenariosink

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/lorawan"
	"github.com/devicechain-io/dc-simulator/sim"
)

// QueuedDownlink is the network server's durable-for-this-run receipt, not the sim's account.
type QueuedDownlink struct {
	At        time.Time `json:"at"`
	Tenant    string    `json:"tenant"`
	ID        string    `json:"id"`
	DevEUI    string    `json:"devEui"`
	FPort     int       `json:"fPort"`
	Hex       string    `json:"hex"`
	Confirmed bool      `json:"confirmed"`
}

type queueLog struct {
	sink   *Sink
	tenant string
}

func (l queueLog) Write(b []byte) (int, error) {
	var event struct {
		Event           string
		ID, DevEUI, Hex string
		FPort           int
		Confirmed       bool
	}
	if err := json.Unmarshal(b, &event); err != nil {
		return 0, err
	}
	if event.Event == "downlink_queued" {
		s := l.sink
		s.mu.Lock()
		defer s.mu.Unlock()
		s.rec.QueueItems = bounded(s.rec.QueueItems, QueuedDownlink{s.now().UTC(), l.tenant, event.ID, event.DevEUI, event.FPort, event.Hex, event.Confirmed}, &s.rec.Dropped)
	}
	return len(b), nil
}

// chirpstack adds a tenant path prefix to the existing queue handler. It uses that handler's
// bearer validation, body cap, DevEUI/port/base64 checks, queue id and gRPC refusal codes.
func (s *Sink) chirpstack(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || len(parts[1]) > 128 || strings.Trim(parts[1], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" || parts[1] == "" {
		http.Error(w, "invalid tenant", 400)
		return
	}
	tenant := parts[1]
	s.mu.Lock()
	if s.queues == nil {
		s.queues = map[string]*lorawan.Downlink{}
	}
	d := s.queues[tenant]
	if d == nil {
		if len(s.queues) >= 256 {
			s.mu.Unlock()
			http.Error(w, "queue tenant bound reached", 503)
			return
		}
		var err error
		d, err = lorawan.NewDownlink(sim.HarborpulseQueueKey(tenant), "ack", []string{sim.HarborpulseSamplingDevEUI, "70B3D57ED0A10002", "70B3D57ED0A10003", "70B3D57ED0A1000F"}, queueLog{s, tenant})
		if err != nil {
			s.mu.Unlock()
			http.Error(w, fmt.Sprint(err), 500)
			return
		}
		d.LimitQueued(MaxRecords)
		s.queues[tenant] = d
	}
	s.mu.Unlock()
	if len(parts) == 4 && parts[2] == "pending" {
		if r.Method != http.MethodGet {
			http.Error(w, "GET required", 405)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+sim.HarborpulseQueueKey(tenant) {
			http.Error(w, "invalid bearer key", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.TakePending(parts[3]))
		return
	}
	d.ServeHTTP(w, r)
}
